## 1. Rules

- [x] 1.1 Domain: account health state, capacity-weighted window health, account burn and runway projection, alert rules with thresholds, collapse and ordering, attention reasons and ranking; table tests at every boundary.

## 2. Reads

- [x] 2.1 SQLite: one read snapshot of accounts, quotas, credits, refusals, memberships, groups, keys and projected limits; directory; shell snapshot.
- [x] 2.2 SQLite: per-key local-day spend for 8 days, pool day totals, spend at the same time yesterday and last-hour and trailing 24 h error windows from rollup slices, total hours and error hours with journal, tail and raw edges; raw fallback reading only the last 26 hours; equivalence tests across zones and fold states.

## 3. Use cases and HTTP

- [x] 3.1 `OverviewService`, `ShellStatusService`, `DirectoryService` with their own ports; group ordering and current-membership attribution tests.
- [x] 3.2 `GET /api/overview`, `GET /api/directory`, `GET /api/api-keys/{id}`, additive `GET /api/dashboard/status`, request-log `30d` and conversation `1h`/`24h` timeframes, `Server-Timing` on page endpoints; route, authentication and statement-count tests on a 300-key, 50-group, 100-account fixture with a latency assertion.

## 4. Dashboard

- [x] 4.1 Shell status hook with one poll per page; header badge, API-key warning and status bar without own polls; navigation order and redirects; global search over the directory.
- [x] 4.2 Overview page: alerts with actions, pool strip, groups table with expandable rows and the ungrouped row, keys needing attention with Extend, Disable and Edit limits; stale and empty states.
- [x] 4.3 Requests page with the moved request-log and conversation components, one period picker, Live toggle and options fetched on demand; dashboard page and its widgets removed.
- [x] 4.4 Locales `en`, `ko`, `zh-CN`; MSW handlers and coverage; component and route tests.

## 5. Verification

- [ ] 5.1 Gates: Go build, vet, lint, tests, race; web lint, typecheck, tests, build; OpenSpec strict validation.
- [ ] 5.2 Contract tests for the new endpoints (done); production-copy latency run (needs operator approval).
- [ ] 5.3 Release notes listing the deprecation of `GET /api/dashboard/overview` and `GET /api/dashboard/projections`.
