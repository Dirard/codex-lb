# 06 — Аудит фронтенда (web/)

Дата: 2026-10-28. Ревизия: `main` @ `0b674e97`, рабочее дерево чистое. Область: `web/` (React 19 + Vite 8/rolldown + TanStack Query 5 + Tailwind 4 + radix-ui) и его встраивание в Go (`internal/adapters/webui`). Контракты: `openspec/specs/go-runtime/spec.md`, `openspec/specs/frontend-architecture/spec.md`.

Все временные артефакты лежат в `/home/dirard/dev/ai-apps/codex-lb/.local/audit/scratch-web/`: логи, `dist/`, `dist-sm/` (сборка с sourcemap), `coverage/`, скрипты анализа в `tools/`, Go-бинарь `codex-lb-bin`. Отслеживаемые файлы не менялись, `npm install` не запускался, к удалённым хостам не обращался.

## Резюме

1. **Маршруты в целом совпадают.** Ни одна страница не обращается к эндпоинту, которого нет в Go. Есть одно исключение, и это мёртвый код: `listAutomationRuns` → `GET /api/automations/{id}/runs`. Контрактный тест `src/__integration__/go-runtime.test.ts` против свежесобранного Go-бинаря проходит (11/11 за 2,9 с). Однако по умолчанию он пропускается, а CI в репозитории нет.
2. **Мёртвый UI находится на уровне полей, а не страниц.** Фронтенд ожидает около 25 полей, которые Go никогда не отдаёт. Из-за этого никогда не отрисовываются: детали маршрута через upstream proxy pools, разбивка стоимости, запрошенный/фактический tier, полосы pooled credits у API-ключей, индикатор дублирующегося email, а также ветки read-only/guest (`role`/`permissions`). Две из этих потерь — реальные баги: скорость генерации (tokens/s) никогда не показывается, а аккаунты с одинаковым email не различаются. Сама спецификация `frontend-architecture` до сих пор требует proxy pools и guest-режим, хотя `go-runtime` их явно исключает.
3. **Главная проблема скорости — доставка.** Go отдаёт JS/CSS/API без сжатия (проверено `curl`), хотя спецификация требует gzip. При первой загрузке `/dashboard` уходит около 1,87 МБ raw вместо примерно 0,52 МБ gzip. Все три локали (≈300 КБ raw) лежат в initial-чанке. Recharts (580 КБ raw / 154 КБ gzip) грузится уже на маршруте по умолчанию.
4. **Быстрые победы.** Включение параллельного запуска vitest сокращает тесты с 229,7 с до 20–23 с (3 прогона, всё зелёное). Перед этим нужно починить утечку таймера в `oauth-dialog.tsx` — именно она уронила прогон с покрытием. Кроме того, можно убрать 3 неиспользуемых devDeps, сделать явным выбор TypeScript 7 и выбрать один пакетный менеджер.
5. Находок P0 нет: потерь данных и уязвимостей во фронтенде не найдено. Итого: P1 — 4, P2 — 15, P3 — 12.

## 1. Проверки

| Команда | Результат | Время | Примечание |
|---|---|---|---|
| `npm --prefix web run lint` (eslint 10) | OK, 0 ошибок, 0 предупреждений | 10,6 с | конфиг без type-aware правил |
| `npm --prefix web run typecheck` (`tsc -b`) | OK | 1,73 с (инкрементально) | `.bin/tsc` указывает на **TypeScript 7.0.2** (`@typescript/native`). С `--force`: TS7 — 1,64 с, TS6 (`node_modules/typescript`) — 8,87 с |
| `npx --prefix web vite build --outDir scratch/dist --emptyOutDir` | OK, 4161 модуль, 81 файл, dist 2,53 МБ | 1,92 с (vite 1,13 с) | хэши совпали со встроенным `internal/adapters/webui/dist`, то есть встроенная сборка актуальна |
| `npm --prefix web run test` (vitest 4, `fileParallelism: false`) | OK: 156 файлов (1 пропущен), 1264 теста прошло, 11 пропущено | **229,7 с** | пропущены 11 тестов `go-runtime.test.ts` (нужен `CODEX_LB_TEST_BINARY`) |
| `vitest run --fileParallelism` ×3 | OK, 1264/1264 во всех прогонах | **20,4 / 22,9 / 21,2 с** | 32 ядра |
| `vitest run --fileParallelism --coverage` (отчёт в scratch) | **rc=1**: тесты зелёные, но 1 unhandled error | 29,6 с | `ReferenceError: window is not defined` из `oauth-dialog.tsx:49` (WEB-4). Покрытие: строки 85,0 %, ветки 76,7 %, функции 81,0 %, операторы 84,6 % |
| `CODEX_LB_TEST_BINARY=… vitest run src/__integration__/go-runtime.test.ts` | OK, 11/11 | 2,87 с | бинарь собран офлайн (`GOPROXY=off go build`) в scratch |

Предупреждения: в тестах vite советует `@vitejs/plugin-react` вместо `-swc` («no swc plugins are used»); jsdom сообщает о не реализованных `scrollTo`/`canvas`.

