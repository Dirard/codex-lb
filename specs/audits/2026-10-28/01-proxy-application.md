# 01. Аудит прикладного слоя проксирования (orchestration)

Зона: `internal/application/proxy.go`, `response_*.go`, `responses_lite.go`, `capability.go`, `affinity.go`, `account_admission.go`, `usage_uncertainty.go`, `codex_operations*.go`, `codex_compact.go`, `compact_replay.go`, `conversation_identity.go`, `ports.go`, `response_ports.go`, `internal/domain/**`, а также записи в SQLite, которые этот слой делает на горячем пути.
Дата: 2026-10-28. Коммит: `0b674e97` (`main`, без изменений в отслеживаемых файлах).

## Краткое резюме

- Инварианты владения и settlement в `Respond` в основном соблюдены. Резерв урегулируется до записи health (`settle`, затем `RecordAccountOutcome`). Исключённые аккаунты действительно выходят из выбора. Failover происходит только при явном quota refusal и только пока клиент ничего не видел. File-pinned запросы не переносятся на другой аккаунт. P0 не найдено.
- Найден один подтверждённый тестом дефект корректности уровня P1 (APP-5): запрос с серверным `conversation` при quota failover переотправляется на другой аккаунт уже без `conversation`, то есть без контекста. Ответ при этом успешный.
- Задержку до первого токена определяют не CPU и не блокировки, а **число синхронных durable-коммитов** через единственный writer SQLite (WAL + `synchronous=FULL`):
  - до dispatch идут 2 последовательных коммита (`ReserveUsage`, затем `SaveAffinity`);
  - после стрима ещё 4–5 коммитов, и всё это время терминальное событие удерживается;
  - при 8 параллельных запросах pre-dispatch p50 = **39 ms на NVMe против 8.8 ms на in-memory БД**. Это совпадает с 36.6 ms из `context.md`.
- На втором месте по стоимости разбор JSON. Тело запроса разбирается ≥7 раз только в этом слое, из них 3 раза в generic `any`. Итог: **34 ms CPU на 1 MiB** запроса при нижней границе 1.9 ms (`json.Valid`). Это объясняет TTFT 10–18 s при всплесках по 256–512 запросов с входом 256 KiB–1 MiB.
- При выборе аккаунта на каждый кандидат делается 3–4 чтения из БД (N+1); с production `ResolvePrice` при 60 аккаунтах выходит 181 запрос.
- Хранение continuation растёт как O(N²) по длине диалога. Alias-строки дублируют зашифрованный контекст, общий лимит 128 MiB вытесняет записи по `created_at`.
- Упрощение: `Respond` занимает 372 строки, блок `finish` повторён 4 раза, есть мёртвый код, `ProxyStore` описан шире, чем используется, чистые правила выбора аккаунта лежат в application, а не в domain.

## Измеренные цифры

Стенд: i9-14900K, NVMe ext4 (заполнен на 96%), Go 1.26.6, `modernc.org/sqlite`, WAL + `_synchronous=2`. Провайдер заменён синтетической заглушкой, без сети. Бенчмарки добавлялись в пакет через `go test -overlay`, дерево репозитория не менялось. Исходники, overlay-файлы, сырые выводы и pprof лежат в `.local/audit/scratch-proxy-app/`.

Воспроизведение:
`AUDIT_DB_DIR=$PWD/.local/audit/scratch-proxy-app/db go test -overlay .local/audit/scratch-proxy-app/overlay-e2e.json -run '^$' -bench BenchmarkAuditPreDispatch -benchtime=200x ./internal/application/`.
Дополнительные переменные: `AUDIT_DB_MEMORY=1` — БД в памяти, `AUDIT_PROD_PRICE=1` — production `ResolvePrice`.

### 1. Сквозной путь `Respond` с реальной SQLite и заглушкой провайдера

