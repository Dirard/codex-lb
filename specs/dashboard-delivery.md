# Dashboard delivery: compressed first load, lighter polling and a UI that matches the Go server

- Last edited with skill pack: `0.2.2`

## Title and scope

Make the first dashboard load and its steady-state polling cheap: the Go binary serves the embedded SPA precompressed (brotli/gzip) and gzips admin JSON, the default `/dashboard` route stops downloading all locales and the recharts bundle, and the status bar and projections stop re-requesting heavy reports. The dashboard is also brought in line with what the Go server actually returns: two visible bugs (tokens/s, duplicate e-mails) are fixed, UI and mocks for features Go does not serve are removed, and the frontend toolchain gets one typecheck path, parallel tests and no unused dependencies.

## Planning anchor

- Entrypoints: `internal/adapters/webui/webui.go#New` / `serveFile`, `internal/adapters/httpapi/server.go#Handler` (`mux.Handle("/api/", …)`), `web/vite.config.ts` (`keepGoEmbedPlaceholder` plugin list), `web/src/main.tsx`, `web/src/i18n/index.ts`, `web/src/components/layout/status-bar.tsx`, `web/src/features/dashboard/hooks/use-dashboard.ts`, `web/src/components/donut-chart.tsx`, `web/src/components/sparkline-chart.tsx`.
- Source: owner audit `specs/audits/2026-10-28/06-frontend.md` (WEB-1…WEB-31); measurements in `.local/audit/scratch-web/`.
- Changed assumptions:
  - The dashboard was written for the Python server, which compressed responses and returned ~25 fields Go never sends (proxy-pool routing, cost breakdown, tiers, pooled credits, roles/permissions, `isEmailDuplicate`, `outputTokensRaw`).
  - `go-runtime` excludes proxy pools and guest dashboard access, while `frontend-architecture` still requires them.
  - Go serves 1.92 MB uncompressed for the first `/dashboard` load (measured with headless Chromium against the built binary, empty database).
- Non-local: touches the Go web adapter and admin HTTP composition, the Vite build, i18n bootstrap, dashboard polling, several feature UIs, MSW mocks and OpenSpec `frontend-architecture`.
- Impacted specs in `specs/`: `specs/engineering-baseline.md` — `reuse` (its build/CI gates run the checks this plan relies on; unchanged). No other solution spec covers the dashboard.
- Impacted OpenSpec capabilities: `frontend-architecture` — `amend` through change `optimize-dashboard-delivery`.

## Connected groups or observed existing logic

1. Static delivery: `webui.New` serves `fs.FS` with `http.ServeContent`, `Cache-Control: immutable` for `/assets/*`, `no-store` for `index.html`, nothing for `/fonts/*` and `favicon.svg`; no `Content-Encoding`, no `ETag` (embed FS has no modtime).
2. Admin HTTP composition: `Server.Handler` mounts every admin route group on an inner mux behind `requireAdmin` at `/api/`; `/api/codex/` (more specific pattern) goes to the proxy; auth and key-report session routes live on the outer mux. `writeJSON` sets `Content-Type: application/json`.
3. Build: Vite 8 (rolldown) emits into `internal/adapters/webui/dist`, `emptyOutDir: true`, then `keepGoEmbedPlaceholder` restores `.gitkeep`.
4. Bootstrap: `main.tsx` imports `@/i18n`, which statically imports `en`, `ko`, `zh-CN` (≈300 KB raw, in the modulepreloaded `react-*.js` chunk).
5. Charts: `components/lazy-recharts.ts` lazily wraps recharts primitives; `DonutChart` (only `usage-donuts.tsx`) and `SparklineChart` (only `stats-grid.tsx`) use it, so `/dashboard` always fetches the 580 KB recharts chunk.
6. Polling: `status-bar.tsx` requests `/api/dashboard/overview?timeframe=7d` every 60 s on every page only to read `lastSyncAt`; `useDashboardProjections` refetches `/api/dashboard/projections` (30-day quota history + per-account quota reads) every `refreshSeconds` (15 s).
7. Request-log and account contracts: `domain.RequestLogEntry` has `outputTokens` (raw, no fallback) but no `outputTokensRaw`; `GET /api/accounts` and overview accounts have no `isEmailDuplicate`.
8. Mocks: `test/mocks/handlers.ts` contains routes Go does not register (`POST /api/accounts/:id/export`, `GET /api/automations/:id/runs`); `handler-coverage.test.ts` compares mocks with a hand-written list.
9. Toolchain: `.bin/tsc` resolves to `@typescript/native` (TS 7) only by link order; `typescript` 6 is required by `typescript-eslint`; `react-doctor`, `bun2nix`, `@playwright/test` are unused; `shadcn` is used only for `shadcn/tailwind.css`; `vitest` runs with `fileParallelism: false` (≈230 s).