## 2. Бандл

### Самые крупные чанки (`vite build`, gzip -9)

| Чанк | raw, КБ | gzip, КБ | В initial (modulepreload) | Содержимое (по sourcemap) |
|---|---:|---:|:-:|---|
| `es6-*.js` | 579,9 | 154,2 | нет (lazy) | recharts 422 КБ, es-toolkit 26, immer 18, d3-* ≈50, decimal.js-light 13, redux toolkit 11 |
| `react-*.js` | 333,8 | 85,8 | **да** | i18next 42 + language detector 16 + **en/ko/zh-CN JSON ≈ 300 КБ** |
| `index-*.js` (entry) | 220,5 | 65,7 | да | react-router 38, react-hook-form 33, settings 19, layout 16, auth 13, ui 10, radix menu 10, input-otp 9 |
| `vendor-react-*.js` | 181,8 | 56,3 | да | react-dom 174 |
| `settings-page-*.js` | 142,4 | 28,4 | нет | settings 63, model-sources 20, sticky-sessions 13, model-prices 10 |
| `dashboard-page-*.js` | 116,4 | 27,1 | нет | dashboard 105 |
| `index-*.css` | 106,4 | 17,5 | да (stylesheet) | tailwind + tw-animate-css + shadcn/tailwind.css |
| `model-source-multi-select-*.js` | 102,6 | 25,5 | нет | date-fns 41 + react-day-picker 39 (используются только в `expiry-picker.tsx`) |
| `api-client-*.js` | 87,0 | 23,8 | да | zod 82 |
| `automations-page-*.js` | 56,4 | 12,6 | нет | |
| `accounts-page-*.js` | 56,1 | 14,3 | нет | |
| `dialog-*.js` | 41,9 | 11,8 | да | sonner 33 + radix dialog |
| `vendor-query-*.js` | 36,3 | 11,3 | да | @tanstack/query-core |
| `dist-*.js` / `button-*.js` / `es2015-*.js` | 33 / 32 / 30 | 12 / 10 / 11 | да | floating-ui / tailwind-merge / react-remove-scroll и другие части radix |

Итоги:
- **Initial (entry, всё модулепредзагружаемое и CSS): 1 113 КБ raw / 311 КБ gzip.** На локали приходится около 27 % gzip.
- Маршрут по умолчанию `/dashboard`: entry (1 007 КБ raw / 294 КБ gz) + CSS (106 / 17,5) + статические чанки страницы (181 / 52) + recharts (580 / 154) ≈ **1,87 МБ raw / ≈ 0,52 МБ gzip**.
- Дополнительные статические чанки других маршрутов (gzip): settings +62 КБ, automations +33, accounts +43, apis +24, reports +9 (recharts подгружается отдельно).
- Весь JS: 2,31 МБ raw / 643 КБ gzip.

### Наблюдения

- **Code-splitting по маршрутам есть.** Шесть страниц грузятся через `lazy()` (`App.tsx:22-37`), `key-reports-page` — тоже lazy (`auth-gate.tsx:17`). Recharts вынесен в async-only чанк (`components/lazy-recharts.ts`): он не modulepreload и статически не импортируется из entry. Требование спецификации `frontend-architecture/spec.md:2301` по части графиков выполнено.
- Однако recharts всё равно нужен на `/dashboard` (`usage-donuts.tsx`, `stats-grid.tsx` → donut/sparkline). Значит, 154 КБ gzip фактически входят в загрузку маршрута по умолчанию (WEB-17).
- `manualChunks["vendor-ui"] = ["radix-ui"]` (`vite.config.ts:19`) ничего не делает. Чанк `vendor-ui` не создаётся, потому что мета-пакет `radix-ui` реэкспортирует `@radix-ui/*` из других путей (WEB-25).
- **Шрифты** (Geist Sans variable 69 КБ, JetBrains Mono latin и latin-ext 31 и 12 КБ) лежат в своём хостинге, woff2, `font-display: swap`, у mono есть `unicode-range`. Но они находятся в `public/fonts` без хэша, и Go отдаёт их без `Cache-Control`, `Last-Modified` и `ETag` (WEB-19).
- **Сжатие (замер на локальном Go-бинаре, офлайн):**
  - `GET /assets/index-DNEG0HzU.js` с `Accept-Encoding: gzip, br` → `200`, без `Content-Encoding`, `Content-Length: 220477`, `Cache-Control: public, max-age=31536000, immutable`;
  - `GET /fonts/GeistSans-Variable.woff2` → без `Cache-Control`/`ETag`/`Last-Modified`;
  - `GET /dashboard` и `GET /api/dashboard-auth/session` → `Cache-Control: no-store`, без сжатия.

  Это нарушает `frontend-architecture/spec.md:2303` («Dashboard API and static-asset responses MUST be served gzip-compressed…; `index.html` MUST remain `no-cache`»). См. WEB-1.
- zod (82 КБ raw) попадает в initial через `api-client`, потому что ответы проверяются zod-схемами. Переход на `zod/mini` дал бы примерно −40 КБ raw, но требует переписать около 27 файлов схем. Низкий приоритет.
- i18n: 1490 ключей в каждой локали, все три синхронизированы. Неиспользуемых ключей по эвристике 19, из них 14 — `accounts.proxyBinding.*`.