| Сценарий | pre-dispatch p50 (NVMe) | pre-dispatch p50 (in-memory) | `Respond` целиком p50 (NVMe) |
|---|---|---|---|
| новый запрос, 2/20/60 аккаунтов, последовательно | 15.9 / 14.8 / 15.3 ms | 1.75 / 2.29 / 2.91 ms | 48.6 / 45.4 / 45.7 ms |
| `previous_response_id`, история 4 KiB / 256 KiB / 1 MiB | 4.8 / 7.1 / 14.4 ms | 0.6 / 2.8 / 7.8 ms | 17.7 / 28.0 / 56.7 ms |
| 8 параллельных новых запросов, вход 2 KiB / 256 KiB | **39.1 / 73.3 ms** | **8.8 / 20.6 ms** | 121 / 196 ms |

Разбивка по операциям, среднее на запрос, NVMe, последовательно, 20 аккаунтов:

| Операция | Время |
|---|---|
| `ReserveUsage` | 6.9 ms |
| `SaveAffinity` | 8.0 ms |
| все чтения вместе | ≈1.6 ms |
| после стрима: `SettleUsage` | 8.0 ms |
| после стрима: `RecordAccountOutcome` | 8.3 ms |
| после стрима: `SaveContinuation` | 8.1 ms |
| после стрима: `SaveSessionContinuation` | 8.3 ms |

При c=8 каждый коммит занимает 19–32 ms, потому что запросы стоят в очереди к единственному writer.

Число чтений при 60 аккаунтах с production price: `GetModelPriceOverride` 60 раз, `ListAccountQuota` 60, `LoadAccountCreditStatus` 60, плюс ещё 7. Всего ≈2.0 ms в памяти, при конкуренции 3–4 ms.

### 2. CPU разбора запроса, 1 ядро (`-cpu 1`), Codex-подобное тело

Тело содержит сообщения, reasoning с `encrypted_content`, `function_call` и вывод инструментов.

| Шаг | 32 KiB | 256 KiB | 1 MiB | Аллокации на 1 MiB |
|---|---|---|---|---|
| `ParseCapabilitySignal` (Decoder) | 0.20 ms | 1.24 ms | 4.2 ms | 4.2 MB |
| `decodeResponseObject` (Decoder) | 0.33 | 1.3–2.0 | 4.8–7.4 | 4.2 MB |
| `responseInput` (копирует input) | 0.21 | 1.4 | 6.3–7.1 | 1.1 MB |
| `accountBoundItems` (generic `any`) | 0.18 | 1.3–1.5 | 4.1 | 1.7 MB |
| `validateCompactTrigger` + `inputUsesResponsesLite` | 0.25 | 1.55 | 6.9–8.0 | 0.4 MB |
| `collectFileIDs` (generic `any`) | 0.15 | 1.0 | 4.0 | 1.6 MB |
| `autoTransportRequiresHTTP` (generic `any`) | 0.24–0.28 | 1.5–2.3 | 4.5–7.3 | 5.8 MB |
| **сумма pre-dispatch в application + `shape` из httpapi** | **1.35 ms** | **8.0 ms** | **34 ms** | **13.2 MB** |
| нижняя граница: один `json.Valid` | 0.09 | 0.48 | 1.9 | 0 |
| прототип: byte-префильтр вместо двух generic decode | 0.08–0.09 | 0.5–0.66 | 2.0 | 0 (было 8.1 ms) |
| post-stream `remember` (marshal, `accountBoundItems`, unmarshal) | 0.45 | 2.9 | 11.3 | 3.9 MB |

pprof для параллельного прогона в памяти: на `encoding/json` уходит больше 55% CPU. `parseResponse` занимает 25% cum, `remember` 21.7%, `saveContinuation` 9.7%, `accountCandidates` 8%, `autoTransportRequiresHTTP` 6.9%.

### 3. Прочие замеры

