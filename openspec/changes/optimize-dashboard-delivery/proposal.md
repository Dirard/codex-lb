## Why

The Go binary serves the dashboard and its admin API uncompressed: the first `/dashboard` load transfers about 1.9 MB, including all three locale catalogues and the 580 KB recharts bundle needed only for two small dashboard charts. The status bar re-requests the full dashboard overview every minute on every page only to read `lastSyncAt`, and projections are recomputed every 15 seconds. The SPA was written for the retired Python server and still expects fields and features Go never serves (proxy pools, guest roles, cost breakdown, service-tier pairs, pooled credit bars), so two visible features are broken (generation speed, duplicate e-mail disambiguation) and `frontend-architecture` contradicts `go-runtime`.

## What Changes

- The build writes brotli and gzip variants of text assets; the embedded UI handler negotiates `Accept-Encoding`, sets `Vary`, strong `ETag`s and keeps `Range` requests unencoded; `index.html` uses `no-cache`; fonts become hashed immutable assets.
- Authenticated admin JSON responses under `/api/` are gzip-compressed; proxy, auth, SSE and WebSocket routes are not wrapped.
- Only the English catalogue is bundled; Korean and Simplified Chinese load on demand before the first render.
- Dashboard usage donuts and stat sparklines render as SVG without recharts.
- New `GET /api/dashboard/status` returns `lastSyncAt` for the status bar; projections refresh when `lastSyncAt` changes or every 5 minutes.
- Generation speed uses Go's `outputTokens`; duplicate e-mails are detected on the client per rendered list.
- **BREAKING (UI only)**: UI, schema fields and mocks for proxy pools, guest roles/permissions, cost breakdown, requested/actual tiers, upstream transport, pooled credit bars and automation per-job runs are removed.
- MSW mocks are checked against the real Go router; the dashboard test suite runs files in parallel; unused frontend dependencies are removed and TypeScript 7 is the explicit typecheck compiler.

## Capabilities

### Modified Capabilities

- `frontend-architecture`: compressed serving, locale loading, status source, projections cadence, duplicate e-mail rule, removal of requirements for features excluded by `go-runtime`.

## Impact

`internal/adapters/webui`, `internal/adapters/httpapi` (new `compress.go`, `dashboard_status.go`, one-line wiring in `server.go`), `web/` build, i18n, dashboard, accounts, APIs, auth store, automations, mocks and `package.json`/`bun.lock`. No schema migration, no proxy-path change, no new setting.