## 3. Мёртвый UI: фронтенд против Go

Маршруты собраны из всех `HandleFunc`/`Handle` в `internal/adapters/httpapi/*.go` и `cmd/codex-lb/runtime.go` с учётом циклов `range []string{…}`. Все опциональные группы маршрутов подключены в `cmd/codex-lb/runtime.go:118-138` (firewall, error archives, catalog, pricing, usage, warmups, automations, runtime updater).

### 3.1 Вызов из фронтенда → маршрут в Go (подозрительные и опциональные фичи)

| Фича (`web/src/features/…`) | Вызов фронтенда | Маршрут в Go | Вывод |
|---|---|---|---|
| conversation-archive | `GET /api/conversation-archive/records?requestId&requestedAt` (`api.ts:44`) | есть, `error_archive.go:54` (+ `/files` `:35`) | поддерживается, но это только архив ошибок; `requestedAt` Go игнорирует |
| sticky-sessions | `GET /api/sticky-sessions?…`, `POST …/delete`, `…/delete-filtered`, `…/purge` | есть, `affinity.go:14-61` | поддерживается. `deleteStickySession` (`api.ts:35`, → `DELETE /{kind}/{key}`) нигде не используется |
| quota-planner | — | нет; `go-runtime/spec.md:51`: «SHALL NOT provide … the quota phase planner» | в git ничего нет, на диске остались пустые каталоги `components/`, `hooks/` |
| health | `GET /health/ready` (`health/api.ts:5`) | есть, `cmd/codex-lb/runtime.go:172` | поддерживается |
| runtime | `GET /api/runtime/version`, `GET /api/runtime/updates`, `POST …/check|apply|rollback` | есть, `runtime_updates.go:28-85` | поддерживается |
| firewall | `GET/POST /api/firewall/ips`, `DELETE /{ip}` | есть, `firewall.go:16-41` | поддерживается |
| model-prices | `GET /api/model-prices`, `PUT/DELETE /{model}` | есть, `model_pricing.go:24-28` | поддерживается |
| model-sources | `GET/POST /api/model-sources/`, `PATCH/DELETE /{id}` | есть, `model_sources.go:11-16` | поддерживается |
| key-reports | `GET/POST/DELETE /api/key-reports/session`, `GET /api/key-reports/reports` | есть, `key_report_session.go:24-60` | поддерживается |
| **automations** | `GET /api/automations/{id}/runs?limit=20` (`automations/api.ts:138-140`) | **нет**. Запрос перехватывает `GET /api/automations/` (`automations.go:27`) и возвращает **список jobs** | мёртвый код: запрос стоит в `useAutomations`, который вызывается с `enableQueries: false` (`automations-page.tsx:184`) |
| automations (остальное) | list, options, runs, runs/options, runs/{id}/details, CRUD, run-now | есть, `automations.go:26-143` | поддерживается |
| accounts | 20 вызовов, включая `DELETE ?delete_history=true` | есть, `accounts.go`, `account_usage.go`, `warmups.go`, `admin.go` | поддерживается |
| dashboard | overview, projections, request-logs (+options), conversations (+{id}) | есть, `reports.go:16-110` | поддерживается |
| (только MSW) | `POST /api/accounts/:id/export` (`test/mocks/handlers.ts:1149`) | нет | мок, который расходится с Go; фронтенд этот путь не вызывает |
| (только MSW) | `GET /api/automations/:id/runs` (`handlers.ts:1584`) | нет | мок для мёртвой функции |

### 3.2 Поля, которые фронтенд читает, а Go никогда не отдаёт

Сравнивались ключи zod-схем со всеми `json:"…"` тегами и строковыми литералами Go (`tools/schema-fields.mjs`). Результат: `scratch-web/schema-fields-missing-in-go.txt` (32 кандидата, после ручной проверки по всему Go-коду ≈ 25).