- Account admission под насыщением, «стадо» пробуждений: 50 ожидающих дают 34 µs на передачу слота, 200 — 311 µs, 500 — 360 µs (32 ядра). На одном ядре 13 / 49 / 56 µs.
- Проверочные тесты (overlay):
  - (а) `parseResponse` сохраняет `ResponsesLite=true`, но пересчёт в `codex_compact.go:135` даёт `false` (APP-11);
  - (б) alias-строка `session:` хранит ту же копию `context_encrypted`, 273 380 байт (APP-6);
  - (в) при quota failover `conversation` молча вырезается (APP-5).
- Сбой при бенчмарке: с дефолтными границами (128 MiB) и историей 1 MiB первый ответ цепочки вытеснялся примерно через сотню ходов, и запрос падал с `previous_response_not_found` (APP-6).

## Таблица находок

| ID | Приоритет | Категория | Доказательство | Почему важно | Предложение | Трудоёмкость | Риск | Как проверить |
|---|---|---|---|---|---|---|---|---|
| APP-1 | P1 | perf | `proxy.go:282` `p.store.ReserveUsage(...)`, затем `proxy.go:353` `affinity.save(ctx, p.store, account.ID, id)`; `sqlite/affinity.go:40-50`: `SaveAffinity` в отдельном `s.write` заново читает резерв `WHERE r.id=? ... r.status='reserved'` | Два последовательных fsync-коммита до dispatch. `SaveAffinity` занимает 7.4–10.8 ms, это примерно половина pre-dispatch нового запроса на NVMe. При этом affinity — мягкая подсказка, а её сбой роняет запрос с 503 `affinity_persistence_failed` (`proxy.go:354`) | Делать резерв и CAS affinity одной write-job. Например, добавить в `domain.ReservationRequest` необязательное поле `Affinity *AffinityCAS{Binding, ExpectedVersion}`, а адаптер выполнит обе операции в одном callback. Порядок и семантика сохраняются: affinity пишется только при валидном резерве и до dispatch | M | Низкий/средний: CAS по версии и проверку route нужно сохранить в той же транзакции | Счётчик коммитов на запрос (fake writer или timed store), должно стать 1. Существующие `TestRequestAffinity*` и `affinity_*_test.go` в httpapi. Бенчмарк `BenchmarkAuditPreDispatchFresh` |
| APP-2 | P1 | perf/reliability | После стрима: `proxy.go:388` `settle` → `SettleUsage`; `proxy.go:412` `RecordAccountOutcome`; `proxy.go:454` `remember` → `response_ownership.go:289` `SaveContinuation` и `:295` aliases (`conversation_identity.go:132,140`). Терминальное событие удерживается до `proxy.go:458` `output.finish()`. `sqlite/continuation.go:63` использует `transact(ctx, s.db, ...)` мимо batch writer, а на каждое сохранение делает `SELECT count(*),coalesce(sum(length(context_encrypted)),0) FROM continuations` (`:116`) | 4–5 durable-коммитов на запрос через один writer: около 30 ms задержки завершения на NVMe. Эта же очередь поднимает pre-dispatch у параллельных запросов с 8.8 ms (память) до 39 ms (NVMe) при c=8; `context.md` фиксирует 36.6 ms при c=8. Continuation-транзакции занимают единственное writer-соединение и не группируются | Один порт `FinalizeAttempt(ctx, {Settlement, Outcome?, Continuation?, Aliases[]})`, одна write-job в batch writer. Шифрование и marshal выполнять до job. Агрегаты count/bytes держать в отдельной meta-строке, а не сканировать таблицу | M–L | Средний: нужна атомарность settlement + ownership и сохранение fence по `reservation_rowid` | Счётчик коммитов (цель — 1 после стрима). Тест «сбой записи continuation → settlement не потерян, ответ не опубликован». Бенчмарк c=8 |
| APP-3 | P1 | perf | Generic `any`: `response_request.go:265-268` (`accountBoundItems`), `codex_operations.go:448-449` (`collectFileIDs`, через `proxy.go:182`), `response_transport.go:196-199` (`autoTransportRequiresHTTP`, на каждой попытке, `proxy.go:267`). Decoder, буфер которого копирует всё тело: `response_object.go:16`, `capability.go:274`. Ещё отдельные проходы: `response_request.go:142,148,153`. Из соседних зон: `httpapi/proxy.go:171` `shape`; WS ingress `proxy_websocket.go:89,94,139` (map плюс 2 раза `ParseCapabilitySignal`); `provider/provider.go:331`, `chatgpt_request.go:90` | 34 ms CPU и 13 MB мусора на каждый 1 MiB запроса до dispatch, против нижней границы 1.9 ms. При всплесках в 1 vCPU это и даёт TTFT 10–18 s из `context.md` | Поэтапно. (a) Byte-префильтр перед generic decode: `\u`, `"file-`, `"file_`, `container_id`, `vector_store_id`, `input_audio`, `item_reference`, `compaction`, `additional_tools`, для транспорта ещё `image`; если ни одна подстрока не найдена, decode не нужен. Прототип: 8.1 → 2.0 ms/MiB, 0 аллокаций. (b) Один типизированный проход по items: type, call_id, account-bound, file_ids, Lite, trigger. (c) Один top-level скан, общий для capability, stream-флага и `decodeResponseObject`; передавать разобранный объект провайдеру через `ResponseTarget`, а не сырые байты | a: S, b: M, c: L | Средний: расхождение в детекции account-bound | Fuzz/property-тест «старая и новая детекция совпадают», включая `\u`-экранированные ключи. Существующий `FuzzDecodeResponseObject`. Бенчмарки `BenchmarkAudit*` |
| APP-4 | P2 | perf | `response_ownership.go:27-35`: на каждом ходе с `previous_response_id` до dispatch выполняются `p.cipher.Decrypt(owner.ContextEncrypted)` и `json.Unmarshal(plain, &history)`. До dispatch история нужна только при `owner.QuotaRefused` (`proxy.go:211-213, 247-255`), иначе только после стрима (`:429, :454, :493`) | +2.2 ms TTFT на 256 KiB и +7 ms на 1 MiB истории на каждом ходе (замер в памяти, без fsync). Именно такой профиль у WS-сессий Codex | Загружать историю лениво. Если `owner.QuotaRefused` — сразу, как сейчас. Иначе запускать decrypt и unmarshal в горутине при dispatch и ждать её перед replay или `remember`; горутина ограничена контекстом запроса и обязательно ожидается | S–M | Низкий: нужно не потерять ожидание горутины на всех путях `return` | `BenchmarkAuditPreDispatchContinuation`. Существующие replay-тесты (`TestExistingThreadAtZeroAndQuotaOnlyFailover`, `response_continuation_test.go`). Race-прогон |
| APP-5 | P1 | bug | `response_request.go:343` `delete(object, "conversation")` в `replayBody`. В `proxy.go:473` при failover проверяются только `FilePinned` и context владельца; `conversation` нигде не считается account-bound. Для compact replay он явно запрещён (`compact_replay.go:94-96`). Тест: попытка 0 ушла с `{"conversation":"conv_123","input":"only the newest user message"}`, попытка 1 на account-b — с `{"input":[{"content":"only the newest user message","role":"user"}],...}`, ответ успешный | Молчаливая потеря контекста: другой аккаунт отвечает без истории, клиент получает «успех». Нарушается правило «reject unsafe account/provider continuation» | Непустой `conversation` трактовать как account-bound: выставлять `FilePinned` или отдельный `ProviderStateBound`; при quota refusal возвращать `replayUnavailable()`, как это сделано для compact. Отдельно решить, нужно ли закреплять такие запросы за аккаунтом или отклонять их без владельца. Нужна OpenSpec-дельта (proxy-routing) | S | Низкий | Регрессионный тест из `.local/audit/scratch-proxy-app/verify_external_test.go` (`TestAuditConversationStrippedOnQuotaReplay`): ожидать 409 `quota_failover_requires_context` и одну попытку |
| APP-6 | P2 | reliability/perf | `response_ownership.go:271-285`: каждый `response_id` хранит полную накопленную историю (`history + input + output`). `conversation_identity.go:130-140`: `alias := owner` копирует `ContextEncrypted` в `session:` и `session:turn:` (проверено: 273 380 байт в обеих строках). `proxy.go:105-110`: общий лимит 4096 записей и 128 MiB; вытеснение по `created_at` (`sqlite/continuation.go:114-136`) | Хранилище растёт как O(N²) по числу ходов, плюс 2–3-кратное дублирование alias-строками. Расчёт: при 10 активных пользователях с контекстом 0.5 MiB и ходом раз в 10 s лимит 128 MiB перезаписывается примерно за 1–2 минуты. Пользователь, который сделал паузу, получает 409 `previous_response_not_found`. Насколько это мешает на практике, зависит от поведения клиента Codex на 409 (**гипотеза** в этой части) | Alias-строки должны ссылаться на строку ответа (`context_ref`), а не копировать blob; compact replay читать через join. Blob хранить только у последнего ответа в цепочке, у предыдущих оставлять лишь владельца. Вытеснять по LRU (last access), добавить метрику вытеснений | M–L | Средний: compact replay использует context alias-строки (`compact_replay.go:46-55`) | Тест с N ходами: размер таблицы растёт линейно. Нагрузочный тест: K сессий, пауза, проверка доступности последнего ответа |
| APP-7 | P2 | perf | `response_ownership.go:129-210`: на каждого кандидата `p.ResolvePrice` (`:130`), `ListAccountAdditionalQuotas` (`:147`), `LoadAccountCreditStatus` (`:183`), `ListAccountQuota` (`:187`), `LoadAccountQuotaRefusalAt` (`:193`). В production `ResolvePrice` → `ModelPricingService.ResolveCodex` → `GetModelPriceOverride` (`cmd/codex-lb/runtime.go:111-116`, `model_pricing.go:105,115`), хотя цена ChatGPT от аккаунта не зависит | 3–4 чтения на аккаунт: 181 запрос и ≈2 ms на 60 аккаунтах в памяти, 3–4 ms при конкуренции; с ростом пула растёт линейно. Это главный компонент pre-dispatch, когда fsync убран | (quick) Мемоизировать цену по `(Kind, model)` для ChatGPT-аккаунтов в пределах одного выбора. (M) Один порт `QuotaCandidateSnapshots(keyID)`, который возвращает аккаунты вместе с quota, credits, additional и refusal за 1–2 запроса; либо снапшот в памяти, инвалидируемый при записях | quick: S; batch: M | Низкий | Счётчики вызовов в timed store. Существующие `TestProxy*Quota*`, `TestSelectionStrategiesAndWeights` |
| APP-8 | P2 | reliability (наблюдаемость) | `proxy.go:176` `queueMS := time.Since(started)` покрывает только глобальный `acquire`. `proxy.go:292` → `response_stream.go:25` `started: time.Now()` отсчитывается после выбора аккаунта и `ReserveUsage`. Ожидание account admission до `QueueTimeout` = 15 s (`account_admission.go:181`), resolve owner, decrypt и коммиты не попадают ни в одну метрику | TTFT на дашборде занижает собственные накладные расходы балансировщика: регресс 15–70 ms и ожидание account capacity в production не видны. Требование спецификации «Local waiting ... MUST remain distinguishable» выполнено лишь частично | Записывать `AdmissionWaitMS` / `PreDispatchMS` в usage event, либо `FirstTokenMS` от начала запроса (поле `TotalLatencyMS` уже считается от `started`). Нужны схема и OpenSpec | S–M | Низкий | Тест: заглушка с задержкой в admission → поле события ≥ задержки |
| APP-9 | P2 | reliability | Внешний источник с `AllowMissingUsage`: `proxy.go:381-382` не выставляет `callErr`; `settle` → `operationUsageUncertain(200,false,nil)` = true (`usage_uncertainty.go:214`) → `settled=false` → выход в `proxy.go:421-428` раньше `remember` (`:454`) | Пользователь получает успешный ответ, но владение не публикуется, и следующий ход с `previous_response_id` получает 409. Путь в коде однозначен; **намерение не ясно**: спецификация (строки 619–621) требует удержать резерв, но про ownership ничего не говорит | Публиковать continuation и после «uncertain»-резерва, потому что владение не то же самое, что биллинг; либо явно записать текущее поведение в спецификацию | S | Средний: решение касается контракта | Тест: внешний источник с `AllowMissingUsage`, двухходовая цепочка |
| APP-10 | P2 | bug/вопрос политики (деньги) | `proxy.go:273` `InputTokens: max(1, min(8192, int64(len(wire)/4)))`; то же в `codex_operations_accounting.go:355` и `native_api.go:309` | Резерв входных токенов ограничен 8192. Запрос на 1 MiB (~250k токенов) резервирует не больше ~10k, так что лимиты ключа по токенам и кредитам превышаются на фактический размер каждого запроса в полёте. В spec/context ограничение не описано. **Гипотеза**: возможно, это унаследовано от legacy | Решение владельца. Если наследование не требуется — оценивать резерв без ограничения сверху (или ограничивать окном контекста модели) | S | Средний: меняет поведение лимитов | Тест: ключ с лимитом 100k токенов, два параллельных запроса по 1 MiB → второй отклоняется на резерве |
| APP-11 | P3 | bug | `codex_compact.go:135` `request.ResponsesLite = inputUsesResponsesLite(request.Input)`. После `parseResponse` `request.Input == nil` (`response_request.go:154`), поэтому всегда `false`, и правильное значение из `response_request.go:153` затирается (подтверждено тестом) | В compact quota-replay теряется Lite-флаг, и проверка `RequireResponsesLite` в каталоге для ключа с принудительной моделью пропускается | Удалить строку: значение уже вычислено в `parseResponse` | S | Низкий | `TestAuditCompactReplayLiteFlagLost` → регрессионный тест replay с Lite и принудительной моделью |
| APP-12 | P3 | perf | `account_admission.go:250` `a.notifyLocked()` при каждом успешном захвате; `:96-99` будит всех ожидающих; `:157-167` `fairShareLocked` за O(W·A) под `a.mu` | 50–360 µs на передачу слота при 200–500 ожидающих, плюс конкуренция за мьютекс ровно в момент перегрузки | При захвате будить только если был удалён waiter; держать очереди по аккаунтам или кэшировать агрегаты пула | S–M | Низкий | `.local/audit/scratch-proxy-app/admission_bench_test.go`. Существующие `account_admission_test.go`, `proxy_capacity_test.go` |
| APP-13 | P3 | reliability | `account_admission.go:185-266`: набор `choices` фиксируется на входе, ожидание длится до 15 s | После ожидания может быть выбран аккаунт, который тем временем стал `quota_exceeded` или `paused`. Повторной проверки нет; дальше остаётся только failover по явному отказу | После пробуждения, если ожидание длилось больше ~1 s, перечитывать кандидатов (`accountCandidates`) | M | Низкий | Тест: аккаунт приостановлен во время ожидания → выбран другой |
| APP-14 | P3 | perf | `response_ownership.go:273` `accountBoundItems(items)` проходит по всей истории на каждом ходе, хотя `previous.FilePinned` (`:274`) уже покрывает её: история и есть сохранённые items предыдущего ответа. Также `:271-281` на каждом ходе заново делает marshal и encrypt всей истории | ~4 ms на 1 MiB истории generic decode перед терминальным событием; CPU по диалогу растёт как O(N²) | `FilePinned = request.FilePinned \|\| accountBoundItems(response.Output) \|\| previous != nil && previous.FilePinned`. Повторный marshal и шифрование частично снимаются вместе с APP-6 | S | Низкий: эквивалентность гарантирована тем, что проверка поэлементная | Тест: file-ссылка только в истории → `FilePinned` сохраняется |
| APP-15 | P3 | perf | `proxy.go:128` `GetAPIKey` (4 запроса), хотя httpapi уже аутентифицировал ключ (`httpapi/proxy_auth.go:60`, `FindAPIKeyByHash` → `GetAPIKey`). `proxy.go:150` `LoadSettings` на каждый запрос (и ещё раз в httpapi). `proxy.go:202` `GetAccount` плюс `response_ownership.go:70` `ScopedAccountForOwner` для одного и того же владельца. `sqlite/continuation.go:20-26` читает blob `context_encrypted` и для alias-lookup, где нужны только поля владельца и `len>0`. `response_request.go:185` `pricing.Models()` сортирует map при каждом `canonicalModel` (2.4 µs + аллокация) | 0.3–0.6 ms лишних чтений на запрос; до ~1 ms на каждый MiB blob при alias-lookup | Передавать аутентифицированный `domain.APIKey` в `ResponseOptions`: свежесть всё равно перепроверяет транзакция `ReserveUsage`. Кэшировать настройки с инвалидацией по `SaveSettings` и `version`. Отдельный запрос «владелец без blob». Отсортированный список моделей считать один раз | S каждое | Низкий | Счётчики вызовов в timed store |
| APP-16 | P3 | simplification | `Respond` = `proxy.go:126-498` (372 строки, ~25 локальных переменных). Блок `if output.terminal != nil && callErr == nil { output.finish() }` повторён 4 раза (`:422-426, :458-462, :466-470, :474-478`). `settle` принимает 15 параметров (`:622`). Мёртвый код `selectAccount` (`response_ownership.go:226`, вызовов нет). Дубль констант `ContinuationBounds{4096,128<<20}` в `codex_operations_accounting.go:239-246` против `proxy.go:105-110`: при настраиваемых границах compact-сохранение вытеснит чужие записи до меньшего лимита. `ProxyStore` (`proxy.go:16-28`) включает CRUD-порты (`SaveAPIKey`, `DeleteAccount`, `AdvanceTOTPStep`...), которые `Proxy` не использует. Чистые правила выбора (`chooseAccount`, `EffectiveAccountQuotaStatus`, `additionalQuotaEligibility`, `planCapacity`) лежат в application, хотя по `AGENTS.md` им место в domain | Сложность сопровождения; риск ошибок при правках failover; скрытые зависимости | Разбить на `prepareRequest` → `runAttempt` → `finalizeAttempt` → `nextAttempt` (решения о replay) с хелпером `finishOutput`. Объединить параметры `settle` в структуру `attempt`. Удалить `selectAccount`. Compact должен брать границы из конфигурации `Proxy`. Сузить `ProxyStore` до реально используемых методов. Перенести чистый выбор в `internal/domain` | M | Средний: большой рефакторинг горячего пути; делать после тестов на APP-1/2 | Существующий набор тестов `internal/application` и `httpapi` (failover, capacity, drain) без изменений |
| APP-17 | P2 | reliability (**гипотеза**) | `proxy.go:182` → `response_ownership.go:39-61`: `collectFileIDs` проходит по всему `input`, включая историю, и для каждого `file_id` требует владельца, зарегистрированного через LB `CreateFile`. Больше 32 ID → 400 (`:41`), незарегистрированный ID → 409 `file_owner_not_found` (`:50-51`) | Если провайдер возвращает `file_id` в ответе (например, аннотации `container_file_citation`), а клиент с полной пересылкой отправляет их обратно, диалог ломается навсегда | Проверить на записанном fixture реального вывода Codex, без сетевого трафика. Учитывать только пользовательские вложения (`input_file` и т.п.), а неизвестные ID считать `FilePinned`, не отклоняя запрос | S–M | Средний | Fixture с аннотацией в истории → запрос не получает 409 |