## Use cases

### 1. Serve the embedded dashboard precompressed
vite build output --write .br and .gz siblings for compressible text assets--> embedded dist --negotiate Accept-Encoding per request--> [brotli variant with ETag, gzip variant with ETag, identity file with ETag]

Requirements:

  R1:
The build SHALL write `<file>.br` (brotli, max quality) and `<file>.gz` (gzip level 9) next to every emitted text asset (`.js`, `.css`, `.html`, `.svg`, `.json`, `.txt`, `.webmanifest`) of at least 1 KiB, keeping a variant only when it is smaller than the original; it SHALL use only Node's `node:zlib`.

  R2:
For `GET`/`HEAD` of a file that has variants, the server SHALL select brotli over gzip among the codings the request accepts with non-zero quality, respond with `Content-Encoding`, the original file's `Content-Type`, the variant's `Content-Length`, and `Vary: Accept-Encoding`; otherwise it SHALL serve the original bytes (still with `Vary: Accept-Encoding` when variants exist).

  R3:
A request with a `Range` header SHALL always receive the original bytes, so `206` ranges refer to the unencoded representation.

  R4:
Every served file SHALL carry a strong `ETag` derived from the bytes actually sent; `If-None-Match` SHALL produce `304`. `index.html` SHALL use `Cache-Control: no-cache`; `/assets/*` keeps `public, max-age=31536000, immutable`.

  R5:
Direct requests for `*.br` / `*.gz` variant files SHALL return `404`.

  R6:
Fonts SHALL be emitted as content-hashed `/assets/*` files so they are cached immutably.

Files And Functions:
 - existing: web/vite.config.ts#keepGoEmbedPlaceholder - plugin list gains `precompressAssets()`
 - planned: web/vite.config.ts#precompressAssets - walks `outDir` in `closeBundle`, writes `.br`/`.gz`
 - existing: internal/adapters/webui/webui.go#New - builds a per-handler index of variants and ETags at construction
 - planned: internal/adapters/webui/webui.go#negotiateEncoding - Accept-Encoding q-value parsing
 - existing: internal/adapters/webui/webui.go#serveFile - variant selection, headers
 - existing: web/src/index.css - `@font-face` URLs point at `src/assets/fonts/*`
 - planned: web/src/assets/fonts/ - moved from `web/public/fonts/`

Tests:
internal/adapters/webui/webui_test.go
 - description: brotli preferred, gzip fallback, identity without Accept-Encoding
   requirements: R2
   input: `Accept-Encoding: gzip, br`, `gzip`, `br;q=0, gzip`, none
   workflow: request --negotiate--> served representation
   expected outcome: `br`, `gzip`, `gzip`, identity; `Vary: Accept-Encoding`, original Content-Type, variant Content-Length
 - description: range requests are served unencoded
   requirements: R3
   input: `Range: bytes=0-3` with `Accept-Encoding: br`
   expected outcome: `206`, no `Content-Encoding`, `Content-Range` over original bytes
 - description: ETag revalidation and index caching
   requirements: R4
   expected outcome: second request with `If-None-Match` gets `304`; br and identity ETags differ; `index.html` has `no-cache`
 - description: variant files are not addressable
   requirements: R5
   expected outcome: `GET /assets/app.js.br` → 404

### 2. Compress admin JSON responses
admin handler response --gzip JSON bodies above 1 KiB when the client accepts gzip--> compressed or original response

Requirements:

  R7:
