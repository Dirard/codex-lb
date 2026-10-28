# 02 — Аудит хранилища (SQLite, `internal/adapters/sqlite`)

Дата: 2026-10-28. Объект: `internal/adapters/sqlite/**` (11 411 строк без тестов), `cmd/codex-lb/storage.go`, `cmd/codex-lb/maintenance.go`, вызовы из `internal/application`.
Стенд: i9-14900K (32 потока), ext4 на NVMe (диск заполнен на 96 %), Go 1.26.6, `modernc.org/sqlite` v1.59.0. Все БД временные, лежат в `.local/audit/scratch-sqlite/db/`. Реальные данные пользователя не открывались, к провайдерам не обращались.

## Резюме

1. **Пропускную способность всего прокси ограничивает запись.** Через групповой writer (`s.write`) идут только 5 операций. 36 вызовов `transact(ctx, s.db, …)` и 23 прямых `s.db.ExecContext` коммитятся по одному, и каждый коммит делает полный fsync (`synchronous=FULL`, на стенде 5,5–17,6 мс). На каждый ответ приходится до трёх таких коммитов только на continuation. Измерение: `SaveContinuation` в 64 потока не масштабируется совсем (5,55 мс/оп, потолок ≈180 оп/с). Пара reserve+settle через батч-writer — 1,08 мс.
2. **Голодание writer'а ломает учёт (P1).** Батч-writer ждёт единственное соединение не дольше 5 с. Любая прямая транзакция длиннее этого срока валит все стоящие в очереди reserve/settle. Измерено: `ReserveUsage` за 6-секундной транзакцией вернул `context deadline exceeded` через 5,0 с. Реальный источник такой транзакции — `ApplyModelPriceChange`: вся история модели переоценивается в одной транзакции, 1,9 с на 60 тыс. событий, то есть ≈30 с на 1 млн.
3. **Дашборд-обзор падает при ≥~139 аккаунтах (P1).** `RecentAccountQuotaHistory` упирается в жёсткий лимит 100 000 строк, потому что история квот пишется раз в минуту на каждое окно каждого аккаунта. Измерено: на 200 аккаунтах и одних сутках истории — `quota projection history exceeds 100000 observations`, запрос занял 1,37 с.
4. **Данные растут без ограничений.** Ретенция по умолчанию выключена, а если её включить, она удаляет не больше 1000 строк в час на таблицу (24 тыс./сут). `usage_reservations` и `usage_reservation_items` не удаляются никогда (≈210–280 байт на запрос). `account_quota_history` прибавляет ≈343 КБ в сутки на аккаунт. WAL-файл не усекается (`journal_size_limit=-1`): под перекрывающимися читателями он вырос со 108 до 286 МБ и после checkpoint остался такого же размера.
5. **Отчёты дашборда работают за O(дни × запросы).** `QueryReports` за 30 дней — 3,0 с, `ListConversations` — 1,4 с, поиск по логам — 0,49 с, всё на 300 тыс. событий. Аналитические запросы делят пул из 8 читателей с горячим путём прокси: при 8 параллельных отчётах `FindAPIKeyByHash` замедляется с 0,23 до 15 мс.
6. **Выбор аккаунта — N+1.** На каждый запрос и каждый подходящий аккаунт выполняется 3–5 запросов к БД. На 100 аккаунтах это 5,8 мс, батч-вариант укладывается в 1,6 мс. Кэша prepared statements нет: SQL перекомпилируется при каждом вызове (простой запрос 5,6 мкс против 2,8 мкс подготовленного).
7. **Что хорошо.** Единственный writer плюс WAL. Изоляция задач батча через SAVEPOINT. Резервирование и settlement атомарны: проверка `status='reserved'` и fence `reset_at` в одной транзакции, двойного списания не нашёл. Транзакции через сетевой I/O не держатся (адаптер не отдаёт `*sql.Tx` наружу). Миграции идут одной транзакцией и только добавляют объекты, чужая или более новая схема отвергается. После падения процесса брошенные резервы помечаются (`MarkInterruptedReservations`). Тонкая настройка PRAGMA `cache_size`/`mmap_size` проверена и выигрыша не даёт: узкое место — CPU модернизированного C-кода, а не I/O.

## Реальная схема (снята с `Open()` на пустой БД, `user_version=33`)

Схема снята программой `.local/audit/scratch-sqlite/schemadump`. Полный DDL — `.local/audit/scratch-sqlite/schema.sql`: 44 таблицы, 70 индексов (включая autoindex), 1 триггер.