## Быстрые победы (≤1 часа каждая)

1. **APP-11**: удалить `codex_compact.go:135` и добавить регрессионный тест.
2. **APP-5**: непустой `conversation` при quota failover → `replayUnavailable()` и тест из scratch (плюс OpenSpec-дельта как обязательный gate).
3. **APP-7 quick**: мемоизировать `ResolvePrice` по `(Kind, model)` для ChatGPT в `accountCandidates`. Убирает N запросов `GetModelPriceOverride` на каждый запрос.
4. **APP-3a**: byte-префильтр перед `accountBoundItems`, `collectFileIDs` и `autoTransportRequiresHTTP`, с fallback при `\u`. Замер −6 ms/MiB, 0 аллокаций; нужен fuzz-тест эквивалентности.
5. **APP-14**: инкрементальный `FilePinned` в `remember`, −~4 ms/MiB до терминального события.
6. **APP-15 мелочи**: отсортированный `pricing.Models()` считать один раз; owner-only запрос для alias; не делать `GetAccount`, если ниже всё равно вызывается `ScopedAccountForOwner`.
7. **APP-16 мелочи**: удалить мёртвый `selectAccount`; compact использует `p.config.ContinuationBounds`; ввести хелпер `finishOutput`.
8. **APP-12**: в `admittedAccount` вызывать `notifyLocked()` при захвате только если был удалён waiter.

