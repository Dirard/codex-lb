## Why

The dashboard was built for a handful of accounts and keys: four views of the same quota numbers, three independent period pickers, a 15 s overview poll that recomputes traffic for the chosen period, and a header that polls `/api/accounts` on every page for one badge. At the planned scale of 300 API keys, 50 groups and up to 100 accounts the operator needs a different first page: is the pool healthy, which group needs attention, what each group spends today and which keys need an action. The usage rollups (`usage-rollups`) make per-key and per-day reads cheap enough to answer that from one request.

## What Changes

- New `GET /api/overview?timezone=` returns, from one bounded read, the pool summary (account health, 5 h and weekly remaining with projected exhaustion, spend today and at the same time yesterday, requests today, error rate in the last hour, 7-day spend trend), one row per account group plus the ungrouped keys, alerts evaluated from fixed rules, and the ten keys needing attention.
- New `GET /api/directory` lists keys, groups and accounts for the global search and pickers; new `GET /api/api-keys/{id}` returns one key for edit dialogs opened outside the keys list.
- `GET /api/dashboard/status` gains the shell status (readiness, quota sync, rollup fold time, re-login and reset-credit counts, API-key authentication and routing settings). Every page issues at most one periodic request; the overview response carries the shell status itself.
- The dashboard page is replaced by the Overview page (alerts, pool strip, groups table with expandable rows, keys needing attention with inline actions) without a period picker; the request log and conversations move to a Requests page with one period picker (1 h, 24 h default, 7 d, 30 d) and no polling unless Live is on. `/api/request-logs` accepts `timeframe=30d`; `/api/conversations` accepts `timeframe=1h` and `24h`.
- Navigation becomes Overview · Groups · Accounts · Keys · Reports · Automations · Settings; Groups and Keys open the existing group settings and APIs pages until their own pages exist. `/`, `/dashboard` and `/apis` redirect. A global search (Ctrl/Cmd+K) jumps to keys, groups, accounts and request ids.
- Page endpoints send `Server-Timing`. `GET /api/dashboard/overview` and `GET /api/dashboard/projections` are deprecated: the embedded UI stops calling them, they stay unchanged in this release and are removed no earlier than the following one. `/v1/usage`, `/api/codex/usage` and `/v1/usage/reports` are unchanged.

## Capabilities

### Modified Capabilities

- `go-runtime`: shell status, overview, alert and attention rules, directory, requests page, endpoint compatibility and the page latency budget.
- `frontend-architecture`: the dashboard widgets this change removes and the requirements that now name the Overview, Requests and Accounts pages.

## Impact

- Code: `internal/domain` (account health, window health, runway projection, alert and attention rules), `internal/application` (overview, shell status and directory use cases), `internal/adapters/sqlite` (batch entity reads, local-day key spend and error windows from the rollups with the raw fallback, directory), `internal/adapters/httpapi` (new routes, `Server-Timing`), `web/` (shell, search, overview and requests features; dashboard page and its widgets removed).
- Schema: none.
- API: additive endpoints and fields; two request-log and conversation timeframe values; deprecated `/api/dashboard/overview` and `/api/dashboard/projections` keep working for one release.
- Operations: no new settings. Alerts are evaluated per request and are never stored or sent.
