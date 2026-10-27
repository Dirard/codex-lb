# 07. Качество кода и гигиена репозитория codex-lb (Go)

Дата: 2026-10-28. Ревизия: `main` @ `0b674e97`. Ничего из отслеживаемых файлов не менялось; `go mod tidy` не применялся (сравнение `go.mod`/`go.sum` до и после — без изменений). Полные выводы лежат в `.local/audit/scratch-quality/`.

Инструменты: Go 1.26.6, golangci-lint 2.5.0 (конфига в репозитории нет), gofumpt, `deadcode@latest`, govulncheck, OpenSpec CLI 1.12.0 (взят из кэша npx без установки, `OPENSPEC_TELEMETRY=0`).

## Резюме

1. **С корректностью кода в целом всё хорошо, с гигиеной процесса — плохо.** `go vet` и `gofmt` проходят чисто, staticcheck не нашёл ни одного дефекта класса SA (только стилистические QF/ST), все 680 тестов верхнего уровня зелёные. Clean architecture соблюдается: `domain` импортирует только stdlib, `application` не импортирует `adapters` и `net/http`.
2. **Безопасность сборки (P1).** Все опубликованные бинарники, включая `go-v1.0.10`, собраны Go 1.26.6. govulncheck находит 11 достижимых уязвимостей stdlib (net/http, crypto/tls, crypto/x509, net/textproto); исправлены в 1.26.9 и 1.26.10. Строки `toolchain` в `go.mod` нет.
3. **CI отсутствует полностью** (`.github/` нет). AGENTS.md при этом описывает ворота CI/CodeRabbit, `.github/CONTRIBUTING.md`, `PRINCIPLES.md`, `docs/`, `CHANGELOG.md`, `.env.example`, `uv run pytest`, Alembic и AsyncSession. Ничего из этого в дереве нет.
4. **OpenSpec завален наследием.** Все 188 активных changes созданы между 2026-07-15 и 2026-09-06, то есть до переписывания на Go (архив `2026-09-27-rewrite-codex-lb-in-go`). Ни один не упоминает Go. У 170 все задачи отмечены выполненными, у 122 ADDED-требования так и не попали в основные specs, 21 не проходит `openspec validate`. Изменения Go-эпохи архивируются сразу, так что активный список — это просто мусор. Из 61 capability к Go относятся 2, смешанных 5, ещё 25 описывают функциональность, которой в Go нет.
5. **Сложность сосредоточена на денежном и ownership-пути.** `Proxy.Respond` занимает 373 строки, 284 statement, когнитивная сложность 270. Дальше идут `websocketSessions.acquire` (132), `Proxy.accountCandidates` (110) и `ProxyHandler.websocket` (108).
6. **Тесты медленные и без параллелизма.** `t.Parallel()` нет ни одного. Пакет `httpapi` идёт 101 с, `sqlite` — 79 с, оба последовательно. Самый медленный тест (15 с) упирается в зашитый таймер `15*time.Second`. Покрытие: 70,1% по пакетам и 80,4% кросс-пакетно. HTTP-обработчики пароля и TOTP администратора покрыты на 0%.
7. **Зависимости.** В `go.mod` все модули помечены `// indirect`, `go mod tidy -diff` выдаёт ненулевой diff. Доступны обновления modernc sqlite/libc и x/crypto/x/sys.

## 1. Статический анализ

### 1.1 Сводка golangci-lint (запуск без конфига, `--enable-only=govet,staticcheck,unused,errcheck,ineffassign,gocritic,revive,bodyclose,noctx,contextcheck,errorlint,prealloc,unparam`; gosimple в v2 входит в staticcheck)

Всего 1710 замечаний: 1041 в production-коде и 669 в тестах. Полный вывод: `golangci.txt` / `golangci.json`; production без errcheck — `golangci-prod-nonerrcheck.txt`.

| Категория | Всего | prod | test | Суть | Вердикт |
|---|---:|---:|---:|---|---|
| revive `exported` | 660 | 660 | 0 | нет doc-комментариев у экспортируемых имён в `internal/` | шум, правило отключить |
| errcheck | 687 | 256 | 431 | 175 × `defer rows.Close()`, 10 × `response.Body.Close`, Rollback, Close в тестах | в основном идиоматичный шум; реальный случай: `api.ConfigureAccounts(accounts)` в `cmd/codex-lb/runtime.go:120`, ошибка игнорируется |
| noctx | 122 | 8 | 114 | `http.NewRequest`, `db.Exec`, `net.Listen` без ctx | prod: `sqlite/store.go:81`, `sqlite/legacy_import.go:213`; остальное — тесты |
| staticcheck | 51 | 43 | 8 | QF1008 ×29, QF1001 ×10, QF1003 ×7, ST1005, ST1023; в тестах 2× SA4006 | дефектов SA в prod нет |
| revive (прочее) | 82 | 29 | 53 | unused-parameter 52, redefines-builtin-id 12 (`cap`, `close`, `copy`), var-naming 3 (`ProxyApiKey…` в `domain/models.go:242-244`), blank-imports 2 | точечно |
| bodyclose | 29 | 1 | 28 | prod: `provider/operations_realtime.go:51` | ложное срабатывание: `coder/websocket.Dial` не требует закрывать resp.Body |
| contextcheck | 27 | 14 | 13 | `context.Background`/`s.ctx` в фоновых задачах | prod-случаи намеренные (контекст живёт столько же, сколько сервис) |
| errorlint | 15 | 6 | 9 | `err == errSeatMismatch`, `err == domain.ErrNotFound`, `probeErr == errWarmupIneligible`, `err.(*Error)` | сейчас работает, но легко ломается — QA-20 |
| unused | 5 | 4 | 1 | `isContextError`, `exportTimeFormat`, `(*AccountsService).pruneExpiredPublic`, `(*Proxy).selectAccount` | удалить |
| ineffassign | 5 | 3 | 2 | `provider/operations.go:328` (`contentType`) — **реальный дефект**, QA-11; `upstream/chat_stream.go:57`, `upstream/response.go:108` — безвредно | |
| unparam | 10 | 4 | 6 | `updateHost.Shutdown` всегда возвращает nil и др. | |
| gocritic | 10 | 8 | 2 | ifElseChain ×7, exitAfterDefer (`main.go:27`) | косметика |
| prealloc | 7 | 5 | 2 | | косметика |

Прочее:
- `go vet ./...` — чисто (`.local/audit/vet.log`).
- `gofmt -l` — чисто.
- `gofumpt -l .` — 168 из 533 отслеживаемых `.go`-файлов (`gofumpt-tracked.txt`, `gofumpt.diff`, 354 hunk-а). Почти всё — восьмеричные литералы `0700` → `0o700` и раскладка составных литералов с несколькими полями в строке.
- Стиль: в production 251 строка длиннее 160 колонок, 72 — длиннее 200, 12 — длиннее 300. Максимум 466 символов в `internal/domain/model_catalog_bootstrap.go:41`. Типичный пример плотной упаковки — `cmd/codex-lb/runtime.go:118`, где `httpapi.New(...)` с 8 полями записан в одну строку.

### 1.2 Мёртвый код (`go run golang.org/x/tools/cmd/deadcode@latest`)

- Недостижимо даже с учётом тестов (`deadcode -test ./...`, файл `deadcode-test.txt`): `chatgpt.DefaultOAuthConfig` (`internal/adapters/chatgpt/oauth.go:37`), `isContextError` (`upstream/adapter.go:380`), `exportTimeFormat` (`application/accounts_export.go:129`), `AccountsService.pruneExpiredPublic` (`accounts_flow_state.go:128`), `Proxy.selectAccount` (`response_ownership.go:226`).
- Используется только тестами (`deadcode.txt`, от `./cmd/codex-lb`): `Server.setAccountsService`, `ProxyHandler.ServeHTTP`, `CodexOperationsHandler.ServeHTTP`, `upstream.ChatCompletionsCapabilities`, `upstream.ZAIChatCompletionsCapabilities`.

### 1.3 Сложность (golangci gocyclo/gocognit/nestif/funlen, плюс AST-подсчёт длины)

Полные данные: `complexity-top.txt`, `funcstat.txt`. В production 27 функций с gocyclo > 20 и 81 с gocognit > 30. Порог dupl 200 не сработал ни разу, дублирования крупных блоков нет.

