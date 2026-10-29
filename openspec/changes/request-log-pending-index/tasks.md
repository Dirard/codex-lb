## 1. Schema and reads

- [x] 1.1 Migration 43: partial covering index `idx_usage_reservations_pending` on flagged reserved reservations; additive.
- [x] 1.2 Pending reader: filter split into reservation-row and (account, generation, key, model) group conditions; facets by group presence; recorded reservations found once per request; totals and the first `offset + limit + 1` page candidates from the index.
- [x] 1.3 Request-log page, totals and options (rollup and raw paths) read pending entries through it; conversation filters skip them.

## 2. Verification

- [x] 2.1 Equivalence with the previous implementation (`reports_logs_pending_reference_test.go`) for every filter, offset and keyset walks, with no, few and many recorded reservations; query-plan test; migration test beside a schema 42 writer.
- [x] 2.2 `BenchmarkRequestLogPending`: 74 k pending among 1.6 M reservations and 742 k events, before and after, warm and cold, and the index build.
- [ ] 2.3 Latency run on a copy of production data.
- [x] 2.4 Gates: build, vet, lint, tests, race.