| Поля (схема) | Где отрисовываются | Последствие при работе с Go |
|---|---|---|
| `upstreamProxyRouteMode/PoolId/EndpointId/FallbackUsed/FailClosedReason` (`dashboard/schemas.ts:217-221`) | секция маршрута в деталях запроса, `recent-requests-table.tsx:621-655` | никогда не показывается. Proxy pools исключены (`go-runtime/spec.md:51`) |
| `costBreakdown.{input,cachedInput,output,total}Usd` (`schemas.ts:195-198,246`) | `formatRequestCostSummary`, `recent-requests-table.tsx:230-280` | функция всегда возвращает `null`, разбивка стоимости не видна |
| `outputTokensRaw` (`schemas.ts:241`) | `formatGenerationSpeed`, `recent-requests-table.tsx:282-293` | **tokens/s никогда не вычисляется** (WEB-7), хотя Go отдаёт `outputTokens` и задержки |
| `requestedServiceTier`, `actualServiceTier`, `upstreamTransport` (`schemas.ts:216,227-228`) | бейджи tier/transport, `recent-requests-table.tsx:416-486,601` | не показываются |
| `failurePhase/Detail/ExceptionType`, `upstreamStatusCode/ErrorCode`, `bridgeStage`, `connectionRequestKind`, `archiveRequestId` (`schemas.ts:208-237`) | почти не используются; `archiveRequestId` — `:708` с fallback на `requestId` | балласт схемы |
| `isEmailDuplicate` (`accounts/schemas.ts:103`) | 6 мест (`accounts-page.tsx:191`, `accounts/…/account-list.tsx:172`, `dashboard/…/account-list.tsx:367`, `account-cards.tsx:51`, `dashboard/utils.ts:198`, `automations/account-display.ts:48`) | **аккаунты с одинаковым email не различаются** (WEB-8) |
| `pooledRemainingPercentPrimary/Secondary`, `pooledCapacityCreditsPrimary` (`api-keys/schemas.ts:73-75`) | `apis/components/api-list-item.tsx:17-19` | полосы pooled credits никогда не рисуются |
| `role`, `permissions` (`auth/schemas.ts:25-40`) | `canWrite` в 23 местах, `isAdmin = initialized` (`dashboard-page.tsx:86-107`) | всегда используются значения по умолчанию `admin`/`read,write`. Это остатки guest-режима, который Go не поддерживает |
| `additionalQuotaPolicies` (`settings/schemas.ts:125`) | `routing-settings.tsx:337-339` | всегда `[]` |
| `redeemStartedAt` (`accounts/schemas.ts:117`) | нигде | балласт |

### 3.3 Расхождение спецификаций

`openspec/specs/frontend-architecture/spec.md` до сих пор нормативно требует то, что `go-runtime` исключает или не реализует:

- proxy pools — `:1013`, `:1022`, `:1219`, `:1249`, `:1268`, `:1287`;
- guest — `:2400`;
- cost breakdown — `:680`, `:700`;
- requested/actual tier — `:192`;
- pooled bars — `:454`;
- duplicate email — `:388`;
- compressed serving — `:2301` (в Go не реализовано).

По правилам `AGENTS.md` удалять код нужно через OpenSpec change, который приводит `frontend-architecture` в соответствие с `go-runtime` (WEB-5).

## 4. Производительность UI

### Polling, когда открыта вкладка `/dashboard` (настройки по умолчанию)

| Запрос | Интервал | Источник | Замечание |
|---|---|---|---|
| `/api/dashboard/overview?timeframe=…` | 15 с (`refreshSeconds` по умолчанию) | `use-dashboard.ts:12-18` | + фокус окна |
| `/api/dashboard/overview?timeframe=1d` | 60 с | `status-bar.tsx:102-108` | **на всех страницах**, только ради `lastSyncAt`; при другом timeframe на дашборде получается второй тяжёлый запрос (WEB-12) |
| `/api/dashboard/projections` | 15 с | `use-dashboard.ts:21-30` | 30 дней истории квот + `ListAccountQuota` на каждый аккаунт (`reports_projections.go:13-37`) (WEB-13) |
| `/api/request-logs`, `/api/request-logs/options` | 30 с + фокус | `use-request-logs.ts:158,189` | серверная пагинация, `limit=25`, `keepPreviousData` — нормально; facets можно запрашивать реже |
| `/api/accounts` | 30 с | `app-header.tsx:54-61` | на всех страницах ради бейджа reset-credits (WEB-29) |
| `/health/ready` | 60 с | `status-bar.tsx:95-101` | лёгкий |
| `/api/settings`, `/api/runtime/version` | staleTime 30 с / 6 ч | `status-bar.tsx:110-119` | нормально |

Итого около 15 запросов в минуту на одну вкладку; при возврате фокуса одновременно уходит до 6 запросов. Глобально `refetchOnWindowFocus: false` (`lib/query-client.ts:9`), но 9 хуков включают его локально. Фоновые вкладки не поллятся (`refetchIntervalInBackground: false` везде) — это правильно.

### Прочее

- `buildDashboardView` (`dashboard/utils.ts:734`) получает `logPage.requests`, хотя только прокидывает их дальше. Из-за этого при каждом обновлении логов (30 с) и при пагинации весь view пересобирается, и donut-ы, карточки аккаунтов и stats перерисовываются (`dashboard-page.tsx:209-222`) (WEB-14).
- Большие таблицы: request-logs и conversations — серверная пагинация; automations — пагинация; список аккаунтов, ключей и firewall — без пагинации (ожидаются десятки строк). Виртуализация не нужна.
- `AdvancedSettingsGroup` размонтирует дочерние секции, пока закрыт, поэтому запросы sticky/firewall/model-sources идут только после раскрытия. Сделано хорошо, но их код всё равно лежит в чанке settings (142 КБ).
- Таймеры: `SubscriptionRemaining` создаёт отдельный `setInterval` на 60 с для каждого аккаунта (`subscription-remaining.tsx:22`); `status-bar` — каждые 10 с. Это мелочь.

## 5. Качество