Самый большой эффект для TTFT дают APP-1 и APP-2. Это не быстрые победы (M–L, затрагивают порты и адаптер sqlite), но по замерам они снимают основную часть разницы 36.6 ms против 13.9 ms. Число коммитов на запрос падает с 6–7 до 2. **Оценка**, не замер: при c=8 pre-dispatch приблизится к 10–15 ms.

## Замечания для соседних зон (не проверялись глубоко)

- **httpapi**:
  - лишние полные разборы тела: `proxy.go:171` (`shape`), WS `proxy_websocket.go:89` (map) и `ParseCapabilitySignal` дважды (`:94` и через `PrepareCapability` на `:139`);
  - на каждое WS-событие создаются `make(chan error,1)` и два переключения горутин (`proxy_websocket_stream.go:49-59`).
- **provider/upstream**: тело снова разбирается в `provider.go:331` (`requestModelAndStream`) и `chatgpt_request.go:90` (`NormalizeCodexResponsesLite` повторно делает unmarshal всего `input`, `responses_lite.go:99`). Разумно передавать уже разобранный объект через `ResponseTarget`.
- **sqlite**:
  - `_synchronous=2` (FULL) при WAL (`store.go:136`): каждый коммит делает fsync, это осознанный выбор ради надёжности;
  - continuation-записи идут мимо batch writer (`continuation.go:63`, `capability.go:50`);
  - на каждое сохранение continuation выполняется агрегирующий скан (`continuation.go:116`).

## Что проверено и в порядке

- Settlement до записи health: `proxy.go:388` → `:410-420`, в `codex_operations_accounting.go:207-214` порядок тот же.
- Исключённые аккаунты: `excluded[account.ID]=true` (`proxy.go:483`) и фильтр в `response_ownership.go:101`.
- Failover только при явном quota refusal, `safeZero` и `!output.visible` (`proxy.go:385-387, 465-480`); обычные 429 и таймауты в эту ветку не попадают.
- File-pinned не переносится (`proxy.go:197, 473`, `replayBody` → `accountBoundItems`).
- Блокировки и транзакции не удерживаются во время сетевого I/O: `accountLease` — счётчик, а не мьютекс; `selectionMu` и `a.mu` отпускаются до dispatch.
- Горутин `Respond` не создаёт; утечек таймеров и waiter-ов не найдено (`defer removeWaiter`, `timer.Stop`).
- Cleanup-контексты settlement и `remember` отвязаны от отмены клиента (`context.WithoutCancel`).

Временные файлы аудита находятся только в `.local/audit/scratch-proxy-app/`. Отслеживаемые файлы репозитория не изменялись: все бенчмарки и проверки подключались через `go test -overlay`.