| # | Функция | Строк | Statements | gocognit | gocyclo |
|---|---|---:|---:|---:|---:|
| 1 | `application.(*Proxy).Respond` — `internal/application/proxy.go:126` | 373 | 284 | **270** | — |
| 2 | `httpapi.(*ProxyHandler).websocket` — `proxy_websocket.go:16` | 240 | 203 | 108 | — |
| 3 | `upstream.(*HTTPAdapter).streamResponsesWebSocket` — `responses_websocket.go:12` | 196 | 156 | 96 | — |
| 4 | `upstream.(*websocketSessions).acquire` — `websocket_sessions.go:64` | 175 | 131 | 132 | — |
| 5 | `application.(*Proxy).accountCandidates` — `response_ownership.go:64` (nestif 19) | 161 | 120 | 110 | — |
| 6 | `sqlite.InspectLegacySnapshot` — `legacy_snapshot.go:34` | 161 | 117 | 59 | — |
| 7 | `application.(*ReportsService).DashboardOverview` — `reports_dashboard.go:29` | 151 | 123 | — | — |
| 8 | `upstream.newToolCatalog` — `request.go:94` | 151 | 89 | 89 | — |
| 9 | `sqlite.(*Store).SaveModelSource` — `model_sources.go:15` | 148 | 73 | 63 | — |
| 10 | `application.(*CodexOperations).billedAdmitted` — `codex_operations_accounting.go:83` | 140 | 98 | 65 | — |
| 11 | `httpapi.(*Server).registerAutomationsRoutes` — `automations.go:17` | 140 | 90 | — | — |
| 12 | `main.openRuntime` — `cmd/codex-lb/runtime.go:45` (единственный funlen > 100 по правилам funlen) | 139 | 111 | — | — |
| 13 | `upstream.translateToChat` — `messages.go:10` | 138 | 93 | 73 | — |
| 14 | `sqlite.prepareUsageIdentityTx` — `account_usage_identity.go:17` | 136 | 88 | 61 | — |
| 15 | `application.(*Proxy).chooseAccount` — `response_selection.go:75` | 135 | 101 | 58 | — |
| 16 | `application.(*CodexOperations).CompactWithOptions` — `codex_compact.go:14` | 133 | 102 | 82 | — |
| 17 | `sqlite.(*Store).SaveSettings` — `settings.go:126` | 133 | 63 | — | — |
| 18 | `application.BuildWeeklyCreditPace` — `weekly_pace.go:49` | 130 | 77 | — | 26 |
| 19 | `httpapi.(*NativeAPIHandler).chatStream` — `native_api.go:124` | 126 | 94 | 61 | — |
| 20 | `sqlite.(*Store).settleUsage` — `ledger.go:327` | 125 | 86 | 83 | — |

Наибольшая цикломатическая сложность: `NativeAPIService.directChat` (32), `AccountsService.ManualCallback` (31), `NativeAPIService.Embeddings` (29), `ModelSource.Validate` (29), `serveManagedRuntime` (27).

## 2. Тесты

### 2.1 Время (`go test -count=1 -json ./...`, параллельно шёл `-race`-прогон другого агента)

Общее время на стене — 1 м 42 с. В отдельном прогоне другого агента (`.local/audit/test.log`) — 1 м 06 с; `-race` (`race.log`) — 8 м 40 с. Тестов верхнего уровня 680, подтестов 711; падений и пропусков нет.

| Пакет | Время пакета, с | Тестов | Покрытие по пакету | Кросс-пакетно |
|---|---:|---:|---:|---:|
| internal/adapters/httpapi | 101,5 | 173 | 78,2% | 78,8% |
| internal/adapters/sqlite | 79,5 | 137 | 75,9% | 77,5% |
| internal/adapters/provider | 16,3 | 46 | 82,2% | 87,9% |
| internal/application | 15,1 | 196 | 61,5% | 83,8% |
| cmd/codex-lb | 6,8 | 21 | 61,2% | 61,2% |
| internal/adapters/upstream | 5,6 | 41 | 75,3% | 79,1% |
| updatefiles / releases / streambuffer / credentials / chatgpt / webui / updatecontrol | < 0,5 | 59 | 70–92% | 76–92% |
| internal/domain (+pricing) | 0,0 | 7 | 29,4% (81,0%) | 83,2% (90,5%) |

Сумма времени тестов внутри пакета равна времени пакета: внутри пакета всё идёт строго последовательно, `t.Parallel()` нет ни одного.

**Топ-20 самых медленных тестов:**

| с | Пакет | Тест |
|---:|---|---|
| 15,02 | provider | TestWaitingWebSocketFailurePreservesNonexecutionProof (жёсткий `time.NewTimer(15 * time.Second)` в `upstream/websocket_sessions.go:159`) |
| 10,36 | httpapi | TestSSEKeepaliveWaitsForAdmissionIncludingCompactTrigger |
| 8,40 | httpapi | Test256ConcurrentResponsesSSEStreamsSettleAndRelease |
| 7,40 | httpapi | TestNativeRefusalsPreserveReportedBilling |
| 5,16 | sqlite | TestAffinityLegacySettingsUseCanonicalPrimaryBeforeAlias |
| 5,01 | upstream | TestResponsesInterruptAcknowledgementLifecycle (жёсткий `ackTimer = 5 * time.Second`, `responses_websocket.go:197`) |
| 4,85 | httpapi | TestNamedThreadsShareCacheWithoutSharingAccountPin |
| 4,13 | sqlite | TestLegacyImportRetainsPendingDeletionWithoutCredentials |
| 3,45 | sqlite | TestVersion30DeletedAccountMigrationErasesAccess |
| 3,24 | sqlite | TestAccountAdmissionV26UpgradeInheritsEnvironment |
| 3,20 | cmd/codex-lb | TestManagedBinaryUpdateRollbackAndFailedCandidate |
| 2,89 | sqlite | TestWeeklyPaceSettingsLegacyImport |
| 2,87 | httpapi | TestManualProbePreservesWarmupAccounting |
| 2,79 | httpapi | TestResponsesRouteRetainsUnknownUsageFromLoopbackUpstream |
| 2,76 | sqlite | TestCodexClientVersionUpgradePersistenceAndValidation |
| 2,68 | httpapi | TestCompactDoesNotRetryNonQuotaUnsafeHistoryOrAccountingFailure |
| 2,64 | httpapi | TestResponsesTransportPolicyOnHTTPAndWebSocketIngress |
| 2,62 | sqlite | TestRelativeAvailabilityV23UpgradeKeepsExistingSettings |
| 2,54 | sqlite | TestWarmupModelV25UpgradeAndPersistence |
| 2,48 | sqlite | TestResetCreditSettingsV24UpgradeAndPersistence |

Источники флейков:
- `time.Sleep` в тестах встречается 23 раза. Почти везде это опрос с дедлайном в 1 секунду: 6 раз в `httpapi/proxy_websocket_frame_test.go:89,126,141,183,267,273`, 2 раза в `websocket_admission_test.go:76,121`, ещё в `sqlite/writer_test.go:122,217,231`. Под `-race` на нагруженном CI односекундный дедлайн рискован.
- Фиксированные паузы: `accounts_oauth_test.go:153` (50 мс), `upstream_test.go:343` (50 мс), `public_warmup_accounting_test.go:109` (30 мс), `cmd/codex-lb/update_integration_test.go:213,253,428`.
- `t.Setenv`/`os.Setenv` используется 34 раза в 5 файлах. Только они мешают включить `t.Parallel` массово.

### 2.2 Покрытие

`go test -coverprofile`: **70,1%** (22 011 statements). С `-coverpkg=./...`: **80,4%**. Профили — `cover.out`, `cover-all.out`; сводки — `cover-files.txt`, `cover-all-summary.txt`.

Слабые места на критичных путях (по кросс-пакетному профилю, то есть с учётом всех тестов):

| Файл | Покрытие | Что не покрыто |
|---|---:|---|
| `internal/adapters/httpapi/auth.go` | 35,3% | `passwordLogin`, `passwordChange`, `passwordRemove`, `totpStart`, `totpConfirm`, `totpVerify` — по 0% на уровне HTTP-маршрута (CSRF, cookie, ошибки). Сервисный уровень проверяет только `TestAdminAuthLifecycle` |
| `internal/adapters/httpapi/keys.go` | 51,2% | `deleteKey`, `regenerateKey` — 0% |
| `cmd/codex-lb/update_runtime.go` / `update_worker.go` | 8,4% / 20,8% | самообновление бинарника выполняется в дочернем процессе, покрытие не собирается (нужен `GOCOVERDIR`) |
| `internal/adapters/upstream/messages.go` | 51,5% | `toolCallsToChat`, `reasoningSummary`, `appendContentlessFallback` — 0% (трансляция в Chat Completions для внешних источников) |
| `internal/application/response_lite_replay.go` | 42,9% | `replayWebSearch` — 0% |
| `internal/application/accounts_flow_state.go` | 59,7% | `removeFlowLocked` и мёртвый `pruneExpiredPublic` |

По самому пакету `application` ряд файлов — `native_api.go`, `public_warmup.go`, `reports*.go`, `model_catalog_view.go`, `key_usage.go` — покрыт на 0–5%. Их проверяют только через `httpapi`, поэтому кросс-пакетно они покрыты нормально. Это не дыра, но регрессии здесь ловятся только тяжёлыми HTTP-тестами.

## 3. Архитектура

- **Слои соблюдаются.** По `go list` `internal/domain` импортирует только stdlib (`crypto/sha256`, `encoding/json`, `net/url`, `regexp`, `time` и др.). `internal/application` зависит от `domain` и `domain/pricing`, а также от `google/uuid` и `x/crypto/bcrypt`; `net/http` и `adapters` не импортирует. Все адаптеры зависят только от `application`/`domain`, кроме `provider → upstream` (адаптер поверх адаптера, это допустимо). Тесты `application` в пакете `application_test` используют `adapters/sqlite`, `chatgpt` и `credentials` — это интеграционные тесты в чужом пакете, цикла нет.
- **Размеры.**

  | Пакет | Код | Тесты | Файлов |
  |---|---:|---:|---:|
  | `application` | 16 129 | 8 748 | 74 |
  | `sqlite` | 11 411 | 7 994 | 83 |
  | `httpapi` | 5 725 | 15 605 | 36 |
  | `upstream` | 3 581 | 2 146 | 16 |

  В `application` 55 интерфейсов-портов и 193 структуры.