- **Гигантские компоненты:** `routing-settings.tsx` — 1330 строк, около 15 полей с ручной валидацией и diff (`:178-330`), хотя react-hook-form и zod уже используются в 12–14 файлах. Также `automations-page.tsx` — 914, `dashboard/utils.ts` — 876, `automation-job-dialog.tsx` — 796 (19 `useState`), `recent-requests-table.tsx` — 781, `dashboard-page.tsx` — 665.
- **Дублирование:**
  - логика подписи аккаунта (`displayName || email || accountId`) повторяется в 13 местах;
  - блоки `accountLabelMap`/`emailLabelIds` скопированы один в один (`recent-requests-table.tsx:346-364` и `conversation-table.tsx:56-74`);
  - в `oauth-dialog.tsx:30-58` своя копия `CopyButton` без очистки таймера, хотя есть общий `components/copy-button.tsx`, где очистка сделана.
- **Код, который используют только тесты:**
  - `utils/constants.ts` — `STATUS_LABELS`, `ERROR_LABELS`, `ROUTING_LABELS`, `KNOWN_PLAN_TYPES`, `MESSAGE_TONE_META` (это английские ярлыки, вытесненные i18n);
  - `utils/formatters.ts:183,224,353,466,478`;
  - `model-source-form.ts:303,307`;
  - `sticky-sessions/api.ts:35` не используется вообще;
  - `useAutomations`: запросы и `listAutomationRuns`.
- **Тесты проверяют моки, а не поведение:**
  - `test/mocks/handlers.ts` (2185 строк) и `factories.ts` (794) описывают контракт Python-эпохи: маршруты, которых нет в Go, и поля, которых Go не отдаёт (`factories.ts:373,384`);
  - `handler-coverage.test.ts` сверяет моки с вручную написанным списком, а не с маршрутами Go;
  - `recent-requests-table.test.tsx` около 76 раз обращается к полям, которых нет в Go: тесты проверяют ветки, недостижимые в продакшене;
  - `settings-page.test.tsx` мокает 13 модулей и проверяет props замоканных детей;
  - `lazy-recharts` мокается в 8 тестах.

  Единственная реальная проверка контракта — `go-runtime.test.ts`, и она по умолчанию пропускается (`make test-web-contract` есть, CI нет).
- **Зависимости:**

  | Пакет | Используется? | Доказательство |
  |---|---|---|
  | `react-doctor` (33 МБ) | нет | нет упоминаний ни в скриптах, ни в репозитории |
  | `bun2nix` | нет | Nix выведен из поставки (`go-runtime/spec.md:51`) |
  | `@playwright/test` (+ `playwright`/`playwright-core`, 19 МБ) | нет | нет конфига и тестов; vitest исключает несуществующие `browser-smoke/**` и `screenshots/**` (`vite.config.ts:70`); `.dockerignore:23` упоминает `web/playwright-report` |
  | `shadcn` (14 МБ) | частично | только ради `@import "shadcn/tailwind.css"` (`index.css:3`, 16 КБ CSS); CLI не нужен |
  | `typescript` 6.0.3 и `@typescript/native` (= `typescript@7.0.2`) | обе | `.bin/tsc` → `../@typescript/native/bin/tsc` из-за коллизии bin-ссылок, то есть typecheck и build идут на **TS7**. TS6 нужен парсеру `typescript-eslint` и `tsserver`. Схема работает, но держится на порядке линковки |
  | `packageManager: bun@1.3.14` + `bun.lock` | смешано | скрипты, Makefile и Dockerfile запускают `npm`; `install:deps` = `npx --yes bun@1.3.14 install --frozen-lockfile` скачивает bun при каждой Docker-сборке; `package-lock.json` нет, поэтому `npm ci` невозможен; `.agents/environments/environment.toml` устарел (`uv sync`, `cd frontend && bun install`) |
  | `react-day-picker` + `date-fns` (+ `@date-fns/tz`) | 1 место | только `api-keys/components/expiry-picker.tsx` (≈85 КБ raw / ≈25 КБ gz) |
  | `@vitejs/plugin-react-swc` | да | vite 8 советует `@vitejs/plugin-react` |

## 6. Доступность и UX (кратко)

- Все 19 иконочных кнопок (`size="icon*"`) подписаны (`aria-label`/`title`). Кликабельных `div`/`tr` без роли не найдено. Строки списков сделаны `<button>`. Статус-точки помечены `aria-hidden` и продублированы текстом. Обновление `lang` у `html` сделано.
- `model-prices-settings.tsx` полностью на английском, без `useTranslation` (`:63,97-125`). Это нарушает «Settings page renders in the active locale» (`frontend-architecture/spec.md:1593`).
- Тёмная тема включается только после выполнения всего entry-бандла (`main.tsx:16`, `use-theme.ts:24`). Пока загружается около 1 МБ, пользователь тёмной темы видит светлую.
- В футере ссылка ведёт на апстрим `github.com/soju06/codex-lb` (`status-bar.tsx:15,230`), а origin — `Dirard/codex-lb`. Резервная версия `__APP_VERSION__` = `"1.0.0"` из `web/package.json`.
- Плотная типографика: много `text-[11px]` и кнопок `h-6` (24 px — нижняя граница WCAG 2.5.8). Это не блокер.

