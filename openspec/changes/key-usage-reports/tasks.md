## 1. Reads

- [ ] 1.1 Domain: key selection, period, usage measures and report types.
- [ ] 1.2 SQLite: per-key local-day measures of the selection and previous-period totals from key slices (a UTC-day row with hour corrections, or hour rows), folded legacy hours, the journal, the unfolded tail and raw partial hours in one analytics snapshot with a fixed number of statements; raw fallback; equivalence tests with legacy rows, keys moved between groups, the ungrouped selection and Europe/Prague days across fold states.

## 2. Use case and HTTP

- [ ] 2.1 `KeyUsageReportService` with its own port: selection and period validation, totals, groups, keys, top-8 daily series and the per-day CSV rows.
- [ ] 2.2 `GET /api/reports/keys` and `GET /api/reports/keys/export.csv` with parameter validation, gzip and `Server-Timing`; CSV-sum and statement-count tests at 30 and 300 keys.

## 3. Dashboard

- [ ] 3.1 Reports page: scope picker, period picker, URL state, Copy link, Export CSV, summary with comparison, groups table, keys table grouped by group with subtotals and owner links, daily chart, lazy traffic details; removed widgets deleted.
- [ ] 3.2 Locales `en`, `ko`, `zh-CN`; MSW handlers and coverage; integration test of the report flow.

## 4. Verification

- [ ] 4.1 Gates: Go vet, lint, tests, race, vulnerability check; web lint, typecheck, tests, build; OpenSpec strict validation.
- [ ] 4.2 Contract tests for the new endpoints; `BenchmarkDashboardPages` with the report at 300 keys and 50 groups for 30 and 90 days and the 90-day CSV.