| Группа | Таблицы (явные индексы) |
|---|---|
| Аккаунты | `accounts` (PK id; trigger `remove_unusable_account_affinity`), `account_credentials`, `account_quotas` (PK account_id,window), `account_quota_history` (`idx_..._identity_time(account_id,window,observed_at DESC,id DESC)`, `idx_..._observed_at`, UNIQUE `legacy_id`), `account_usage_credits`, `account_usage_observation`, `account_additional_quotas` (+`idx_..._key_time`), `account_additional_quota_sync`, `account_quota_block_evidence`, `account_outcomes`, `account_deletions` (+`idx_account_deletions_pending`), `account_limit_warmup_attempts` (+2 индекса) |
| Группы/ключи | `account_groups`, `account_group_accounts` (+`idx_..._account`), `group_limits`, `api_keys` (UNIQUE key_hash, `idx_api_keys_group`), `api_key_accounts`, `api_key_sources`, `api_key_limits` (AUTOINCREMENT, UNIQUE(api_key_id,type,window,model)) |
| Ledger | `usage_reservations` (PK TEXT id, `idx_usage_reservations_status_age(status,updated_at)`), `usage_reservation_items` (PK reservation_id,limit_id; FK CASCADE), `usage_events` (PK request_id, UNIQUE reservation_id, `idx_usage_events_{account_time,key_time,conversation_time,time,model_request}`), `usage_totals` |
| Свёртки (живые, хотя называются legacy) | `legacy_hourly_usage` (PK из 7 колонок, `idx_legacy_hourly_key_account`, `idx_legacy_hourly_model`), `legacy_hourly_errors`, `legacy_conversation_hourly`, `legacy_import_state` |
| Маршрутизация | `continuations` (PK response_id,key_id; `idx_continuations_expiry`, `idx_continuations_oldest`), `affinity_bindings` (PK kind,key; `idx_affinity_account`, `idx_affinity_updated`), `codex_resource_owners` (+expiry,key), `capability_lineage_markers` |
| Прочее | `runtime_settings` (1 строка, ≈60 колонок), `admin_secret`, `runtime_identity`, `firewall_allowlist`, `automation_jobs`, `automation_runs` (+3), `reset_credit_redeem_requests`, `model_registry_snapshots`, `codex_price_overrides`, `legacy_model_sources` и `legacy_model_source_models` (это живые model sources), `sqlite_sequence` |

**Фактические PRAGMA и пулы.**
- Writer: 1 соединение. Читатели: 8 соединений (`store.go:26,70,101`).
- DSN: `_busy_timeout=5000`, `_foreign_keys=1`, `_synchronous=2` (FULL), у читателей `_query_only=1` (`store.go:134-140`).
- `journal_mode=WAL`, `page_size=4096`, `cache_size=-2000` (2 МиБ на соединение), `mmap_size=0`, `temp_store=0`, `wal_autocheckpoint=1000`, `journal_size_limit=-1`, `auto_vacuum=0`.
- `ANALYZE` / `PRAGMA optimize` не вызываются нигде.
- Транзакции открываются через `BEGIN DEFERRED`. Кэша prepared statements нет ни в драйвере, ни в коде (`PrepareContext` в адаптере не используется).

**Размер на запрос** (`dbstat`, 300 тыс. событий):
- `usage_events` с 7 индексами — ≈360 байт;
- `usage_reservations` с 2 индексами — ≈136 байт;
- `usage_reservation_items` — ≈73 байта на каждый лимит;
- `account_quota_history` — ≈119 байт на строку, из них 5,6 МиБ на 576 тыс. строк приходится на индекс `legacy_id UNIQUE`, нужный только для импорта.

## EXPLAIN QUERY PLAN горячих запросов

Данные для проверки: 200 аккаунтов, 50 ключей, 300 тыс. событий, резервов и позиций, 576 тыс. строк истории квот (1 сутки), 4096 continuations по 4 КиБ, 50 тыс. affinity. `ANALYZE` не выполнялся, как и в продакшене. Полный вывод — `.local/audit/scratch-sqlite/db/explain.txt`.