## Таблица находок

| ID | Пр. | Категория | Доказательство | Исправление | Труд. | Риск | Как проверить |
|---|---|---|---|---|---|---|---|
| WEB-1 | P1 | perf | `webui.go:95` (`http.ServeContent` без кодирования); замер: `/assets/index-*.js` при `Accept-Encoding: gzip, br` отдаётся без `Content-Encoding`, 220 477 Б; API тоже без сжатия; spec `frontend-architecture:2301-2303` | при сборке создавать `*.gz`/`*.br` (Node `zlib`, без новых зависимостей) и встраивать; `serveFile` выбирает вариант по `Accept-Encoding`, ставит `Vary: Accept-Encoding`, на `Range` отдаёт raw; для `/api/` — gzip middleware (не трогать `/v1`, `/backend-api`); `index.html` → `no-cache` по spec | M | ETag/Range, двойное сжатие за reverse proxy | `curl -sI -H 'Accept-Encoding: gzip' …/assets/…`; тест в `webui_test.go` |
| WEB-2 | P1 | perf | `i18n/index.ts:5-7` статически импортирует 3 локали; чанк `react-*.js` 333,8 КБ / 85,8 КБ gz в modulepreload | статически только `en` (fallback), `ko`/`zh-CN` через `import()` и `i18n.addResourceBundle` до первого рендера (`await` в `main.tsx`) | S | короткая задержка при не-en языке | build: в initial нет ko/zh; initial gzip ≈ −48 КБ (−15 %); `i18n/index.test.ts` |
| WEB-3 | P1 | perf (dev) | `vite.config.ts:71` `fileParallelism: false`: 229,7 с; с параллельностью 20,4 / 22,9 / 21,2 с, 1264/1264 | удалить `fileParallelism: false` (после WEB-4) | S | скрытая общая state между файлами | 5 прогонов `npm run test` подряд без падений |
| WEB-4 | P2 | bug | `oauth-dialog.tsx:30-58`: `setTimeout(() => setCopied(false), 2000)` без очистки; coverage-прогон rc=1, `ReferenceError: window is not defined` | заменить на общий `components/copy-button.tsx` (в нём есть cleanup) | S | нет | coverage-прогон без unhandled errors |
| WEB-5 | P1 | dead-code/spec | `frontend-architecture/spec.md:192,388,454,680,700,1013,1022,1219,1249,1268,1287,2400` противоречат `go-runtime/spec.md:51` | OpenSpec change «align-frontend-spec-with-go-runtime»: удалить или переформулировать эти требования, затем код (WEB-6/9/10/24) | M | решение владельца: что вернуть в Go | `openspec validate --specs` |
| WEB-6 | P2 | dead-code | поля без Go-источника: `dashboard/schemas.ts:195-247` против `domain/reports.go:147-178`; UI `recent-requests-table.tsx:230-280,416-486,621-655` | удалить поля и ветки (upstream proxy, cost breakdown, tiers, failure*, bridgeStage); если разбивка стоимости нужна — сначала добавить её в Go | S–M | нет (ветки недостижимы) | `go-runtime.test.ts` проходит; тесты на эти ветки удаляются |
| WEB-7 | P2 | bug | `recent-requests-table.tsx:283`: `formatGenerationSpeed` требует `outputTokensRaw`, Go отдаёт только `outputTokens` | `const out = request.outputTokensRaw ?? request.outputTokens` | S | нет | unit-тест со строкой формы Go; детали запроса показывают tokens/s |
| WEB-8 | P2 | bug | `isEmailDuplicate` не отдаётся Go; 6 мест использования (см. §3.2) | вычислять на клиенте: подсчёт нормализованных email в одном хелпере `useAccountLabels` (вместе с WEB-15) | S | нет | два аккаунта с одинаковым email → показан accountId |
| WEB-9 | P2 | dead-code | `api-keys/schemas.ts:73-75`, `apis/components/api-list-item.tsx:17-19` | удалить pooled bars (или реализовать в Go по решению владельца) | S | нет | страница APIs без изменений на Go-данных |
| WEB-10 | P2 | simplification | `auth/schemas.ts:25-40` (`role`/`permissions` не отдаются Go); `canWrite` ×23; `dashboard-page.tsx:86-107`; `use-auth.ts:147-157` | удалить роли и права, оставить `authenticated`; убрать read-only ветки и guest-логику conversations | M | 401-хэндлер станет проще | тесты auth и dashboard; `go-runtime.test.ts` |
| WEB-11 | P2 | dead-code | `automations/api.ts:138-140` → несуществующий `GET /api/automations/{id}/runs` (ловится `automations.go:27`); запросы выключены (`automations-page.tsx:184`); мок `handlers.ts:1584` | удалить запросы из `useAutomations`, `listAutomationRuns` и мок; оставить только мутации | S | нет | страница automations, `handler-coverage.test.ts` |
| WEB-12 | P2 | perf | `status-bar.tsx:102-108`: полный overview каждые 60 с на всех страницах ради `lastSyncAt`; при нестандартном timeframe — дубль | лёгкий источник `lastSyncAt`: поле в `/api/runtime/version` или `/api/accounts`, либо отдельный `GET /api/dashboard/status` (через OpenSpec) | S–M | изменение контракта | на странице Settings в сети нет `/api/dashboard/overview` |
| WEB-13 | P2 | perf (гипотеза о стоимости) | `use-dashboard.ts:21-30`: projections каждые 15 с; `reports_projections.go:13-37` (30 дней истории + N запросов квот) | `staleTime`/`refetchInterval` 5 мин или перезапрос при изменении `lastSyncAt` | S | менее свежий прогноз | замерить время `/api/dashboard/projections` на реальной БД; число запросов в минуту |
| WEB-14 | P3 | perf | `dashboard-page.tsx:209-222`, `utils.ts:734-736`: `requestLogs` только прокидывается, но делает весь view зависимым | передавать `logsQuery.data.requests` напрямую в таблицу, убрать из `buildDashboardView` | S | нет | React Profiler: обновление логов не перерисовывает donuts и cards |
| WEB-15 | P3 | simplification | 13 копий `displayName || email || accountId`; копия `recent-requests-table.tsx:346-364` ≡ `conversation-table.tsx:56-74` | общий `getAccountLabel`/`useAccountLabels` в `features/accounts` | S | нет | grep не находит повторов; тесты |
| WEB-16 | P2 | simplification | `routing-settings.tsx` 1330 строк, ручная валидация `:178-330`; чанк settings 142 КБ | разбить на секции (strategy/capacity/warmup/affinity/pace) с zod-схемой на секцию (react-hook-form уже есть); `lazy()` для Advanced-группы | L | регрессии сохранения → контрактный тест settings PATCH уже есть | `go-runtime.test.ts` «saves real UI setting patches…»; размер чанка |
| WEB-17 | P2 | perf | recharts (580 КБ / 154 КБ gz) на `/dashboard` ради `usage-donuts.tsx` и `stats-grid.tsx` | простые SVG donut и sparkline для дашборда (по образцу `mini-quota-bar`); recharts оставить для Reports, APIs и трендов | M | визуальные отличия → скриншоты до и после | на `/dashboard` нет запроса `es6-*.js`; −154 КБ gz |
| WEB-18 | P3 | simplification | `components/lazy-recharts.ts`: lazy + Suspense на каждый примитив, работает только благодаря совпадению `displayName` (recharts `findAllByType`, `node_modules/recharts/es6/util/ReactUtils.js:63-78`) | `lazy()` на уровне компонентов-графиков | S | нет | графики рендерятся; убрать 8 `vi.mock(lazy-recharts)` |
| WEB-19 | P3 | perf | `public/fonts/*` без хэша; замер: нет `Cache-Control`/`ETag`/`Last-Modified` (`webui.go:86-94`) | перенести шрифты в `src/assets` (Vite хэширует в `/assets/`, immutable) или задать кэш для `/fonts/` | S | нет | `curl -I /assets/…woff2` → immutable |
| WEB-20 | P2 | deps | `package.json`: `react-doctor`, `bun2nix`, `@playwright/test` не используются; `shadcn` нужен только ради 16 КБ CSS; `vite.config.ts:70` исключает несуществующие каталоги | удалить 3 пакета; вендорить `shadcn/tailwind.css` в `src/styles`; почистить exclude и `.dockerignore` | S | нужна регенерация lockfile (сеть — отдельное разрешение) | lint, typecheck, test, build зелёные; `node_modules` ≈ −80 МБ |
| WEB-21 | P2 | deps | `.bin/tsc` → `@typescript/native` (TS 7.0.2) из-за коллизии; TS6 только для eslint; TS7 1,64 с против TS6 8,87 с | явный скрипт `"typecheck": "node node_modules/@typescript/native/bin/tsc -b"` (то же в `build`), комментарий «TS6 — только для typescript-eslint» | S | нет | `npm run typecheck` печатает 7.0.2 независимо от порядка установки |
| WEB-22 | P2 | deps/build | `packageManager: bun@1.3.14` + `bun.lock`, но npm-скрипты, Makefile и Dockerfile; `install:deps` скачивает bun через `npx`; `.agents/environments/environment.toml` устарел | выбрать один менеджер. Рекомендация: npm + `package-lock.json` + `npm ci` (Node всё равно нужен), удалить bun.lock и поле `packageManager`, обновить Dockerfile, Makefile и environment.toml | M | lockfile требует сети и ревью диффа версий | `npm ci && npm run build` в чистом контейнере |
| WEB-23 | P2 | tests | моки расходятся с Go (`handlers.ts:1149,1584`, `factories.ts:373,384`); `handler-coverage.test.ts:115-121` сверяет с ручным списком; `settings-page.test.tsx` мокает 13 модулей; `go-runtime.test.ts` пропускается; `.github/` нет | (a) сделать `make test-web-contract` обязательным (pre-merge/CI); (b) Go-тест выгружает зарегистрированные паттерны, а `handler-coverage` сверяется с этим списком; (c) фабрики строить из Go-фикстур; (d) переписать settings-page-тест на MSW без моков дочерних компонентов | M | рост времени CI на ≈3 с | мок с несуществующим маршрутом валит тест |
| WEB-24 | P3 | dead-code | код только для тестов: `utils/constants.ts:1-44,63-90`, `formatters.ts:183,224,353,466,478`, `sticky-sessions/api.ts:35`, `model-source-form.ts:303,307`; 14 ключей `accounts.proxyBinding.*` ×3 локали; `additionalQuotaPolicies` (`routing-settings.tsx:337-339`); пустые `features/quota-planner/{components,hooks}` | удалить вместе с их тестами | S | нет | lint (`noUnusedLocals`) и тесты |
| WEB-25 | P3 | build | `vite.config.ts:19` `vendor-ui` ничего не делает (чанк не создаётся); предупреждение `plugin-react-swc` | удалить `vendor-ui` (или матчить `/node_modules/@radix-ui/`); заменить на `@vitejs/plugin-react` | S | изменится нарезка чанков | сравнить вывод `vite build` |
| WEB-26 | P3 | i18n/a11y | `model-prices-settings.tsx:63,97-125` — захардкоженный английский; spec `:1593` | вынести строки в локали; разделить на api, hooks и components как в других фичах | S | нет | переключить на ko/zh — строк на английском нет |
| WEB-27 | P3 | UX | `status-bar.tsx:15,230` → `soju06/codex-lb`; fallback `__APP_VERSION__` = `"1.0.0"` | URL репозитория нового владельца (или из `/api/runtime/version`); версию брать только с сервера | S | нет | визуально |
| WEB-28 | P3 | UX | `main.tsx:16`, `use-theme.ts:24`: класс `dark` ставится только после загрузки бандла | inline-скрипт в `index.html` (localStorage + `prefers-color-scheme`) до CSS; CSP в Go не задан | S | при будущем CSP нужен hash | на медленной сети нет светлой вспышки |
| WEB-29 | P3 | perf | `app-header.tsx:54-61`: `/api/accounts` каждые 30 с на всех страницах ради бейджа | реже (5 мин) или брать из уже загруженных данных | S | бейдж обновляется позже | число запросов в минуту на Settings |
| WEB-30 | P3 | simplification/perf | `components/ui/calendar.tsx` используется только в `expiry-picker.tsx`; чанк 102,6 КБ / 25,5 КБ gz | `<input type="date">` + `addDays` на `Date`; удалить `react-day-picker` и `date-fns` | S | UX календаря отличается в разных браузерах | `vite build` без чанка с date-fns |
| WEB-31 | P3 | dead-code | `dashboard/api.ts:89-94,105-110`: параметры `since`/`until` фронтенд никогда не задаёт; `conversation-archive/api.ts:41` шлёт `requestedAt`, который Go игнорирует | удалить неиспользуемые параметры | S | нет | тесты api |