- **Стоит ли делить `application`.** Окупается частично. Измеренная связность:
  - `reports_*`, `key_usage*`, `weekly_pace*`, `key_report_auth` — 11 файлов, около 1,8K строк, ни одной ссылки на `Proxy`, `AccountsService` или `CodexOperations`.
  - `runtime_update*` — около 390 строк, ссылок на ядро нет.
  - `admin_auth`, `admin_totp`, `firewall` — около 540 строк, ссылок нет.
  - `model_catalog*`, `model_pricing`, `model_sources` — около 1,7K строк, ссылается только на `ProxyError`.
  - `automations`, `warmups`, `public_warmup` — около 1,7K строк, зависят от `Proxy`, `CodexOperations`, `ProxyError`.

  Предложение: вынести `application/reports`, `application/updates`, `application/admin` (около 2,7K строк, почти механически) и, возможно, `application/catalog`. Ядро прокси (`proxy`, `response_*`, `compact_*`, `capability`, `affinity`, `account_admission`, `codex_*`, `native_*`) оставить одним пакетом: методы `*Proxy` разнесены по файлам, общие неэкспортируемые хелперы при разделении пришлось бы экспортировать. Главный выигрыш — навигация и явные границы, а не скорость сборки.
- **`httpapi`.** Делить production-код (5,7K) не нужно. Проблема в 15,6K строк последовательных e2e-тестов. Выигрыш даст `t.Parallel` и общий тестовый харнесс (сейчас есть `newAccountsTestServer`, `newAutomationsTestServer`, `newNativeTestServer` и др.), а не разбиение пакета.
- **Где реально окупится.** Декомпозиция `Proxy.Respond` и трёх WebSocket-функций (QA-7): там сосредоточены инварианты владения, резервирования и settlement.

## 4. Зависимости

- `go mod tidy -diff` даёт ненулевой diff (`tidy.diff`):
  - все 7 прямых зависимостей помечены `// indirect`: `coder/websocket`, `fernet-go`, `google/uuid`, `x/crypto`, `x/sys`, `modernc.org/sqlite`, `rsc.io/qr`;
  - в `go.sum` не хватает хешей тестового графа modernc (`ccgo/v4`, `cc/v4`, `gc/v2`/`v3`, `x/tools`, `x/mod`, `x/sync`, `hashicorp/golang-lru`, `google/pprof`).
- Доступные обновления (`mod-updates.txt`):

  | Модуль | Текущая | Доступна |
  |---|---|---|
  | `modernc.org/sqlite` | 1.59.0 | 1.61.0 |
  | `modernc.org/libc` | 1.75.7 | 1.79.1 |
  | `golang.org/x/crypto` | 0.57.0 | 0.58.0 |
  | `golang.org/x/sys` | 0.48.0 | 0.49.0 |
  | `ncruces/go-strftime` | 1.0.0 | 1.1.0 |
  | `dustin/go-humanize` | 1.0.1 | 1.1.0 |

  `modernc.org/sqlite` обновляется только вместе с парной версией `libc`.
- govulncheck (`govulncheck.txt`): 11 достижимых уязвимостей stdlib Go 1.26.6 — GO-2026-6809 (x509, исправлено в 1.26.10), GO-2026-6617/6613/6612/6611/6610/6609/6605/6603 (net/http, 1.26.9), GO-2026-6608 (textproto), GO-2026-6607 (tls). Ещё 1 уязвимость в импортируемых пакетах и 3 в модулях не вызываются кодом. `bin/codex-lb`, `dist/*/codex-lb` и релиз `.local/release-go-v1.0.10-*/linux-amd64/codex-lb` собраны `go1.26.6`. Dockerfile использует плавающий `golang:1.26-alpine` и получит патч при пересборке; локальная релизная сборка его не получит.

## 5. OpenSpec

### 5.1 Валидация

Кэшированный `@fission-ai/openspec@1.12.0` из npx:
- `validate --specs`: 61/61 проходят, но выдают 442 INFO «requirement > 500 символов».
- `validate --changes`: 167 проходят, 21 падает. Все падения одного типа: MODIFIED-блок теряет сценарии, которые уже есть в основном spec. Значит, основные specs ушли вперёд, и такие changes нельзя заархивировать без ручной правки.
- `openspec` в `PATH` нет, хотя AGENTS.md требует `openspec validate --specs`.

### 5.2 openspec/changes (188 активных)

- Все созданы с 2026-07-15 по 2026-09-06 и тронуты в последний раз не позже 2026-09-06.
- Ни один не упоминает Go (`internal/`, `.go`, `go test`, `go-runtime` — 0 совпадений).
- У 170 все задачи `[x]`, у 18 есть открытые задачи; у `redesign-weekly-pace-runway` открыты все 18 из 18.
- У 122 из 139 changes с ADDED-требованиями ни одно требование не попало в основные specs: работа сделана в Python, но не синхронизирована. У 16 ADDED уже есть в specs (синхронизировано, но не заархивировано).
- В архиве 34 изменения Go-эпохи (с 2026-09-27); активных Go-изменений нет. Процесс для Go работает, а активный список — мёртвый груз.

Классификация (полная таблица — в приложении A):

| Вердикт | Кол-во | Действие |
|---|---:|---|
| **Актуально для Go** (активная работа) | **0** | — |
| **legacy** — исключённые механизмы (HTTP bridge / durable anchors / spool, Postgres/Helm/Nix, multi-replica, install telemetry, guest, quota phase planner, Images API, proxy pools) или Python/asyncio/Prometheus/CI-инфраструктура | 108 | архивировать или удалить без sync |
| **неясно (паритет)** — Python-изменение в домене, который есть в Go (API-ключи и резервирования, OAuth, firewall, chat/embeddings/transcriptions, model sources, UI в `web/`) | 80 | не синхронизировать. Свести в один Go-change `audit-legacy-parity` со списком; по каждому пункту проверить поведение Go тестом, при необходимости добавить требование в `go-runtime`, затем заархивировать |

### 5.3 openspec/specs (61 capability)

В `go-runtime/context.md` прямо сказано, что остальные capability — «legacy references unless explicitly referenced by the Go contract». При этом `go-runtime/spec.md:961` нормативно ссылается на `responses-api-compat` (5806 строк, «bridge» упоминается 368 раз) и `sticky-session-operations` (1054 строки, «bridge» — 125 раз). То есть Go-контракт формально тянет за собой спецификации Python-механизма HTTP bridge. У 22 specs в Purpose стоит «TBD - created by archiving…».