Responses of the authenticated admin mux mounted at `/api/` SHALL be gzip-encoded when the request accepts gzip, the response `Content-Type` is JSON, the status allows a body, no `Content-Encoding` is set and the body reaches 1 KiB; such responses SHALL carry `Vary: Accept-Encoding` and no stale `Content-Length`.

  R8:
Proxy routes (`/v1/*`, `/backend-api/*`, `/api/codex/*`), auth/key-report session routes, SSE and WebSocket traffic SHALL NOT pass through the compressing wrapper; `HEAD`, non-JSON and small responses SHALL pass through unchanged; `Flush` SHALL flush compressed bytes.

Files And Functions:
 - planned: internal/adapters/httpapi/compress.go#compressJSON - buffering gzip wrapper with pooled writers
 - existing: internal/adapters/httpapi/server.go#Handler - `mux.Handle("/api/", compressJSON(s.requireAdmin(admin)))`

Tests:
internal/adapters/httpapi/compress_test.go
 - description: large JSON is gzipped, small JSON and non-JSON are not
   requirements: R7, R8
 - description: proxy and auth routes are untouched through the real `Server.Handler`
   requirements: R8
   input: `GET /api/dashboard/overview` and `GET /api/codex/...` with `Accept-Encoding: gzip`
   expected outcome: overview gzip when large enough; proxy stub response identical, no `Content-Encoding`
 - description: `q=0`, HEAD and Flush behavior
   requirements: R7, R8

### 3. Load the default route without locale and chart bundles
entry chunk --bundle en only and import ko/zh-CN on demand--> i18n ready before first render --render dashboard with SVG donut and sparkline--> /dashboard without recharts

Requirements:

  R9:
Only the English catalogue SHALL be part of the entry chunks; `ko` and `zh-CN` SHALL load through dynamic import when detected or selected, and the first render SHALL wait until the detected language is loaded so no English flash occurs.

  R10:
The dashboard usage donuts and stat sparklines SHALL render as plain SVG without recharts, keeping the existing visual contract (stacked credits center, legend hover highlight, safe-line tick, gradient sparkline); `/dashboard` SHALL NOT request the recharts chunk.

Files And Functions:
 - existing: web/src/i18n/index.ts - `partialBundledLanguages` + lazy backend, exported `i18nReady`
 - existing: web/src/main.tsx - render after `i18nReady`
 - existing: web/src/components/donut-chart.tsx#DonutChart - SVG ring segments
 - existing: web/src/components/sparkline-chart.tsx#SparklineChart - SVG monotone area
 - existing: web/src/features/dashboard/components/usage-donuts.tsx, stats-grid.tsx - drop `lazy()`/`Suspense`

Tests:
 - description: switching to ko/zh-CN loads the bundle and resolves the language
   requirements: R9
 - description: donut renders segments, legend highlight and safe line without recharts mocks
   requirements: R10
 - description: built `/dashboard` load has no `es6-*.js` request and no ko/zh-CN strings in entry chunks
   requirements: R9, R10

### 4. Poll only what the visible surface needs
open dashboard surface --status bar reads GET /api/dashboard/status every 60 s--> cheap lastSyncAt --refetch projections only when lastSyncAt changes or 5 minutes elapse--> bounded report load

Requirements:

  R11:
`GET /api/dashboard/status` SHALL return `{"lastSyncAt": <ISO time|null>}` equal to the overview's `lastSyncAt` (latest quota observation of non-deleted ChatGPT accounts) without traffic aggregation; the status bar SHALL use it instead of the overview.

  R12:
Dashboard projections SHALL be refetched when the overview `lastSyncAt` changes, on manual refresh, or at most every 5 minutes, not on every overview poll.

Files And Functions:
 - planned: internal/adapters/httpapi/dashboard_status.go#dashboardStatus - handler and `latestQuotaSync`
 - existing: internal/adapters/httpapi/reports.go#registerReportsRoutes - registers the status route
 - planned: web/src/features/dashboard/api.ts#getDashboardStatus, schemas `DashboardStatusSchema`
 - existing: web/src/components/layout/status-bar.tsx - status query
 - existing: web/src/features/dashboard/hooks/use-dashboard.ts#useDashboardProjections - key on `lastSyncAt`, 5-minute interval