## Быстрые победы (каждая ≤ 0,5 дня, по эффекту)

1. **WEB-4 + WEB-3:** заменить локальный `CopyButton` на общий и убрать `fileParallelism: false`. Тесты сократятся с 230 с до ≈ 21 с.
2. **WEB-2:** lazy-загрузка `ko`/`zh-CN`. Initial gzip уменьшится примерно на 48 КБ (−15 %).
3. **WEB-7:** однострочный fallback `outputTokensRaw ?? outputTokens` вернёт показ tokens/s.
4. **WEB-8:** вычислять `isEmailDuplicate` на клиенте; аккаунты с одинаковым email снова различимы.
5. **WEB-19:** шрифты в `src/assets` — они станут хэшированными и immutable.
6. **WEB-21:** явный TS7 в скриптах `typecheck` и `build`.
7. **WEB-11 + WEB-24 + WEB-25:** удалить мёртвые функции, моки, константы, ключи i18n и `vendor-ui`.
8. **WEB-13 / WEB-29:** реже поллить projections и header accounts.
9. **WEB-1** (≈ 1 день, но самый большой эффект для пользователя): сжатие ассетов в Go, ≈ −70 % трафика первой загрузки.

## Гипотезы и что не проверено

- Стоимость `/api/dashboard/overview` и `/api/dashboard/projections` на реальной БД не замерялась (WEB-12/13). Выводы сделаны по коду (агрегация за 30 дней и N+1 `ListAccountQuota`).
- Устарелость версий зависимостей не проверялась: сеть запрещена. Все основные пакеты свежие (React 19.2.8, Vite 8.2.2, TS 7.0.2). Проблема в неиспользуемых пакетах, а не в старых.
- Цветовой контраст и работа со скринридером вживую не проверялись.
- Стабильность параллельных тестов подтверждена тремя прогонами. Для CI рекомендуется 5–10 прогонов после WEB-4.
- Ожидаемая экономия на локалях и recharts оценена по gzip -9 отдельных файлов и sourcemap-атрибуции (`tools/map-attrib.mjs`). Реальные числа после рефакторинга могут отличаться на ±10 %.