| Capability | Req | Последняя правка | Вердикт | Основание |
|---|---:|---|---|---|
| go-runtime | 61 | 2026-10-26 | **Go** | основной контракт; 32 Go-изменения. Требование «Preserve the legacy implementation during in-repository rewrite» устарело |
| runtime-updates | 8 | 2026-10-04 | **Go** | Go-only самообновление (`/api/runtime/...`, `cmd/codex-lb/update_*`) |
| account-groups | 6 | 2026-10-01 | смешанная | на неё ссылается go-runtime; правка в релизном Go-коммите 29ea3b9b |
| api-keys | 55 | 2026-10-01 | смешанная | Go-правки + legacy (replica ×21, postgres ×3, bridge ×3) |
| frontend-architecture | 139 | 2026-10-10 | смешанная | дашборд `web/` унаследован от легаси React; одно Go-изменение; guest ×8 |
| outbound-http-clients | 23 | 2026-10-01 | смешанная | python ×23 (httpx/SSLContext), одна Go-правка |
| usage-refresh-policy | 39 | 2026-10-01 | смешанная | одно Go-изменение; replica ×101 |
| account-auth-export | 1 | 2026-06-02 | неясно | в Go есть `/api/accounts/{id}/export/auth`; Purpose TBD; слить с unified-auth-export |
| account-identity | 2 | 2026-09-06 | неясно | на неё ссылается go-runtime:101 |
| account-import | 1 | 2026-08-14 | неясно | импорт есть в Go, требования продублированы в go-runtime |
| account-pool-usage-v1-usage | 2 | 2026-07-13 | неясно | в Go есть `/v1/usage` |
| account-quota-presentation | 4 | 2026-07-13 | неясно | UI |
| account-routing | 23 | 2026-08-31 | неясно | домен есть; replica ×47, bridge ×13 |
| admin-auth | 14 | 2026-08-02 | неясно | пароль/TOTP есть в Go; guest ×44 исключён |
| api-firewall | 5 | 2026-07-17 | неясно | firewall есть в Go |
| audio-transcriptions-compat | 8 | 2026-09-05 | неясно | `/v1/audio/transcriptions` есть |
| automations | 8 | 2026-07-22 | неясно | automations есть; replica ×5 |
| chat-completions-compat | 19 | 2026-08-14 | неясно | `/v1/chat/completions` есть |
| clipboard-copy-fallback | 2 | 2026-06-02 | неясно | UI |
| conversations-api | 6 | 2026-08-10 | неясно | `/api/conversations` есть; guest ×4 |
| data-retention | 5 | 2026-08-14 | неясно | в Go есть `FoldAndPruneRequestLogs`; «leader-gated» и dashboard-пресеты — legacy |
| date-display-format | 3 | 2026-08-14 | неясно | UI |
| fast-mode-policy | 1 | 2026-08-30 | неясно | колонка `prohibit_fast_mode` есть в Go |
| files-upload-protocol | 5 | 2026-08-15 | неясно | `/backend-api/files` есть; replica ×5 |
| graceful-shutdown | 5 | 2026-08-14 | неясно | текст про Python (python ×17), drain в Go есть — переписать |
| http-ingress-limits | 6 | 2026-08-14 | неясно | `MaxBytesReader`/gzip есть, zstd в Go нет |
| model-catalog-compat | 26 | 2026-08-19 | неясно | каталог есть; replica ×43, bridge ×18 |
| model-source-routing | 4 | 2026-09-05 | неясно | model sources есть |
| proxy-admission-control | 22 | 2026-09-06 | неясно | admission есть; bridge ×78, replica ×63 |
| proxy-warmup | 7 | 2026-08-16 | неясно | `/v1/warmup` есть |
| rate-limit-reset-credits | 7 | 2026-08-14 | неясно | reset credits есть; replica ×45 |
| realtime-api-compat | 5 | 2026-07-27 | неясно | Realtime есть; в go-runtime свои требования |
| responses-api-compat | 184 | 2026-09-06 | неясно | на неё нормативно ссылается go-runtime:961; bridge ×368 |
| sticky-session-operations | 19 | 2026-09-06 | неясно | на неё ссылается go-runtime:961; `/api/sticky-sessions` есть; bridge ×125 |
| unified-auth-export | 3 | 2026-07-13 | неясно | см. account-auth-export |
| usage-error-metrics | 6 | 2026-08-14 | неясно | отчёты по cancelled/error в Go |
| api-response-metadata | 2 | 2026-06-02 | legacy | заголовка `X-App-Version` в Go нет |
| audit-logging | 2 | 2026-08-14 | legacy | audit-лога в Go нет |
| bridge-ring-membership | 4 | 2026-07-16 | legacy | multi-replica исключено |
| compatibility-tooling | 24 | 2026-08-31 | legacy | Python-инструменты wire-паритета удалены |
| contribution-simplicity | 5 | 2026-07-15 | legacy | `PRINCIPLES.md` и budgets отсутствуют |
| database-backends | 13 | 2026-08-31 | legacy | Postgres/Helm (postgres ×40) |
| database-migrations | 19 | 2026-08-14 | legacy | Alembic (python ×28). Версионные миграции SQLite в Go (v22…v30) собственного spec не имеют — пробел |
| deployment-installation | 19 | 2026-09-06 | legacy | Helm ×64, Postgres ×48, Rust ×11 |
| deployment-networking | 8 | 2026-08-14 | legacy | network policy Helm-чарта |
| fleet-summary | 10 | 2026-08-14 | legacy | endpoint-а в Go нет |
| github-automation | 12 | 2026-08-14 | legacy | `.github/` нет |
| images-api-compat | 8 | 2026-08-20 | legacy | исключено в go-runtime |
| live-usage-ingestion | 4 | 2026-07-14 | legacy | пассивного разбора `x-codex-*` в Go нет, usage опрашивается |
| proxy-architecture | 4 | 2026-08-31 | legacy | фасад Python `ProxyService` |
| proxy-runtime-observability | 36 | 2026-08-31 | legacy | Prometheus/bridge; `/metrics` в Go нет |
| query-caching | 35 | 2026-09-04 | legacy | кэши Python/Postgres |
| quota-phase-planner | 11 | 2026-08-30 | legacy | исключено |
| release-automation | 3 | 2026-07-13 | legacy | release-please/GitHub |
| release-management | 7 | 2026-09-06 | legacy | beta-канал, Helm |
| replica-operations | 8 | 2026-08-15 | legacy | исключено |
| runtime-portability | 3 | 2026-08-14 | legacy | монитор памяти Python, retag CLI |
| scheduler-coordination | 6 | 2026-07-16 | legacy | leader election (multi-replica) |
| telemetry | 12 | 2026-08-12 | legacy | исключено |
| upstream-proxy-routing | 8 | 2026-08-31 | legacy | proxy pools исключены |
| user-documentation | 5 | 2026-08-27 | legacy | mkdocs и `docs/` отсутствуют |

Итого: Go — 2, смешанные — 5, неясно — 29, legacy — 25.

## 6. Процесс и противоречия

| Ссылка или артефакт | Реальность |
|---|---|
| AGENTS.md → `.github/CONTRIBUTING.md`, `PRINCIPLES.md`, `.github/simplicity-budgets.toml`, лейбл `simplicity-budget-approved`, CodeRabbit, `mergeable=CLEAN`, «CI green» | `.github/` нет, CI нет; `PRINCIPLES.md` нет |
| AGENTS.md → `docs/` (mkdocs), «не править `CHANGELOG.md`», `.env.example` budgets | `docs/`, `CHANGELOG.md`, `.env.example` отсутствуют; `openspec/config.yaml` тоже обещает «docs/ is a rendered mkdocs site» |
| AGENTS.md → `uv run pytest`/`ruff`, AsyncSession, Alembic graph | Python удалён; оговорка «historical upstream guidance» не отменяет того, что больше половины AGENTS.md неприменима |
| AGENTS.md → `/project-conventions` «auto-enforced (PreToolUse guard)» | `.agents/settings.json` вызывает `$CLAUDE_PROJECT_DIR/.claude/hooks/*.py`, но каталога `.claude/` нет; хуки — Python-скрипты в `.agents/hooks/`. Guard не работает |
| `.agents/skills/project-conventions/conventions.md` (170 строк) | в начале — Go-раздел, дальше — FastAPI/Pydantic/SQLAlchemy, ссылки на удалённый `legacy/` |
| `.agents/environments/environment.toml` | `uv sync; cd frontend && bun install` — каталога `frontend/` и uv нет |
| `.agents/scheduled_tasks.lock` | отслеживается git, содержит `sessionId`/`pid`; должен быть в `.gitignore` |
| `.agents/handover/http-bridge-keepalive-hang.md` | handover по Python HTTP bridge |
| `scripts/compare-runtime.py` | **в постановке задачи сказано, что он в `.gitignore`, — это не так.** Файл отслеживается (`git ls-files`; последние коммиты e679be2b, 29ea3b9b), в `.gitignore` его нет (там только `__pycache__/`). Это Python-стенд для бенчмарков, на него ссылаются `go-runtime/context.md:501,725` и 4 архивных Go-change. Противоречие в другом: README заявляет «No Python … is required», а единственный стенд производительности написан на Python и не упомянут ни в README, ни в Makefile |
| `specs/spec-lifecycle/workflow.{md,json}` (корень) | конфигурация workflow skill-а `spec-first-planning-loop` (layered-spec 0.2.2). Вторая система планирования рядом с OpenSpec; связь между ними нигде не описана |
| `openspec validate` | CLI не установлен, в Makefile цели нет |
| `go-runtime/context.md` (764 строки) | вперемешку с контекстом — журнал инцидентов и ручных проверок («An All-account key was checked again on 2026-10-01: … 11 input and 5 output tokens») |
| В рабочем дереве `.ruff_cache/`, `scripts/__pycache__/` | игнорируются, но это остатки Python-окружения |

Как привести процесс в соответствие реальности (предложение):
1. Переписать AGENTS.md под Go, примерно в 60–80 строк. Оставить: слои, правила Go, команды проверки (`make check`), секреты, OpenSpec-цикл, правила PR. Удалить Python/Alembic/CodeRabbit/budgets/docs. Ворота формулировать через то, что реально существует (`make check` и CI-workflow после его добавления).
2. Добавить `make check`: `go vet`, `go test`, `go test -race`, `golangci-lint` с конфигом, `govulncheck`, `openspec validate --specs` через закреплённый `npx @fission-ai/openspec@1.12.0` с `OPENSPEC_TELEMETRY=0`. Затем — минимальный GitHub Actions workflow, повторяющий `make check`. Публикация workflow требует отдельного разрешения.
3. OpenSpec:
   - перенести 188 активных changes в `openspec/changes/archive/2026-10-28-legacy-unsynced/` (или удалить — история в git) без sync;
   - legacy-specs вынести в `openspec/legacy-specs/` или пометить баннером;
   - в `go-runtime` заменить нормативную ссылку на `responses-api-compat`/`sticky-session-operations` на конкретные Go-требования;
   - со временем выделить из монолита `go-runtime` Go-capability: `go-proxy-responses`, `go-accounts`, `go-api-keys`, `go-reports`, `go-admin-auth`, `go-storage`/миграции, `go-runtime-updates`.
4. `.agents/`: удалить Python-хуки и `settings.json` (или переписать guard на shell/Go); вычистить `conventions.md` до Go; удалить `environment.toml`, handover про bridge и lock-файл; `scheduled_tasks.lock` добавить в `.gitignore`.
5. `scripts/compare-runtime.py`: либо перенести на Go (`go test -bench` с loopback-стабом в `internal/...` или `cmd/benchcompare`), либо явно задокументировать как опциональный dev-инструмент, требующий `python3`.
6. Определиться, что является SSOT: `specs/spec-lifecycle` (глобальный CLAUDE.md требует `spec-first-planning-loop`) или OpenSpec. Минимум — описать в AGENTS.md, где лежат артефакты каждого.

## 7. Таблица находок