| Запрос (источник) | План | Время |
|---|---|---|
| `FindAPIKeyByHash` (`keys.go:284`) и `GetAPIKey` | SEARCH по autoindex key_hash и id; лимиты ищутся по autoindex | 18–28 мкс на запрос, **5 запросов на аутентификацию**, 53 мкс суммарно |
| `eligibleAccounts` (`keys.go:374`) с группой и source scope | SCAN accounts по PK + коррелированные EXISTS по covering-индексам | 460 мкс (100 строк) |
| `ListAccountQuota`, `LoadAccountCreditStatus`, `LoadAccountQuotaRefusalAt`, `ListAccountAdditionalQuotas`, `GetModelPriceOverride` | SEARCH по PK / autoindex | 5–19 мкс, но **по одному набору на каждый аккаунт** (N+1) |
| `GetContinuation`, `LookupAffinity`, `GetCodexResourceOwner`, settle: reservation, items, deletionPolicy | SEARCH по PK | 8–39 мкс |
| `SaveContinuation`, проверка лимитов (`continuation.go:116`) | **SCAN continuations** на каждое сохранение | 0,78 мс при 4096 строках |
| reserve: лимиты `ORDER BY id` (`ledger.go:49`) | SEARCH по autoindex + USE TEMP B-TREE FOR ORDER BY | 12 мкс (строк мало, некритично) |
| `checkAnyReservedAccountTx` (`ledger.go:243`) | SCAN accounts + коррелированные подзапросы | 191 мкс внутри транзакции writer'а |
| `ListReservationsNeedingReconciliation` / `MarkInterruptedReservations` / pending-логи | SEARCH по `idx_usage_reservations_status_age (status=?)` + TEMP B-TREE | быстро сейчас, но индекс переписывается при каждом settle |
| `FoldAndPruneRequestLogs.select` | SEARCH по `idx_usage_events_time` + TEMP B-TREE для последнего терма | 2,3 мс на 1000 строк |
| `RecentAccountQuotaHistory` (`reports_account_trends.go:73`) | SEARCH по observed_at, **USE TEMP B-TREE ×2** (окно и ORDER BY) | **1,21 с, 100 001 строка → ошибка** |
| Логи: count без фильтра | SCAN по covering-индексу `idx_usage_events_time` | 1,3 мс |
| Логи: страница с OFFSET | SCAN по idx_time + TEMP B-TREE для request_id | 0,4 мс; OFFSET 250 000 — 138 мс |
| Логи: поиск (`LIKE '%x%'` по 7 полям) | **SCAN usage_events** + 2 LEFT JOIN | 213 мс |
| Логи: options.models / options.statuses | **SCAN** + TEMP B-TREE DISTINCT и ORDER | 142 мс / 27 мс (опций 4 → 184 мс) |
| Отчёты: aggregateWindow за 30 дней | SEARCH по диапазону idx_time + TEMP B-TREE count(DISTINCT) | 251 мс на окно |
| Отчёты: median (оконная функция) | SEARCH по диапазону + TEMP B-TREE ORDER BY | 392 мс на окно |
| Отчёты: byUserAgent / countConversations | SEARCH по диапазону + TEMP B-TREE | 156 / 166 мс |
| Беседы: страница (`reports_conversations.go:37`) | **SCAN e** + TEMP B-TREE GROUP BY и ORDER BY: `trim()` не даёт использовать индекс | 186 мс + такой же count |
| reprice: батч (`model_pricing_reprice.go:155`) | SEARCH по `idx_usage_events_model_request` | 0,77 мс на 500 строк, но всё в одной транзакции |
| FK-проверки при удалении ключа или лимита | SCAN usage_reservations / items: нет индекса по api_key_id и limit_id | сейчас не срабатывает: ключи и лимиты удаляются мягко |

## Бенчмарки

Существующие тесты: `go test -run '^$' -bench . -benchmem -benchtime=3x ./internal/adapters/sqlite/` (263 609 событий).

| Бенчмарк | ns/op | B/op | allocs |
|---|---|---|---|
| AdminReports/logs_offset_0 | 2 489 758 | 72 840 | 1 928 |
| logs_offset_25 | 2 727 044 | 71 584 | 1 937 |
| logs_offset_2500 | 3 277 032 | 71 536 | 1 938 |
| **logs_offset_250000** | **138 524 152** | 71 466 | 1 937 |
| **options** | **183 767 906** | 16 552 | 357 |
| **overview_7d** | **314 581 329** | 159 074 | 2 268 |

Аудиторские бенчмарки: временный `zz_audit_tmp_test.go` в пакете, после прогона удалён; копия лежит в `.local/audit/scratch-sqlite/zz_audit_tmp_test.go`. БД — на реальном диске.

| Бенчмарк / тест | Результат |
|---|---|
| reserve+settle, последовательно, FULL | **13,36 мс/пара** (27,6 КБ, 675 allocs) |
| reserve+settle, 64 горутины (батч) | **1,08 мс/пара** (≈925 пар/с) |
| reserve+settle, последовательно, `synchronous=NORMAL` | 4,36 мс/пара |
| одиночный INSERT autocommit, FULL против NORMAL | **17,6 мс против 30 мкс** |
| `SaveContinuation` при 4096 строках, последовательно | 5,80 мс/оп |
| `SaveContinuation`, 64 горутины (прямая транзакция) | **5,55 мс/оп, масштабирования нет** |
| `EligibleAccounts` (100 из 200) | 0,74 мс, 3 895 allocs |
| выбор аккаунта N+1 (eligible + 3 запроса на аккаунт) | **5,83 мс**, 23 095 allocs |
| выбор аккаунта батчем (eligible + 1 override + 1 JOIN) | 1,59 мс, 11 943 allocs |
| `FindAPIKeyByHash` / `LoadSettings` / `GetContinuation` | 53 / 38 / 26 мкс |
| простой SELECT: ad hoc против prepared | 5,56 против 2,79 мкс |
| строка api_keys: ad hoc против prepared | 17,3 против 6,8 мкс |
| `QueryReports` 7 / 30 дней | **0,76 с / 3,04 с** |
| `ListConversations` | **1,42 с** |
| `RecentAccountQuotaHistory` (30 дней, 576 тыс. строк) | 1,37 с → **ошибка 100k** |
| `ListRequestLogs` offset 0 / поиск | 1,8 мс / 493 мс |
| `QueryDashboardTraffic` 7 дней | 369 мс |
| PRAGMA: cache 64 МиБ / mmap 256 МиБ / оба + temp_store=MEMORY (агрегат за 7 дней) | 48 → 60 / 60 / 53 мс: **выигрыша нет** |
| HOL: `FindAPIKeyByHash` при 8 параллельных `QueryReports` | среднее 0,23 → **15,1 мс**, худшее 27 мс |
| `ApplyModelPriceChange` (60 тыс. событий) | 1,90 с одной транзакцией; параллельный `ReserveUsage` ждал 1,89 с |
| `ReserveUsage` за 6-секундной прямой транзакцией | **через 5,0 с `context deadline exceeded`** |
| батч ретенции на 1000 строк: events / quota history / affinity | 69 / 30 / 9 мс |
| WAL: 3000 reserve+settle при 4 длинных читателях | 108 → 192 → **286 МБ**; после PASSIVE checkpoint файл остался 286 МБ; TRUNCATE → 0 |

