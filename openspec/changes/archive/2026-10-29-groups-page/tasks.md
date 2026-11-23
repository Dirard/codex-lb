## 1. Reads

- [x] 1.1 Domain: groups summary, summary row, limit template, group key and account types; summary usage query with the 30-day span.
- [x] 1.2 SQLite: entity snapshot of the overview with the group limit templates in one statement; overview usage plus the per-key span before its first day in one analytics snapshot (UTC-day key slices, hour rows at the edges, folded legacy hours, raw split hours, tail and journal); raw fallback with an unknown span; equivalence tests in UTC, Europe/Prague and Asia/Kolkata across fold states.

## 2. Use case and HTTP

- [x] 2.1 `GroupsSummaryService` reusing the overview assembly: rows equal the overview rows for the same snapshot, 30-day spend, templates, members, top keys by 7-day spend, pool row and accounts.
- [x] 2.2 `GET /api/groups/summary` with gzip and `Server-Timing`; route, authentication, statement-count and latency tests.

## 3. Dashboard

- [x] 3.1 Groups page: toolbar, pool row, pool runway card, sortable groups table, detail sheet with members, limits, keys, Reset period and Delete; poll every 60 s with the draft kept; `/groups?selected=` and the Settings redirect.
- [x] 3.2 Settings without the API key and account-group sections and without the refresh-rate and burn-projection appearance settings; API-key access section with both toggles.
- [x] 3.3 Locales `en`, `ko`, `zh-CN`; MSW handlers and coverage; integration test of the groups flow.

## 4. Verification

- [x] 4.1 Gates: Go vet, lint, tests, race, vulnerability check; web lint, typecheck, tests, build; OpenSpec strict validation.
- [x] 4.2 Contract tests for the new endpoint; `BenchmarkDashboardPages` with the groups summary at 50 groups, 300 keys and 100 accounts.
