## Why

A key is the unit the operator manages and the unit a key owner pays for, but neither has a page that explains it. The administrator's APIs detail shows a 7-day account-cost donut, a 7-day hourly trend and lifetime totals without period labels; the owner's key report shows limits and report cards copied from admin Reports, with a free-text model filter and without latency, errors or recent requests. At hundreds of keys the operator needs one dense page per key that answers "how close is this key to its limits, what does it spend on, does it fail, and what did it just do", and the owner needs the same answers for their own key without seeing account identities. The usage rollups (`usage-rollups`) make these period reads cheap enough to serve from one request.

## What Changes

- New `GET /api/api-keys/{id}/page?start_date&end_date&timezone` (administrator) and `GET /api/key-reports/page?start_date&end_date&timezone` (key owner, report cookie) return one key page: key properties, current limits with the projected exhaustion at the last 3 hours' pace, the pool or group remaining, period KPIs with the previous-period comparison, usage by local day and model, errors by code, latency percentiles per day (TTFT p50/p95, total latency p50/p95, queue p95, tokens per second p50), user agents and the first page of recent requests; the administrator page adds the shell status, cost by account and lifetime totals.
- New `GET /api/api-keys/{id}/requests` and `GET /api/key-reports/requests` page the key's recent requests newest first through the key index with a keyset cursor (at most 100 rows), optionally errors only (at most 5 000 rows read per call).
- Owner routes bind every section to the authenticated key, accept only their documented parameters, omit account identities, client IPs, full user-agent strings, conversation ids, error messages, cost by account, lifetime totals and assigned account ids, show the pool or group remaining only when upstream quotas are visible to the key, keep the key-report session rules and are gzip-compressed.
- The dashboard gets one `KeyPage` component with an administrator and an owner mode: `/keys/:id` for the administrator (with Edit, limits, move, enable/disable, regenerate, group reset, delete and a copyable owner link) and the key-report surface after key sign-in. One period picker (Today, 7 d, 30 d, 90 d, custom up to 730 days) is kept in `?from=&to=`; the page polls every 60 s only while the period includes today. Key sign-in keeps `from` and `to`, so an owner link opens the owner page for that period; the link carries no credential.
- Key links from search, the overview and the APIs list open `/keys/:id`; `/keys?selected=<id>` and `/apis?selected=<id>` redirect there. The APIs page detail pane (account-cost donut, hourly trend chart, unlabelled 7-day figures) is removed; the list stays at `/keys` until the Keys page replaces it.
- Deprecated, unchanged for one release: `GET /api/api-keys/{id}/trends`, `GET /api/api-keys/{id}/usage-7d` and `GET /api/key-reports/reports`. `/v1/usage`, `/api/codex/usage` and `/v1/usage/reports` are unchanged.

## Capabilities

### Modified Capabilities

- `go-runtime`: key page reads and owner scope, recent-request paging by key, owner routes, owner links and deprecations.
- `frontend-architecture`: the APIs-tab widgets this change removes, the trend-axis requirement that loses the API trend chart, and compression of the owner page reads.

## Impact

- Code: `internal/domain` (key page contracts), `internal/application` (`KeyPageService` with admin and owner scopes, limit pace projection), `internal/adapters/sqlite` (period usage, error codes and limit pace from rollups with the raw fallback; recent requests by key), `internal/adapters/httpapi` (four routes, parameter validation, owner projection, compression, `Server-Timing`), `web/` (`features/key-page`, key-report wrapper, auth gate, routes and links, APIs detail removed).
- Schema: none.
- API: additive endpoints; three deprecated endpoints keep working for one release.
- Operations: no new settings.