## Рост данных

Скорость роста:
- **запросы** — около 0,6 КБ на запрос, при 100 тыс. запросов в сутки ≈60 МБ/сут;
- **опрос квот** — ≈343 КБ в сутки на аккаунт, при 100 аккаунтах ≈34 МБ/сут.

| Таблица | Как растёт | Очистка |
|---|---|---|
| `usage_events` | 1 строка на запрос, 7 индексов | Только при заданном `request_log_retention_days` (по умолчанию NULL). Не больше **1000 строк в час** (`maintenance.go:30`, `reports.go:184`) |
| `usage_reservations` + `usage_reservation_items` | 1 + N_limits строк на запрос | **Никогда** (DELETE отсутствует) |
| `account_quota_history` | 2880 строк в сутки на аккаунт (`accounts.go:204`, каждый опрос раз в минуту) | Только при заданном `usage_history_retention_days`, не больше 1000 строк в час |
| `affinity_bindings` | kind `sticky_thread` / `codex_session` — по строке на тред или сессию | Удаляется только `prompt_cache` (`affinity.go:128`), 1000 в час |
| `capability_lineage_markers` | по маркеру на линию | Никогда (так требует спека) |
| `legacy_hourly_*`, `legacy_conversation_hourly` | час × измерения, плюс по строке на беседу в час | Никогда (свёртки) |
| `automation_runs`, `account_limit_warmup_attempts`, `reset_credit_redeem_requests`, `account_deletions` | медленно | Никогда, кроме удаления задачи или аккаунта |
| `continuations` | ≤4096 строк / ≤128 МиБ | SCAN на каждое сохранение; blob копируется в каждый алиас |
| `codex_resource_owners`, `error_archives` | ≤100 тыс. / ≤10 тыс. | `count(*)` и оконная функция на каждую вставку |
| WAL / основной файл | WAL держит размер максимальной транзакции; файл не сжимается | `journal_size_limit=-1`, `auto_vacuum=0` |

## Находки

