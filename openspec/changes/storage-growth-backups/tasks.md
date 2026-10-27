## 1. Quota history

- [ ] 1.1 Coalesce history writes into five-minute buckets; keep reset/window changes as new rows.
- [ ] 1.2 Read the projection history per current series (horizon plus 64-row tail) without the 100 000-row failure; regression test with 250 accounts and benchmark at 500.

## 2. Online backups

- [ ] 2.1 Migration 34: backup schedule table and counter tables with triggers.
- [ ] 2.2 Read-only `VACUUM INTO`, fsync, verification (quick_check, schema, key identity) and atomic publication.
- [ ] 2.3 Backup service: free-space guard, single flight, rotation, last-attempt status, scheduler.
- [ ] 2.4 Administrator routes with same-origin download guard; runtime wiring.
- [ ] 2.5 `codex-lb backup` subcommand.
- [ ] 2.6 Dashboard backup card with key warning and tests.

## 3. Retention and growth

- [ ] 3.1 Budgeted batch retention loop through the batch writer.
- [ ] 3.2 Quota-history default of 45 days and hourly thinning after one day.
- [ ] 3.3 Settled reservation pruning that preserves fences; automation-cycle and warm-up attempt pruning.

## 4. WAL and planner

- [ ] 4.1 `journal_size_limit`, maintenance checkpoints and `PRAGMA optimize`.

## 5. Reports and logs

- [ ] 5.1 Migration 35: `(requested_at, request_id)` and partial reservation indexes.
- [ ] 5.2 One-pass daily reports with an equivalence test against the previous implementation.
- [ ] 5.3 Keyset cursor for request logs.

## 6. Correctness fixes

- [ ] 6.1 Atomic automation delete, release clamp, counter-based archive/owner bounds, absolute `Open` path.

## 7. Verification

- [ ] 7.1 go vet, golangci-lint, go test, race tests for touched packages, web lint/typecheck/vitest, strict OpenSpec validation; record before/after numbers in context.