Tests:
 - description: status endpoint returns latest observation, ignores external and deleted accounts, null when none
   requirements: R11
 - description: status bar never requests the overview
   requirements: R11
 - description: projections are requested once per lastSyncAt value
   requirements: R12

### 5. Show generation speed and disambiguate duplicate e-mails
Go-shaped rows --compute tokens/s from outputTokens and count normalized real e-mails per rendered list--> (request rows with generation speed, account rows flagged with isEmailDuplicate)

Requirements:

  R13:
Generation speed SHALL be computed from Go's `outputTokens` minus `reasoningTokens` over `latencyMs - latencyFirstTokenMs`; `outputTokensRaw` SHALL be removed.

  R14:
The client SHALL derive `isEmailDuplicate` for every account list it renders (`/api/accounts`, overview accounts): `true` when another row of the same list has the same trimmed, case-insensitive real e-mail; blank e-mails and `unknown@example.com` are never duplicates.

Files And Functions:
 - existing: web/src/features/dashboard/components/recent-requests-table.tsx#formatGenerationSpeed
 - planned: web/src/features/accounts/email-duplicates.ts#markDuplicateEmails
 - existing: web/src/features/accounts/api.ts#listAccounts, web/src/features/dashboard/api.ts#getDashboardOverview - apply the helper after parsing

Tests:
 - description: Go-shaped row shows tokens/s
   requirements: R13
 - description: duplicate, case-insensitive, placeholder and unique e-mails
   requirements: R14

### 6. Remove UI for features Go does not serve
frontend schemas, components and requirements for excluded features --delete fields, branches and mocks without a Go source--> UI rendering only Go data --amend frontend-architecture through an OpenSpec change--> requirements matching go-runtime

Requirements:

  R15:
Request-log UI SHALL NOT read upstream-proxy routing, cost breakdown, requested/actual tier, upstream transport or failure-diagnostic fields; API-key UI SHALL NOT render pooled-credit bars; the auth store SHALL NOT model roles/permissions/`canWrite` or guest branches; `listAutomationRuns` and other calls to non-existent routes SHALL be removed.

  R16:
`frontend-architecture` SHALL no longer require proxy pools, guest mode, cost breakdown, service-tier pairs or pooled bars, and SHALL describe the compressed serving, the status endpoint and client-side duplicate detection.

Files And Functions:
 - existing: web/src/features/dashboard/schemas.ts, components/recent-requests-table.tsx
 - existing: web/src/features/api-keys/schemas.ts, web/src/features/apis/components/api-list-item.tsx
 - existing: web/src/features/auth/schemas.ts, hooks/use-auth.ts and `canWrite` consumers
 - existing: web/src/features/automations/api.ts, hooks/use-automations.ts
 - planned: openspec/changes/optimize-dashboard-delivery/

### 7. Keep mocks honest to the Go router
MSW handler list --match each handler against the real admin and outer muxes--> mismatch fails a Go test

Requirements:

  R17:
Every MSW handler under `/api/` SHALL resolve, through the real `http.ServeMux` routing of `Server`, to a Go pattern with the same method and path; MSW factories SHALL produce only fields Go sends.

Files And Functions:
 - existing: internal/adapters/httpapi/server.go#Handler - extract `adminRoutes()` used by `Handler` and the test
 - planned: internal/adapters/httpapi/dashboard_routes_test.go#TestDashboardMocksMatchGoRoutes
 - existing: web/src/test/mocks/handlers.ts, factories.ts, handler-coverage.test.ts

Tests:
 - description: unregistered or subtree-only mock route fails
   requirements: R17

### 8. Fast tests and one toolchain path
vitest config --parallel files after removing leaked timers--> ≈20 s suite; package.json --remove unused packages, explicit TS7 typecheck--> frozen bun install

Requirements:

  R18:
The OAuth dialog copy button SHALL cancel its pending "Copied" reset timer on unmount; vitest SHALL run test files in parallel.

  R19:
`react-doctor`, `bun2nix`, `@playwright/test` and `shadcn` SHALL be removed (the used `shadcn/tailwind.css` is vendored); `typecheck`/`build` SHALL invoke TS 7 explicitly while TS 6 remains only for `typescript-eslint`; `bun.lock` is updated with bun 1.3.14 and `npm --prefix web run install:deps` passes with `--frozen-lockfile`.