| ID | P | Категория | Доказательство | Почему важно | Исправление | Трудоёмкость | Риск | Как проверить |
|---|---|---|---|---|---|---|---|---|
| DB-1 | P1 | bug | `writer.go:112` (5-секундный ctx берётся до `BeginTx`); `model_pricing.go:79-106` + `model_pricing_reprice.go:149-195` (переоценка всей истории в одной транзакции); тест: `ReserveUsage` за 6-секундной транзакцией упал через 5,0 с; reprice 60 тыс. событий — 1,9 с | Любая прямая транзакция дольше 5 с (переоценка ≈32 мкс на событие, большая очистка удалённых аккаунтов, будущие миграции) валит **все** стоящие в очереди reserve/settle. Settle после успешного ответа провайдера возвращает 503 `usage_settlement_failed` (`proxy.go:673`), резерв висит до рестарта. | (1) Перевести все записи на `s.write` (см. DB-2): тогда writer'у некого ждать, и дедлайн покрывает только выполнение. (2) Переоценку разбить на задачи по ≈500 событий: сначала одной транзакцией сохранить override (новые settle сразу считают по новой цене, требование спеки «obsolete price» сохраняется), затем курсор в таблице `reprice_progress(model, after_request_id)` и батчи через `s.write`, после рестарта — продолжение. Нужно изменение OpenSpec: согласованность агрегатов становится eventual. | M | средний: меняется семантика атомарности переоценки | регресс-тест: параллельно переоценке 1 млн событий идут `ReserveUsage`/`SettleUsage` без ошибок; p99 settle < 50 мс |
| DB-2 | P1 | perf | 36× `transact(ctx, s.db…)`, 23× `s.db.ExecContext` против 5× `s.write`; `continuation.go:63,144`, `conversation_identity.go:128-141` (до 3 сохранений на ответ), `capability.go:50`, `error_archive.go:27`, `codex_resource_owners.go:23`, `ledger.go:314`; бенч: `SaveContinuation` в 64 потока — 5,55 мс/оп, на стенде fsync 17,6 мс | Прямые коммиты не попадают в групповой commit, и каждый платит полный fsync на единственном соединении. Получается потолок ≈60–180 прямых транзакций в секунду на всю инсталляцию, а на запрос их ≥3. Пока такая транзакция держит соединение, батч-writer тоже стоит. | Механическая замена `transact(ctx, s.db, func(tx)…)` → `s.write(ctx, func(ctx, tx)…)`, одиночных `s.db.ExecContext` → `s.write`. Исключения: `migrate()` и `ImportLegacySnapshot` (до старта writer'а). Сохранение continuation, session и turn-state алиасов собрать в один job (`SaveContinuations([]…)`). Поднять `writeBatchSize` с 16 до 64–128 (savepoint изолирует задачи). | M | низкий: семантика транзакций та же; следить, чтобы job не делал долгой работы | бенч `SaveContinuation` в 64 потока должен дать ≤1 мс/оп; нагрузочный e2e прокси |
| DB-3 | P1 | bug | `reports_account_trends.go:89-92` (жёсткий лимит 100 000), вызов `reports_dashboard.go:173` и `reports_projections.go:20` (окно 30 дней), `accounts.go:204` (история пишется на каждый опрос), `account_usage.go:370-407` (опрос всех аккаунтов раз в минуту); тест: 200 аккаунтов × 1 сутки → ошибка | На каждый ряд в 6-часовом горизонте приходится 360 строк. При ≥~139 аккаунтах с двумя окнами обзор дашборда и проекции возвращают ошибку. Ниже порога оконная функция всё равно сортирует всю 30-дневную историю: 1,37 с на 576 тыс. строк, ≈8,6 млн строк при 100 аккаунтах. | (1) Писать историю только при изменении `used_percent`/`reset_at`/`window_minutes` или не чаще чем раз в N минут: `INSERT … SELECT … WHERE NOT EXISTS(последняя строка совпадает и моложе 5 мин)`. (2) Переписать запрос на выборку по рядам через индекс identity_time: `account_quotas s JOIN account_quota_history h ON h.id IN (SELECT id … WHERE account_id=s.account_id AND window=s.window ORDER BY observed_at DESC,id DESC LIMIT 64)` плюс горизонт, прореженный до `weekly_pace_smoothing_minutes` (12 точек на 6 ч вместо 360). (3) Убрать лимит 100k. Изменение OpenSpec для weekly pace. | M | средний: влияет на расчёт pace | тест со 1000 аккаунтов × 30 дней: обзор < 200 мс без ошибки |
| DB-4 | P1 | growth | `maintenance.go:30-46` (раз в час по одному батчу), `reports.go:184,194` (limit 1000), `runtime_settings.request_log_retention_days` / `usage_history_retention_days` по умолчанию NULL; батч на 1000 строк — 69 / 30 мс | По умолчанию ретенции нет. При включённой удаляется ≤24 тыс. строк в сутки, а пишется больше уже при >24 тыс. запросов в сутки или ≥9 опрашиваемых аккаунтах, так что очистка никогда не догоняет. | Цикл батчей по 500 строк через `s.write` с бюджетом времени (≈20 с за цикл) до `n<limit`. Значение ретенции по умолчанию (например, events 90 дней, quota history 30 дней) или явный показ «без ограничения» в UI — через OpenSpec и simplicity-gate. | S | низкий | интеграционный тест: 100 тыс. просроченных строк удаляются за один цикл |
| DB-5 | P2 | growth | DELETE из `usage_reservations` / `usage_reservation_items` отсутствует (grep по адаптеру); `dbstat`: 23,5 + 7,2 + 8,2 + 13 + 7,8 МиБ на 300 тыс. запросов | ≈210–280 байт на запрос навсегда, даже с ретенцией логов. Индекс `status_age` переписывается при каждом settle. | Удалять в `FoldAndPruneRequestLogs` вместе с событием его reservation (items уходят каскадом), если `status!='reserved'`, на неё не ссылаются `account_outcomes.reservation_rowid` и `continuations.reservation_rowid` и это не `max(rowid)`. Rowid неявный (PK TEXT), поэтому удаление максимального сломает монотонность fence. Следующий шаг: сделать `usage_events.reservation_id` колонкой без FK и хранить settled-резервы ≈7 дней. | M | средний: fence на rowid используется в continuation, affinity и outcomes | тест: после очистки settle, quota-refusal и fence сессии работают; rowid новой резервации больше всех удалённых |
| DB-6 | P2 | perf | `reports_aggregate.go:55-67` (цикл по дням: aggregate + conversations + 3 медианы на день), `reports_aggregate.go:135-145`, `reports_dashboard.go:83-100` (conversations на каждый бакет), `reports_conversations.go:13,33,37` (`trim()`), `reports_logs.go:239+` (4 полных скана для options); бенчи: 3,04 с / 1,42 с / 184 мс / 315 мс | Тяжёлая аналитика перечитывает сырые события по 5 раз на день окна (до 731 дня → ~3600 запросов). Ответы дашборда занимают секунды и держат соединения читателей (см. DB-7). | (1) Один проход `GROUP BY` по дню: границы дней в TZ считать в Go и передавать через `json_each(?)`, бакет вычислять `CASE`/бинпоиском в Go по часовым агрегатам. (2) Сворачивать закрытые часы в `legacy_hourly_*` фоном (fold-on-write без удаления), а сырые события читать только для текущего часа. (3) Медианы считать по часовой гистограмме латентности. (4) `trim` на вставке плюс backfill `UPDATE usage_events SET conversation_id=nullif(trim(conversation_id),'') …`, после чего `GROUP BY conversation_id` идёт по индексу; ещё лучше — таблица `conversation_stats`, обновляемая при settle. (5) Опции фильтров брать из `usage_totals`-подобных справочников или из последних N дней. | L | средний: результаты отчётов должны совпасть побайтно | golden-тесты на существующих отчётах; бенч `QueryReports_30d` < 100 мс |
| DB-7 | P2 | perf | `store.go:26,101` (общий пул из 8); тест HOL: 0,23 → 15,1 мс (худшее 27 мс) | Аналитика дашборда, включая отчёты по ключу, доступные владельцу API-ключа, отнимает соединения у горячего пути прокси: аутентификация, выбор аккаунта, continuation. | Отдельный пул `analyticsDB` (2–3 соединения) для `reports_*` и `ListRequestLogs`, плюс `context.WithTimeout` на отчёты; либо семафор на аналитику поверх `readDB`. | S | низкий | повторить HOL-тест: худшее < 1 мс |
| DB-8 | P2 | growth | `store.go:134-140` (нет `journal_size_limit`), `wal_autocheckpoint=1000` на writer'е; тест WAL: 108 → 286 МБ, после checkpoint размер не уменьшился | Длинные читатели (многосекундные отчёты, `ListRequestLogs` в read-tx) не дают сбросить WAL. Большая транзакция (reprice, ретенция) навсегда раздувает файл. Автоматический checkpoint выполняется в commit writer'а и даёт ему пики латентности (гипотеза, косвенно: NORMAL при последовательной работе всё равно 4,4 мс). | `_pragma=journal_size_limit(67108864)` в DSN writer'а. Фоновый checkpointer: `wal_autocheckpoint=0` на writer'е, отдельное соединение делает `PRAGMA wal_checkpoint(PASSIVE)` раз в 1–5 с и `TRUNCATE` при простое. Аналитические read-tx держать короткими (DB-6). | S | низкий | тест WAL: после остановки читателей файл ≤64 МБ |
| DB-9 | P2 | perf | `response_ownership.go:128-196` (на каждый аккаунт `ResolvePrice` → `GetModelPriceOverride`, `LoadAccountCreditStatus`, `ListAccountQuota`, опционально `LoadAccountQuotaRefusalAt` и `ListAccountAdditionalQuotas`); бенч 5,83 против 1,59 мс | На запрос 3–5×N обращений к БД; цена одной и той же модели перечитывается N раз. С ростом числа аккаунтов (коммит «512 generations») CPU и пул читателей уходят на выбор. | (1) Порт `AccountSelectionSnapshot(ctx, ids)` — один запрос: quotas + credits + refusal + additional через `LEFT JOIN`/`IN` в одной read-tx (ещё и консистентный снимок). (2) `ResolvePrice` кэшировать на запрос по `(kind, model)`. (3) Позже — кэш в памяти с инвалидацией по версии из writer'а. | M | низкий | бенч selection < 1,5 мс на 100 аккаунтов; тесты маршрутизации |
| DB-10 | P2 | perf | отсутствует `PrepareContext`; бенч 5,6 против 2,8 мкс и 17,3 против 6,8 мкс; reserve+settle ≈29 SQL-операторов на writer'е | Каждый вызов перекомпилирует SQL. На writer'е, единственном и последовательном, это прямая потеря пропускной способности группового commit. | Кэш `map[string]*sql.Stmt` в `Store` для горячих запросов (ledger, auth, selection, continuation); в транзакциях — `tx.StmtContext(ctx, stmt)`. Для writer'а (1 соединение, `ConnMaxLifetime=0`) подготовка фактически разовая. | M | низкий | бенч parallel reserve+settle: ожидается ≈1,5–2× |
| DB-11 | P2 | perf | `continuation.go:114-130` (SCAN `count`/`sum(length)` на каждое сохранение, вытеснение по одной строке в цикле), `conversation_identity.go:128-141` (blob копируется в 2 алиаса) | 0,78 мс CPU на writer'е за сохранение, до 3 раз на ответ. Зашифрованный контекст хранится трижды, то есть фактический лимит 128 МиБ втрое меньше. | Счётчики `continuation_stats(id=1, count, bytes)` обновлять в той же транзакции (или вычислять дельтами), вытеснять одним `DELETE … LIMIT k`. Алиасы хранить без blob со ссылкой на `response_id` владельца. | M | средний: fence-логика алиасов | тест лимитов; бенч `SaveContinuation` < 0,2 мс CPU |
| DB-12 | P2 | perf | `store.go:136` (`_synchronous=2`); бенч: commit 17,6 мс против 30 мкс, пара 13,4 против 4,4 мс | FULL оправдан для ledger: резерв должен пережить отключение питания до запроса к провайдеру. Но ту же цену платят все некритичные записи, и при последовательной работе на каждый запрос уходит ≥1 fsync до TTFT. | Решение владельца. Рекомендация: оставить FULL и снизить число fsync через DB-2 и больший батч. Альтернатива для операционного состояния (affinity, quota polling, capability, continuation) — отдельный job-флаг `relaxed`, с которым writer коммитит батч под `PRAGMA synchronous=NORMAL`, если в нём нет ledger-задач. | S–M | высокий при ошибочной классификации | тест выживания после `kill -9` и симуляции потери питания |
| DB-13 | P3 | simplification | `ledger.go:310` (`TouchReservation`), `ledger.go:453` (`ReleaseStaleReservations`), `ports.go:52,54` — в продакшене не вызываются (только тесты) | Мёртвый код. `ReleaseStaleReservations` противоречит требованию спеки «never … stale-released» (`spec.md:613,619,648`). | Удалить из порта, адаптера и тестов. | S | нет | `go build`, `go test` |
| DB-14 | P3 | simplification | `legacy_import*.go`, `*_import.go`, `legacy_snapshot.go`: ≈2,4 тыс. строк кода + ≈1,3 тыс. строк тестов; `account_quota_history.legacy_id UNIQUE` (5,6 МиБ на 576 тыс. строк) | Если cutover завершён (`legacy/` уже выведен, AGENTS.md), импорт — около 21 % адаптера и поддерживать его незачем. Живые таблицы `legacy_hourly_usage` и `legacy_model_sources` названы как legacy и путают. | После подтверждения администратора удалить `import-legacy` (история останется в Git) и индекс `legacy_id` (`DROP INDEX` невозможен для autoindex UNIQUE — только через пересборку таблицы, поэтому откладывается). Watermark-предикаты `legacy_import_state` в отчётах оставить, пока в БД есть импортированные строки. Переименование таблиц — отдельно, низкий приоритет. | M | средний: бэкап старых инсталляций; нужен OpenSpec | `go test ./...` |
| DB-15 | P3 | simplification | одни и те же правила scope продублированы в `keys.go:374-410`, `ledger.go:122-185,243-268`, `keys.go:433-505`, `affinity.go:44-60` | 4–5 копий правил group/account/source scope, отвечающих за авторизацию. Расхождение одной копии — дыра в авторизации. | Общий построитель SQL-предиката `accountScopeSQL(alias, key)` и одна Go-функция проверки; тест-матрица на все комбинации. | M | средний | существующие тесты scope + таблица комбинаций |
| DB-16 | P3 | perf | `reports_logs.go:139` и пагинация через OFFSET; план: TEMP B-TREE для `request_id`; offset 250 000 — 138 мс; поиск 213–493 мс | Глубокие страницы и поиск сканируют всю историю. | `CREATE INDEX idx_usage_events_time_id ON usage_events(requested_at, request_id); DROP INDEX idx_usage_events_time;` + keyset-курсор `(requested_at, request_id) < (?, ?)`; для поиска — FTS5 по `request_id`/`error_code`/`conversation_id` или обязательный временной диапазон. | M | низкий | бенч logs_offset_250000 < 5 мс |
| DB-17 | P3 | perf | индекс `status_age` используется только с `status='reserved'` (`usage_reconciliation.go:9`, `ledger.go:281`, `reports_logs.go:26`) | Индекс в 8,2 МиБ на 300 тыс. строк переписывается при каждом settle (меняются status и updated_at); в ORDER BY id остаётся TEMP B-TREE. | `CREATE INDEX idx_usage_reservations_open ON usage_reservations(needs_reconciliation, id) WHERE status='reserved'; DROP INDEX idx_usage_reservations_status_age;` | S | низкий | EXPLAIN показывает partial index без TEMP B-TREE |
| DB-18 | P3 | growth | `affinity.go:128` (удаляется только `prompt_cache`); `capability_lineage_markers`, `automation_runs`, `account_limit_warmup_attempts` без очистки | Медленный, но неограниченный рост. | TTL для `sticky_thread` и `codex_session`, синхронизированный со сроком жизни сессии; для runs и attempts хранить последние N на задачу или аккаунт. Маркеры не трогать без решения по спеке. | S | низкий | — |
| DB-19 | P3 | perf | `error_archive.go:42-43` (оконная функция по всем ≤10 тыс. архивам на каждую вставку), `codex_resource_owners.go:56` (`count(*)` по ≤100 тыс. на вставку) | O(N) работы writer'а на каждую ошибку или файл. | Счётчики в отдельной строке, обновляемые в той же транзакции; вытеснение по индексу `occurred_at`. | S | низкий | — |
| DB-20 | P3 | bug | `automations.go:128-141`: удаление задачи и её runs — два autocommit, ошибка второго игнорируется (`_, _ =`) | Не атомарно: остаются осиротевшие runs. | Одна транзакция через `s.write`, ошибку возвращать. | S | нет | unit-тест |
| DB-21 | P3 | bug (гипотеза) | `ledger.go:409-412`: `… AND current_value+?>=0` молча пропускает отрицательную дельту | Если переоценка уменьшила `current_value` в том же окне, освобождение старого резерва не применится, и ключ останется «пережжённым». Не воспроизведено. | `SET current_value=max(0,current_value+?)`; проверить `RowsAffected`, расхождение логировать. | S | низкий | тест: reserve → reprice вниз → release |
| DB-22 | P3 | migration/ops | `store.go:147-196` (миграции только вперёд, одной транзакцией), `ANALYZE`/`optimize` отсутствуют, `auto_vacuum=0` | Миграции корректны и идемпотентны (атомарны), пересборок таблиц нет, поэтому FK-ловушек нет. Откат бинаря после миграции невозможен без бэкапа (`update_backup.go`, ожидаемо). Планировщик работает без статистики, файл после чистки не сжимается. | `PRAGMA optimize` при `Close()` и раз в сутки в maintenance; офлайн-команда `codex-lb vacuum` (VACUUM INTO + swap). PRAGMA `cache_size`/`mmap_size` **не** менять: бенч выигрыша не показал. | S | нет | — |
| DB-23 | P3 | simplification | `ledger.go:276-307` и `keys.go:294-320` (сначала id, затем `Get` по каждому), чтения на writer-пуле (`ledger.go:457`, `automations.go:164`, `groups.go:202`, `account_policy.go:82`), `store.go:58-60` (относительный путь превращается в `file://rel` → ошибка authority) | Лишние запросы; чтения конкурируют с записью за единственное соединение; ловушка в API `Open`. | Один SELECT со всеми колонками; чтения перенести на `readDB` или внутрь write-job; в `Open` использовать `filepath.Abs`. | S | нет | — |

## Быстрые победы (≤1 часа каждая)

1. **DB-8.** Добавить `_pragma=journal_size_limit(67108864)` в DSN writer'а (`store.go:sqliteDSN`) и `PRAGMA optimize` в `Close()`. Около 10 строк.
2. **DB-4.** Цикл батчей ретенции в `maintainReportRetention` до `n<limit` с бюджетом 20 с (код уже ограничен по размеру батча). Значения по умолчанию — отдельным изменением OpenSpec.
3. **DB-9.** Кэшировать `ResolvePrice` на время одного вызова `accountCandidates` (`map[kind+model]`): минус 1–2 запроса на каждый аккаунт.
4. **DB-2 (частично) и DB-23.** `writeBatchSize` 16 → 64. Перевести самые частые прямые записи на `s.write`: `SaveContinuation`/`SaveSessionContinuation`, `RequireCapability`, `SaveCodexResourceOwner`, `SaveErrorArchive`, `TouchReservation`. Это механическая замена сигнатуры callback'а.
5. **DB-17.** Миграция v34: partial index для открытых резервов, `DROP INDEX idx_usage_reservations_status_age`.
6. **DB-13.** Удалить `TouchReservation` и `ReleaseStaleReservations` вместе с их тестами.
7. **DB-20.** Сделать `DeleteAutomationJob` одной транзакцией и перестать проглатывать ошибку.
8. **DB-7.** Семафор на 2–3 одновременных аналитических запроса (reports, logs, conversations) поверх `readDB`.

## Порядок работ (предложение)

1. **Стабильность:** DB-1, DB-2, DB-3, DB-4.
2. **Пропускная способность:** DB-9, DB-10, DB-11, решение по DB-12.
3. **Дашборд:** DB-6, DB-7, DB-16.
4. **Рост и упрощение:** DB-5, DB-8, DB-13–DB-15, DB-17–DB-23.

Изменения поведения (ретенция по умолчанию, дедупликация истории квот, неатомарная переоценка, очистка резервов) по AGENTS.md требуют OpenSpec-change до кода.

## Артефакты

- Схема: `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/schema.sql`.
- Планы: `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/db/explain.txt`.
- Размеры объектов: `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/db/dbstat.txt`.
- Бенчмарки:
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/bench_existing.txt`
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/bench_audit_hot.txt`
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/bench_audit_reports.txt`
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/bench_audit_direct.txt`
- Тесты-эксперименты:
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/db/starvation.txt`
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/db/wal.txt`
  - `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/db/hol.txt`
- Код временного теста (в пакете удалён): `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/zz_audit_tmp_test.go`.
- Программа дампа схемы: `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-sqlite/schemadump/main.go`.