| ID | Пр. | Категория | Доказательство | Исправление | Трудоёмк. | Риск |
|---|---|---|---|---|---|---|
| QA-1 | P1 | deps | govulncheck: 11 достижимых уязвимостей stdlib (GO-2026-6809, -6617, -6613, -6612, -6611, -6610, -6609, -6608, -6607, -6605, -6603); `go version bin/codex-lb` и релиз v1.0.10 → go1.26.6; в `go.mod` нет `toolchain` | `toolchain go1.26.10` (или `go 1.26.10`) в `go.mod`, в Dockerfile закрепить `golang:1.26.10-alpine`; пересобрать релиз; govulncheck в `make check` | S | низкий |
| QA-2 | P1 | process | `.github/` нет — тесты, `-race`, vet, линт и govulncheck никто не запускает автоматически; AGENTS.md ссылается на CI-ворота | `make check` + workflow GitHub Actions (нужно разрешение на публикацию) | M | низкий |
| QA-3 | P1 | docs/process | AGENTS.md ссылается на 9 несуществующих артефактов (§6), больше половины текста — Python-правила | переписать AGENTS.md под Go (§6, п. 1) | S | низкий |
| QA-4 | P1 | process | 188 активных changes (07-15…09-06), 0 упоминаний Go, 122 не синхронизированы, 21 не проходит `openspec validate --changes` | массово заархивировать без sync (приложение A); 80 «неясных» свести в один Go-change проверки паритета | M | низкий: история в git |
| QA-5 | P1 | docs | Из 61 spec к Go относятся 2; `go-runtime/spec.md:961` нормативно тянет legacy-specs про HTTP bridge (`responses-api-compat`: 184 req, «bridge» ×368); `go-runtime` — монолит на 1269 строк и 61 req, 442 INFO «>500 символов» | баннер или перенос legacy-specs; заменить ссылку на конкретные требования; поэтапно разбить `go-runtime` | L | средний: можно потерять неявные требования |
| QA-6 | P1 | test | Кросс-пакетное покрытие `httpapi/auth.go` 35%: `passwordLogin`, `passwordChange`, `passwordRemove`, `totpStart`, `totpConfirm`, `totpVerify` — 0%; `keys.go` `deleteKey`/`regenerateKey` — 0% | route-тесты: успех/ошибка, CSRF, cookie сессии, неверный TOTP, удаление и перевыпуск ключа | M | низкий |
| QA-7 | P1 | architecture | `Proxy.Respond`: 373 строки / 284 stmt / gocognit 270 (`application/proxy.go:126`); `websocketSessions.acquire` 132, `accountCandidates` 110 (nestif 19), `ProxyHandler.websocket` 108, `streamResponsesWebSocket` 96 | выделить фазы Respond (auth → capability → selection → reservation → dispatch/failover → settlement) в методы с типизированным состоянием запроса; делать постепенно под существующими тестами, один PR на фазу | L | **высокий**: денежные инварианты и ownership |
| QA-8 | P2 | test | `t.Parallel()` нигде нет; `httpapi` 101 с, `sqlite` 79 с последовательно; 15-секундный тест на зашитом `time.NewTimer(15 * time.Second)` (`upstream/websocket_sessions.go:159`); 5-секундный — на `ackTimer` (`responses_websocket.go:197`) | `t.Parallel()` во всех тестах без `Setenv` (34 вызова в 5 файлах), начать с миграционных тестов sqlite; таймауты вынести в `Config` или использовать `testing/synctest` | M | средний: может вскрыть скрытое общее состояние |
| QA-9 | P2 | test | 23 вызова `time.Sleep`, опрос с дедлайном 1 с (`proxy_websocket_frame_test.go` ×6 и др.), фиксированные паузы 25–50 мс | каналы-сигналы, `synctest`, дедлайн от `t.Context()` и дольше | S–M | низкий |
| QA-10 | P2 | lint | Конфига golangci нет; сырой запуск даёт 1710 замечаний, полезный сигнал тонет в шуме | добавить `.golangci.yml` (ниже) и исправить около 20 реальных замечаний | S | низкий |
| QA-11 | P2 | bug | `internal/adapters/provider/operations.go:326-330`: `contentType` вычисляется из `request.ContentType` и нигде не используется (ineffassign); `form.CreateFormFile` всегда ставит `application/octet-stream`, MIME-тип аудио от клиента теряется | `form.CreatePart` с `Content-Disposition` + `Content-Type: contentType`; тест на multipart-тело | S | низкий |
| QA-12 | P2 | lint | Мёртвый код: 5 недостижимых функций, 5 используются только в тестах (§1.2) | удалить; тестовые хелперы перенести в `_test.go` | S | низкий |
| QA-13 | P2 | deps | В `go.mod` все модули помечены `// indirect`, `go mod tidy -diff` ≠ 0, в `go.sum` не хватает тестового графа modernc; доступны sqlite 1.61.0 / libc 1.79.1, x/crypto 0.58.0, x/sys 0.49.0 | закоммитить результат `go mod tidy`; обновление modernc — отдельным PR с полным `-race` и миграционными тестами | S (tidy) / M (sqlite) | низкий / средний |
| QA-14 | P2 | process | Python-хуки в `.agents/hooks/*.py`, `settings.json` указывает на несуществующий `.claude/hooks`; `conventions.md` — в основном FastAPI; `environment.toml` — uv/frontend; в git лежит `scheduled_tasks.lock` с PID | вычистить `.agents/` (§6, п. 4) | S | низкий |
| QA-15 | P2 | process | `scripts/compare-runtime.py` — отслеживаемый Python-стенд (не в `.gitignore`, вопреки постановке), на который опирается `go-runtime/context.md`; README обещает «No Python» | перенести на Go-бенчмарк или явно задокументировать как опциональный dev-инструмент | M | низкий |
| QA-16 | P2 | process | Две системы планирования: `openspec/` и `specs/spec-lifecycle/` (layered-spec 0.2.2), их связь не описана | выбрать SSOT и описать в AGENTS.md | S | низкий |
| QA-17 | P2 | process | `openspec` CLI отсутствует в PATH и Makefile; работает только из кэша npx 1.12.0 | цель `make spec-validate` с закреплённой версией и `OPENSPEC_TELEMETRY=0` | S | низкий |
| QA-18 | P2 | test | `cmd/codex-lb/update_runtime.go` 8,4%, `update_worker.go` 20,8%: самообновление идёт в дочернем процессе, покрытие теряется; `upstream/messages.go` 51,5% (`toolCallsToChat`, `reasoningSummary` — 0%) | собирать дочерние бинарники с `-cover` и `GOCOVERDIR`; unit-тесты трансляции tool calls | M | низкий |
| QA-19 | P3 | lint | gofumpt: 168/533 файлов; 251 строка > 160 колонок (максимум 466); плотная упаковка нескольких полей в строку | один механический коммит gofumpt (и, при желании, golines -m 140) отдельно от логики | S | низкий: только diff-шум |
| QA-20 | P3 | lint | errorlint: `err == errSeatMismatch` (`accounts_oauth.go:311,347`), `err == domain.ErrNotFound` (`accounts_service.go:219`), `probeErr == errWarmupIneligible` (`warmups_probe.go:57,68`), непроверенное `err.(*Error)` (`upstream/adapter.go:261`) | `errors.Is`/`errors.As`; `rejectedResult` должна возвращать `*Error` | S | низкий |
| QA-21 | P3 | lint | `cmd/codex-lb/runtime.go:120`: `api.ConfigureAccounts(accounts)` — ошибка отброшена; `main.go:27`: `os.Exit` после `defer stop()`; `unparam` у `updateHost.Shutdown` и `AutomationsService.runViews` | проверять ошибку; убрать лишние возвращаемые значения | S | низкий |
| QA-22 | P3 | lint | revive: `redefines-builtin-id` (`cap` в `upstream/*`, `close` в `capability.go:302`, `copy` в `runtime_updates.go:165`), var-naming `ProxyApiKey…` (`domain/models.go:242-244`), `noctx` в prod (`sqlite/store.go:81`, `legacy_import.go:213`) | переименовать; `QueryRowContext`/`ExecContext` | S | низкий |
| QA-23 | P3 | architecture | `application`: 16K строк, 74 файла, 55 интерфейсов в одном пакете; reports/updates/admin (около 2,7K строк) не ссылаются на ядро прокси | вынести `application/reports`, `application/updates`, `application/admin` (и, возможно, `catalog`); ядро прокси не трогать | M | низкий–средний |
| QA-24 | P3 | docs | 22 spec с Purpose «TBD»; в `go-runtime/context.md` журнал ручных проверок и инцидентов; устаревшее требование «Preserve the legacy implementation during in-repository rewrite» | переписывать вместе с QA-5; журнал перенести в `notes.md` архивных changes | M | низкий |
| QA-25 | P3 | docs | Версионные миграции SQLite в Go (v22…v30, `sqlite/migrations.go`) не имеют собственной Go-спецификации; `database-migrations` описывает Alembic | добавить capability `go-storage`: версии схемы, проверка владельца и ключа, правила апгрейда и отказа | M | низкий |

Черновик `.golangci.yml` (в репозиторий не добавлялся):

```yaml
version: "2"
linters:
  default: none
  enable: [govet, staticcheck, unused, errcheck, ineffassign, errorlint, bodyclose, noctx, gocritic, revive, unparam, gocognit]
  settings:
    errcheck:
      exclude-functions:
        - (io.Closer).Close
        - (*database/sql.Rows).Close
        - (*database/sql.Tx).Rollback
        - (*github.com/coder/websocket.Conn).CloseNow
    revive:
      rules:
        - {name: exported, disabled: true}
        - {name: unused-parameter, disabled: true}
    gocognit: {min-complexity: 60}
  exclusions:
    rules:
      - {path: _test\.go, linters: [errcheck, noctx, bodyclose, gocognit]}
formatters:
  enable: [gofumpt]
```

## 8. Быстрые победы (каждая — S, без изменения поведения, кроме QA-11)