Tests:
 - description: unmounting after a copy clears the reset timer
   requirements: R18
 - description: three consecutive parallel runs and a coverage run pass without unhandled errors
   requirements: R18, R19

Files And Functions:
 - existing: web/src/features/accounts/components/oauth-dialog.tsx#CopyButton - timer held in a ref and cleared on unmount
 - existing: web/vite.config.ts - `fileParallelism`, stale excludes, `vendor-ui`
 - existing: web/package.json, web/bun.lock, web/src/index.css
 - planned: web/src/styles/shadcn-tailwind.css

## Implementation checklist

1. [x] Solution spec and OpenSpec change `optimize-dashboard-delivery` (R16).
2. [x] Build-time precompression and Go variant negotiation with ETag/Range/Vary; fonts hashed (R1–R6).
3. [x] `compressJSON` for the admin mux (R7, R8).
4. [x] Lazy locales, SVG donut and sparkline (R9, R10).
5. [x] `GET /api/dashboard/status`, status bar, projections cadence (R11, R12).
6. [x] tokens/s and client-side duplicate e-mails (R13, R14).
7. [x] Remove dead fields/UI/mocks, align MSW, Go route test (R15, R17).
8. [x] Copy-button timer, parallel vitest, dependency and TS cleanup (R18, R19).
9. [x] Measurements before/after, screenshots, full gate run.
10. [ ] After review: sync the OpenSpec delta into `openspec/specs/frontend-architecture/spec.md` and archive `optimize-dashboard-delivery`.

## Open questions

None blocking. Non-blocking assumptions are recorded in the decision log.

## Decision log

- Branch is based on `chore/engineering-baseline` (not `main`): the slice needs its `keepGoEmbedPlaceholder`, `.golangci.yml` and audit files.
- Precompressed variants instead of on-the-fly compression: zero CPU per request, brotli at max quality; gzip is kept because browsers advertise `br` only over HTTPS and codex-lb is usually reached over plain HTTP on a LAN. Binary grows by the variants (~0.6 MB).
- `index.html` switches from `no-store` to `no-cache` + ETag as `frontend-architecture` already required; it contains no secrets and revalidation returns `304`.
- Admin JSON compression wraps only the admin mux so the security-header block and proxy/auth routing stay untouched; key-report session reads are small and stay uncompressed.
- `isEmailDuplicate` is a property of the rendered list, so it is derived on the client for each list; the previous requirement's "same ChatGPT identity" condition is dropped because rows with equal visible e-mail labels are indistinguishable regardless of workspace.
- `lastSyncAt` gets a dedicated cheap endpoint rather than an extra field on `/api/accounts`, which is itself a heavy per-account read polled by the header.
- TS 7 stays the typecheck compiler (1.6 s vs 8.9 s); TS 6 cannot be removed because `typescript-eslint` needs the JS compiler API.
- bun stays the installer (`bun.lock`, `packageManager`), npm only runs scripts; replacing the lockfile is out of scope for this slice.
- The OAuth dialog keeps its own copy button (ghost style, blur after pointer activation, covered by tests) and only gains timer cleanup; switching to the shared `CopyButton` would change both behaviors.
- `additionalQuotaPolicies` (settings) is left in place: removing it changes how routing settings list per-quota overrides and needs its own decision on the Go source of quota keys.
- Measured (headless Chromium against the built binary, empty database, first `/dashboard` load): 1921 KB → 357 KB with brotli / 395 KB gzip-only; 46 → 42 requests; the recharts chunk is no longer requested. Seeded admin JSON: overview 12.9 KB → 2.6 KB, request logs 16.3 KB → 1.8 KB on the wire. Vitest 190.5 s → 20 s.
- Deferred audit items outside this slice: WEB-14 (dashboard view memoization), WEB-16 (routing-settings split), WEB-26 (model-prices i18n), WEB-28 (pre-paint theme script), WEB-29 (header accounts polling), WEB-30 (date picker), WEB-31 (unused request-log parameters).
