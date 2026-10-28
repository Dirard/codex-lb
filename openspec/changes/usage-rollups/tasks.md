## 1. Shared rules

- [x] 1.1 Histogram codec with merge, subtract and median; raw path uses histogram medians and integer-microdollar costs.

## 2. Schema and folder

- [ ] 2.1 Migration 42: rollup tables, state, journal and update/delete triggers; additive.
- [ ] 2.2 Folder with cursor, journal draining, adaptive batches, rebuild and verifier; runs on the active worker only.
- [ ] 2.3 Fold, crash, late-event, rowid-reuse, rebuild and two-store (old writer) tests.

## 3. Read paths

- [ ] 3.1 Window planner and snapshot sources (rollups, journal, tail, edges, split hours).
- [ ] 3.2 Rollup readers for dashboard traffic, reports, key reports, key usage and trends, request-log options and totals, conversations list and details.
- [ ] 3.3 Randomized equivalence tests rollup versus raw across filters, time zones and fold states.

## 4. Verification

- [ ] 4.1 Backfill and read benchmarks; latency run on a copy of production data.
- [ ] 4.2 Gates and strict OpenSpec validation.