1. **QA-1.** Строка `toolchain go1.26.10` и пересборка закрывают 11 CVE stdlib.
2. **QA-13.** `go mod tidy` и коммит `go.mod`/`go.sum`, после чего прямые зависимости перестают быть `// indirect`.
3. **QA-10/12.** `.golangci.yml` из черновика выше. Удалить 5 мёртвых функций и перенести 5 тестовых хелперов в `_test.go`.
4. **QA-11.** Вернуть MIME-тип аудио-части (примерно 5 строк и тест).
5. **QA-4.** Одним коммитом перенести 188 legacy-changes в архив.
6. **QA-14.** Удалить `.agents/scheduled_tasks.lock`, добавить его в `.gitignore`, убрать нерабочие Python-хуки, `environment.toml` и handover про bridge.
7. **QA-3.** Переписать AGENTS.md под Go.
8. **QA-17.** Цель `make spec-validate`; плюс `make check`, объединяющая vet, test, race, lint, govulncheck и spec-validate.
9. **QA-8 (первый шаг).** `t.Parallel()` в миграционных тестах sqlite (каждый 2–5 с); 15-секундный таймер `websocket_sessions.go:159` вынести в поле конфигурации, чтобы тест не ждал 15 с.
10. **QA-20/21.** `errors.Is` вместо `==` в 5 местах, проверка ошибки `ConfigureAccounts`.

## Артефакты (`.local/audit/scratch-quality/`)

| Файл | Содержимое |
|---|---|
| `golangci.{txt,json,log}` | полный вывод lint |
| `golangci-prod-nonerrcheck.txt` | production-замечания без errcheck |
| `complexity.json`, `complexity-top.txt`, `funcstat.txt` | сложность и длина функций |
| `gofumpt-tracked.txt`, `gofumpt.diff` | вывод gofumpt |
| `deadcode.txt`, `deadcode-test.txt` | мёртвый код |
| `test.json`, `test.time` | прогон тестов |
| `cover.out`, `cover-all.out`, `cover-func.txt`, `cover-files.txt`, `cover-all-summary.txt` | покрытие |
| `tidy.diff`, `mod-updates.txt`, `govulncheck.txt` | зависимости |
| `openspec-validate-{specs,changes}.txt` | валидация OpenSpec |
| `changes.json`, `changes-synced.json`, `changes-table.md`, `specs.json` | классификация changes и specs |
| `routes.txt`, `routes-mux.txt` | маршруты Go |

Замечание о соседних аудиторах. Во время работы в дереве появились неотслеживаемые `internal/adapters/httpapi/zz_audit_e2e_bench_test.go` (00:47:43) и `internal/adapters/sqlite/zz_audit_tmp_test.go` (00:48:20). Их создал не этот аудит. В мой `test.json` они не попали, тестов с такими именами там нет.

## Приложение A. Классификация 188 активных openspec/changes

Колонки: change | вердикт | создан | tasks выполнено/всего | ADDED уже в основных specs («да» — все, «нет» — ни одного, «—» — ADDED в change нет) | основание. Вердикт выставлен по правилам: имя, затронутые capability и частота ключевых слов bridge/postgres/helm/replica/telemetry/guest; «неясно» требует ручной сверки с Go. Ни один change не помечен как «актуален для Go».

| Change | Вердикт | Создан | Tasks | ADDED в specs | Основание |
|---|---|---|---|---|---|
| abandon-expired-http-bridge-operations | legacy | 2026-09-04 | 22/22 | нет | HTTP bridge / durable-anchor механика Python |
| add-chunked-http-bridge-transcripts | legacy | 2026-08-28 | 11/11 | нет | HTTP bridge / durable-anchor механика Python |
| add-daybreak-blue-client-profile | legacy | 2026-08-14 | 15/15 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| add-request-log-usage-rollups | legacy | 2026-07-25 | 32/34 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| add-root-file-allowlist-budget | legacy | 2026-08-20 | 6/6 | нет | Python/Prometheus/CI-инфраструктура |
| add-telemetry-optout-signal | legacy | 2026-08-20 | 11/11 | нет | install-telemetry (исключена) |
| add-timeout-invariant-linter | legacy | 2026-08-20 | 11/11 | нет | Python/Prometheus/CI-инфраструктура |
| attribute-websocket-scope-cleanup-phase | legacy | 2026-08-16 | 4/4 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| background-account-deletion | legacy | 2026-08-17 | 25/25 | нет | дельты только в легаси-capability: account-deletion |
| bound-account-summary-live-tail | legacy | 2026-08-17 | 11/11 | — | Python SQLAlchemy/кэш/миграции Alembic |
| bound-durable-bridge-spool-cleanup | legacy | 2026-08-29 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| bound-eventless-server-recovery | legacy | 2026-08-30 | 4/4 | нет | HTTP bridge / durable-anchor механика Python |
| bound-request-metric-label-cardinality | legacy | 2026-09-05 | 9/9 | нет | Python/Prometheus/CI-инфраструктура |
| bound-sqlite-wedged-teardown | legacy | 2026-08-17 | 9/9 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| bound-stalled-upstream-streams | legacy | 2026-08-06 | 8/9 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| bound-zstd-decoded-memory | legacy | 2026-08-31 | 7/7 | нет | Python/Prometheus/CI-инфраструктура |
| cap-projection-history-rows | legacy | 2026-08-17 | 11/11 | — | Python SQLAlchemy/кэш/миграции Alembic |
| classify-bridge-recovery-error-frames | legacy | 2026-08-20 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| classify-required-http-bridge-owner-unavailable | legacy | 2026-07-22 | 22/22 | нет | HTTP bridge / durable-anchor механика Python |
| clean-recovery-sqlite-sidecars | legacy | 2026-08-30 | 14/14 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| consolidate-defer-cancellation-waits | legacy | 2026-08-31 | 8/8 | — | опирается на исключённые механизмы: bridge,postgres |
| credential-safe-proxy-logging | legacy | 2026-09-04 | 8/8 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| credential-safe-proxy-logging-loop-handler | legacy | 2026-09-04 | 8/8 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| dashboard-first-run-empty-states | legacy | 2026-08-14 | 11/11 | нет | опирается на исключённые механизмы: guest |
| dedupe-http-bridge-retry-circuit-attempts | legacy | 2026-08-16 | 17/17 | нет | HTTP bridge / durable-anchor механика Python |
| defer-bridge-precreated-health-until-settlement | legacy | 2026-08-27 | 6/6 | — | HTTP bridge / durable-anchor механика Python |
| defer-live-lease-release-cancellation | legacy | 2026-09-02 | 7/7 | нет | HTTP bridge / durable-anchor механика Python |
| disable-helm-global-backpressure-default | legacy | 2026-08-31 | 6/6 | нет | Helm/Postgres доставка (исключена) |
| disable-upstream-websocket-compression | legacy | 2026-08-17 | 3/3 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| enable-chunked-http-bridge-writer | legacy | 2026-08-28 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| expand-postgres-shm-and-pool-headroom | legacy | 2026-08-17 | 7/7 | нет | Helm/Postgres доставка (исключена) |
| expand-ttft-output-events | legacy | 2026-07-18 | 5/5 | — | дельты только в легаси-capability: proxy-runtime-observability |
| fail-closed-draining-live-lease-claim | legacy | 2026-08-14 | 10/10 | да | HTTP bridge / durable-anchor механика Python |
| fail-closed-http-bridge-security-retry | legacy | 2026-07-24 | 7/7 | да | HTTP bridge / durable-anchor механика Python |
| failover-stuck-http-bridge-owner | legacy | 2026-08-10 | 11/11 | — | HTTP bridge / durable-anchor механика Python |
| fence-successor-bridge-claims | legacy | 2026-08-15 | 20/20 | нет | HTTP bridge / durable-anchor механика Python |
| fix-compact-previous-response-quota-failover | legacy | 2026-08-17 | 6/6 | нет | опирается на исключённые механизмы: bridge |
| fix-cross-account-previous-response-anchor | legacy | 2026-08-10 | 9/11 | нет | HTTP bridge / durable-anchor механика Python |
| fix-disconnect-cleanup-leak | legacy | 2026-08-16 | 5/5 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| fix-images-cancel-reservation | legacy | 2026-08-31 | 11/11 | — | опирается на исключённые механизмы: images_api |
| fix-inflight-future-abandoned | legacy | 2026-08-16 | 5/5 | — | Python runtime-механика (asyncio/httpx/логирование) |
| fix-latency-promql-aggregation | legacy | 2026-09-02 | 8/8 | нет | Python/Prometheus/CI-инфраструктура |
| fix-native-http-compression-relay | legacy | 2026-09-05 | 9/9 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| fix-proxy-usage-refresh-owned-sessions | legacy | 2026-08-26 | 9/9 | нет | дельты только в легаси-capability: database-backends |
| fix-refresh-singleflight-successor-guard | legacy | 2026-08-16 | 3/3 | нет | HTTP bridge / durable-anchor механика Python |
| fix-retire-stale-preawait-liveness | legacy | 2026-08-30 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| fix-shutdown-grace-and-nested-budget | legacy | 2026-08-30 | 5/5 | нет | дельты только в легаси-capability: graceful-shutdown |
| fix-transcribe-cancel-reservation-release | legacy | 2026-09-02 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| fix-websocket-response-create-lease-cancellation | legacy | 2026-08-16 | 4/4 | нет | HTTP bridge / durable-anchor механика Python |
| forward-canonical-prompt-cache-continuity | legacy | 2026-09-05 | 8/8 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| goal-restart-thread-header-abandonment | legacy | 2026-08-16 | 7/7 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| harden-compact-tool-tail-anchors | legacy | 2026-08-11 | 6/6 | — | HTTP bridge / durable-anchor механика Python |
| harden-websocket-stale-anchor-error-shapes | legacy | 2026-08-31 | 12/13 | нет | HTTP bridge / durable-anchor механика Python |
| hold-operation-fenced-hard-turn-cooldown | legacy | 2026-08-16 | 4/4 | нет | HTTP bridge / durable-anchor механика Python |
| honor-external-database-network-policy-port | legacy | 2026-08-26 | 16/16 | нет | Helm/Postgres доставка (исключена) |
| ignore-detached-durable-bridge-owner | legacy | 2026-09-05 | 6/6 | нет | HTTP bridge / durable-anchor механика Python |
| invalidate-denied-bridge-anchor | legacy | 2026-08-30 | 29/29 | нет | HTTP bridge / durable-anchor механика Python |
| invalidate-durable-bridge-anchor-after-stuck-timeout | legacy | 2026-08-06 | 16/17 | нет | HTTP bridge / durable-anchor механика Python |
| keep-aborted-invalidation-bumps-pending | legacy | 2026-08-14 | 5/5 | — | Python SQLAlchemy/кэш/миграции Alembic |
| keep-abrupt-eventless-drop-account-neutral | legacy | 2026-08-17 | 13/13 | нет | HTTP bridge / durable-anchor механика Python |
| keep-file-pin-owner-on-soft-1011-reconnect | legacy | 2026-08-15 | 9/9 | да | HTTP bridge / durable-anchor механика Python |
| line-scope-log-secret-redaction | legacy | 2026-09-05 | 10/10 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| name-bridge-eventless-timeout | legacy | 2026-08-30 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| normalize-responses-lite-reasoning-context | legacy | 2026-07-22 | 13/14 | нет | опирается на исключённые механизмы: bridge,telemetry |
| optimize-dashboard-hot-path-indexes | legacy | 2026-07-17 | 9/9 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| perf-basehttp-middleware-to-asgi | legacy | 2026-09-04 | 10/10 | нет | Python/Prometheus/CI-инфраструктура |
| perf-bridge-installation-id-stamp | legacy | 2026-09-04 | 6/7 | — | HTTP bridge / durable-anchor механика Python |
| persist-file-account-pins | legacy | 2026-08-15 | 11/11 | да | опирается на исключённые механизмы: bridge,postgres |
| pin-same-owner-stale-anchor-replay | legacy | 2026-08-30 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| preserve-fresh-bridge-full-resends | legacy | 2026-07-27 | 11/12 | нет | HTTP bridge / durable-anchor механика Python |
| preserve-http-bridge-recovery-operation-spool | legacy | 2026-08-31 | 8/8 | нет | HTTP bridge / durable-anchor механика Python |
| preserve-warmup-usage-on-cancellation | legacy | 2026-09-02 | 7/7 | нет | quota phase planner (исключён) |
| prevent-level-cancellation-busy-spin | legacy | 2026-09-04 | 10/10 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| recover-cache-invalidation-after-prime-failure | legacy | 2026-08-04 | 7/7 | — | Python SQLAlchemy/кэш/миграции Alembic |
| recover-codex-desktop-idle-bridge | legacy | 2026-07-22 | 10/10 | — | HTTP bridge / durable-anchor механика Python |
| recover-codex-ws-stale-anchor-with-canonical-code | legacy | 2026-08-04 | 63/63 | — | HTTP bridge / durable-anchor механика Python |
| recover-expired-quota-warmup-claims | legacy | 2026-08-30 | 7/7 | да | quota phase planner (исключён) |
| recover-fresh-hard-bridge-timeouts | legacy | 2026-08-04 | 7/9 | нет | HTTP bridge / durable-anchor механика Python |
| recover-poisoned-bridge-anchor-at-half-open | legacy | 2026-08-30 | 215/215 | — | HTTP bridge / durable-anchor механика Python |
| reject-illegal-bridge-forward-headers | legacy | 2026-09-05 | 5/5 | нет | HTTP bridge / durable-anchor механика Python |
| reject-invalid-operator-config | legacy | 2026-09-02 | 8/8 | нет | Helm/Postgres доставка (исключена) |
| relay-bridge-events-as-upstream-json-text | legacy | 2026-09-04 | 12/12 | нет | HTTP bridge / durable-anchor механика Python |
| release-keepalive-timer-on-connection-loss | legacy | 2026-09-04 | 11/11 | нет | HTTP bridge / durable-anchor механика Python |
| release-routed-upstream-stream-responses | legacy | 2026-09-04 | 10/10 | нет | HTTP bridge / durable-anchor механика Python |
| remove-codex-review-label-gate | legacy | 2026-08-16 | 7/7 | — | Python/Prometheus/CI-инфраструктура |
| report-sqlite-long-write-holders | legacy | 2026-08-15 | 7/7 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| report-suppressed-duplicate-tool-call-terminal | legacy | 2026-08-20 | 5/5 | нет | опирается на исключённые механизмы: bridge |
| require-upstream-quota-for-owner-failover | legacy | 2026-09-02 | 9/10 | да | опирается на исключённые механизмы: bridge |
| restore-settings-dialog-focus | legacy | 2026-08-26 | 9/9 | нет | опирается на исключённые механизмы: telemetry |
| restrict-guest-audit-log-access | legacy | 2026-09-04 | 7/7 | нет | guest-доступ (исключён) |
| restrict-guest-sensitive-request-data | legacy | 2026-07-30 | 10/10 | нет | guest-доступ (исключён) |
| retire-cooldown-suppressed-http-bridge-session | legacy | 2026-09-04 | 15/15 | — | HTTP bridge / durable-anchor механика Python |
| retire-precreated-gates-after-telemetry | legacy | 2026-07-15 | 8/8 | — | install-telemetry (исключена) |
| retire-rejected-proxy-anchor | legacy | 2026-09-02 | 5/6 | да | HTTP bridge / durable-anchor механика Python |
| retry-detached-api-key-release | legacy | 2026-08-06 | 9/9 | — | HTTP bridge / durable-anchor механика Python |
| scope-codex-affinity-by-thread | legacy | 2026-08-15 | 10/10 | нет | опирается на исключённые механизмы: bridge |
| serve-h2c-upgrade-offers-as-http11 | legacy | 2026-08-17 | 8/8 | нет | Python/Prometheus/CI-инфраструктура |
| settle-live-ingest-task-failures | legacy | 2026-08-17 | 9/9 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| settle-live-usage-after-account-consolidation | legacy | 2026-08-16 | 21/21 | нет | опирается на исключённые механизмы: postgres |
| settle-terminal-spool-append-failure | legacy | 2026-08-16 | 7/7 | нет | HTTP bridge / durable-anchor механика Python |
| skip-clean-shutdown-sqlite-startup-check | legacy | 2026-08-31 | 34/34 | нет | Python SQLAlchemy/кэш/миграции Alembic |
| stabilize-codex-forbidden-concurrency | legacy | 2026-08-31 | 6/6 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| surface-continuity-owner-conflict-on-http-streams | legacy | 2026-08-28 | 10/10 | нет | опирается на исключённые механизмы: postgres,telemetry |
| surface-upstream-route-metadata | legacy | 2026-08-16 | 9/9 | — | дельты только в легаси-capability: upstream-proxy-routing |
| sweep-idle-bridge-sessions-off-request-path | legacy | 2026-08-14 | 7/7 | нет | HTTP bridge / durable-anchor механика Python |
| sync-needs-rebase-label | legacy | 2026-07-24 | 6/6 | да | Python/Prometheus/CI-инфраструктура |
| type-required-reconnect-owner-provenance | legacy | 2026-09-02 | 11/11 | нет | опирается на исключённые механизмы: bridge |
| websocket-scope-cleanup-budget | legacy | 2026-08-16 | 9/9 | нет | Python runtime-механика (asyncio/httpx/логирование) |
| adapt-subscription-prompt-cache-controls | неясно (паритет) | 2026-08-04 | 6/7 | нет | домен есть в Go (responses-api-compat) |
| add-api-key-allowed-reasoning-efforts | неясно (паритет) | 2026-08-18 | 10/10 | нет | домен есть в Go (api-keys, chat-completions-compat, responses-api-compat) |
| add-model-source-embeddings | неясно (паритет) | 2026-08-20 | 12/12 | да | домен есть в Go (model-source-routing) |
| add-reset-credit-display-settings | неясно (паритет) | 2026-07-17 | 13/13 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| align-server-authoritative-dashboard-windows | неясно (паритет) | 2026-08-02 | 19/20 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| allow-compact-inline-image-elision | неясно (паритет) | 2026-08-04 | 7/7 | — | домен есть в Go (responses-api-compat) |
| apply-context-window-override-to-v1-input-budget | неясно (паритет) | 2026-08-20 | 8/8 | — | домен есть в Go (model-catalog-compat) |
| bind-api-firewall-to-raw-peer | неясно (паритет) | 2026-08-31 | 9/9 | — | домен есть в Go (api-firewall) |
| clarify-routing-quota-help-copy | неясно (паритет) | 2026-08-17 | 9/9 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| classify-background-json-completion | неясно (паритет) | 2026-09-05 | 6/6 | — | домен есть в Go (responses-api-compat) |
| clear-dashboard-capacity-overrides | неясно (паритет) | 2026-08-26 | 13/13 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| close-nonstream-chat-collect | неясно (паритет) | 2026-08-14 | 6/6 | да | домен есть в Go (chat-completions-compat) |
| coalesce-sticky-same-owner-refresh | неясно (паритет) | 2026-08-17 | 9/9 | нет | домен есть в Go (sticky-session-operations) |
| configure-model-source-reasoning-efforts | неясно (паритет) | 2026-08-20 | 5/5 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| confirm-workspaceless-plan-downgrade | неясно (паритет) | 2026-08-05 | 18/18 | нет | домен есть в Go (usage-refresh-policy) |
| conversations-qualify-by-any-request-in-window | неясно (паритет) | 2026-08-02 | 18/20 | да | домен есть в Go (conversations-api) |
| customize-dashboard-request-log-columns | неясно (паритет) | 2026-08-16 | 13/13 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| document-compact-trigger-proxy-contract | неясно (паритет) | 2026-08-18 | 5/5 | — | домен есть в Go (responses-api-compat) |
| enable-auth-guardian-default-and-paused | неясно (паритет) | 2026-08-07 | 5/6 | — | домен есть в Go (usage-refresh-policy) |
| fail-over-compact-permanent-refresh | неясно (паритет) | 2026-09-05 | 8/8 | — | домен есть в Go (responses-api-compat) |
| fall-back-to-http-on-websocket-connect-failure | неясно (паритет) | 2026-08-27 | 15/15 | нет | домен есть в Go (responses-api-compat) |
| fence-stale-oauth-polls | неясно (паритет) | 2026-08-31 | 11/11 | нет | домен есть в Go (account-identity) |
| file-pin-does-not-rewrite-thread-locality | неясно (паритет) | 2026-08-16 | 6/6 | нет | домен есть в Go (sticky-session-operations) |
| fix-api-key-collection-route-equivalence | неясно (паритет) | 2026-08-31 | 7/7 | нет | домен есть в Go (api-keys) |
| fix-api-key-format-contract | неясно (паритет) | 2026-08-31 | 12/12 | нет | домен есть в Go (api-keys) |
| fix-api-key-limit-patch-preserve-usage | неясно (паритет) | 2026-08-26 | 6/6 | — | домен есть в Go (api-keys) |
| fix-api-key-no-store | неясно (паритет) | 2026-09-04 | 7/7 | нет | домен есть в Go (api-keys) |
| fix-audio-cancel-reservation | неясно (паритет) | 2026-08-28 | 10/10 | — | домен есть в Go (api-keys) |
| fix-compact-api-key-dialog | неясно (паритет) | 2026-08-27 | 7/7 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-dashboard-overview-error | неясно (паритет) | 2026-09-05 | 8/8 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-embeddings-cancel-reservation | неясно (паритет) | 2026-08-26 | 9/9 | — | домен есть в Go (api-keys) |
| fix-mobile-donut-overflow | неясно (паритет) | 2026-08-28 | 7/7 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-reports-full-cost-thousands-separators | неясно (паритет) | 2026-08-20 | 7/8 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-request-log-timeframe-refresh | неясно (паритет) | 2026-09-02 | 12/12 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-reset-credit-claim-cancellation | неясно (паритет) | 2026-09-02 | 6/6 | нет | домен есть в Go (rate-limit-reset-credits) |
| fix-settings-initial-load-error | неясно (паритет) | 2026-08-26 | 12/12 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| fix-source-responses-sse-keepalives | неясно (паритет) | 2026-08-27 | 10/10 | нет | домен есть в Go (responses-api-compat) |
| fix-transcription-sdk-fingerprint | неясно (паритет) | 2026-09-05 | 6/6 | — | домен есть в Go (audio-transcriptions-compat) |
| fix-windows-asset-mime-types | неясно (паритет) | 2026-08-13 | 4/4 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| keep-reauth-required-access-routable | неясно (паритет) | 2026-08-31 | 13/13 | нет | домен есть в Go (account-routing, usage-refresh-policy) |
| normalize-single-account-warmup-summary | неясно (паритет) | 2026-08-16 | 7/7 | — | домен есть в Go (proxy-warmup) |
| omit-oauth-callback-query-logs | неясно (паритет) | 2026-09-02 | 7/7 | нет | домен есть в Go (oauth-callback-privacy) |
| omit-unset-chat-tools | неясно (паритет) | 2026-08-14 | 7/7 | да | домен есть в Go (chat-completions-compat) |
| preserve-agent-control-output-slimming | неясно (паритет) | 2026-08-28 | 11/11 | нет | домен есть в Go (responses-api-compat) |
| preserve-dashboard-cancelled-count | неясно (паритет) | 2026-08-15 | 5/5 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| preserve-embedding-explicit-null-fields | неясно (паритет) | 2026-08-26 | 8/8 | да | домен есть в Go (model-source-routing) |
| preserve-openai-root-error-envelope | неясно (паритет) | 2026-09-02 | 8/8 | нет | домен есть в Go (responses-api-compat) |
| preserve-previous-response-source-ownership | неясно (паритет) | 2026-08-26 | 8/8 | да | домен есть в Go (responses-api-compat) |
| probe-valid-token-floor | неясно (паритет) | 2026-08-30 | 7/7 | — | домен есть в Go (usage-refresh-policy) |
| purge-stale-hard-codex-session-mappings | неясно (паритет) | 2026-08-06 | 17/17 | — | домен есть в Go (sticky-session-operations) |
| reconcile-durable-full-resend-owner | неясно (паритет) | 2026-08-06 | 7/7 | — | домен есть в Go (responses-api-compat) |
| recover-dashboard-routes | неясно (паритет) | 2026-09-04 | 9/9 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| recover-rate-limited-on-confirmed-reset | неясно (паритет) | 2026-08-15 | 18/18 | — | домен есть в Go (account-routing, usage-refresh-policy) |
| recover-sequenced-codex-prewarm | неясно (паритет) | 2026-07-16 | 12/12 | — | домен есть в Go (responses-api-compat) |
| redesign-weekly-pace-runway | неясно (паритет) | 2026-08-31 | 0/18 | — | UI; web/ унаследован от легаси React — сверить с web/src |
| refuse-disabled-model-source-routing | неясно (паритет) | 2026-08-30 | 8/8 | нет | домен есть в Go (responses-api-compat) |
| reject-duplicate-trusted-identities | неясно (паритет) | 2026-09-02 | 3/3 | нет | домен есть в Go (admin-auth) |
| reject-truncated-chat-completions | неясно (паритет) | 2026-08-20 | 10/10 | нет | домен есть в Go (chat-completions-compat) |
| relay-unmodified-sse-frames-verbatim | неясно (паритет) | 2026-08-17 | 9/9 | — | домен есть в Go (responses-api-compat) |
| reports-api-key-filter | неясно (паритет) | 2026-08-18 | 7/7 | нет | домен есть в Go (reports) |
| restore-route-scroll | неясно (паритет) | 2026-08-26 | 6/6 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| route-model-sources-off-websocket | неясно (паритет) | 2026-08-16 | 14/14 | нет | домен есть в Go (responses-api-compat) |
| settle-compact-failover-before-health | неясно (паритет) | 2026-08-14 | 23/23 | да | домен есть в Go (usage-refresh-policy) |
| settle-keyed-stream-before-health | неясно (паритет) | 2026-09-05 | 9/9 | нет | домен есть в Go (api-keys, model-source-routing, responses-api-compat) |
| settle-keyed-stream-midloop-health | неясно (паритет) | 2026-08-26 | 25/25 | нет | домен есть в Go (api-keys) |
| show-cancelled-request-logs | неясно (паритет) | 2026-08-16 | 8/8 | нет | домен есть в Go (usage-error-metrics) |
| skip-empty-usage-reservations | неясно (паритет) | 2026-08-17 | 13/13 | нет | домен есть в Go (api-keys) |
| skip-passthrough-request-field-validation | неясно (паритет) | 2026-09-04 | 14/14 | нет | домен есть в Go (chat-completions-compat, responses-api-compat) |
| slim-nested-tool-output-images | неясно (паритет) | 2026-07-17 | 6/6 | — | домен есть в Go (responses-api-compat) |
| smooth-consumption-refresh | неясно (паритет) | 2026-08-31 | 8/8 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| source-model-reasoning-metadata | неясно (паритет) | 2026-08-17 | 11/12 | нет | домен есть в Go (model-catalog-compat) |
| spare-account-health-from-model-rejections | неясно (паритет) | 2026-08-30 | 7/7 | — | домен есть в Go (account-routing) |
| stream-idle-timeout-account-neutral | неясно (паритет) | 2026-08-14 | 6/6 | да | домен есть в Go (responses-api-compat) |
| support-conversation-grouping | неясно (паритет) | 2026-07-22 | 55/55 | частично | UI; web/ унаследован от легаси React — сверить с web/src |
| surface-reasoning-token-usage | неясно (паритет) | 2026-08-18 | 13/13 | нет | UI; web/ унаследован от легаси React — сверить с web/src |
| surface-reports-cancellation-totals | неясно (паритет) | 2026-08-16 | 15/15 | — | домен есть в Go (usage-error-metrics) |
| treat-invalid-refresh-token-as-reauth | неясно (паритет) | 2026-08-26 | 5/5 | нет | домен есть в Go (account-routing) |
| usage-refresh-explicit-deactivation-only | неясно (паритет) | 2026-09-05 | 12/12 | — | домен есть в Go (usage-refresh-policy) |
| validate-stream-lifecycle-events-only | неясно (паритет) | 2026-08-17 | 9/9 | — | домен есть в Go (responses-api-compat) |
| warm-free-plan-transition | неясно (паритет) | 2026-08-20 | 8/8 | нет | домен есть в Go (usage-refresh-policy) |
