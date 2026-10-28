## 1. Quota history

- [x] 1.1 Coalesce history writes into five-minute buckets; keep reset/window changes as new rows.
- [x] 1.2 Read the projection history per current series (horizon plus 64-row tail) without the 100 000-row failure; regression test with 250 accounts and benchmark at 500.

## 2. Online backups

- [x] 2.1 Migration 34: backup schedule table and counter tables with triggers.
- [x] 2.2 Read-only `VACUUM INTO`, fsync, verification (quick_check, schema, key identity) and atomic publication.
- [x] 2.3 Backup service: free-space guard, single flight, rotation, last-attempt status, scheduler.
- [x] 2.4 Administrator routes with same-origin download guard; runtime wiring.
- [x] 2.5 `codex-lb backup` subcommand.
- [x] 2.6 Dashboard backup card with key warning and tests.

## 3. Retention and growth

- [x] 3.1 Budgeted batch retention loop through the batch writer.
- [x] 3.2 Quota-history default of 45 days and hourly thinning after one day.
- [x] 3.3 Settled reservation pruning that preserves fences; automation-cycle and warm-up attempt pruning.

## 4. WAL and planner

- [x] 4.1 `journal_size_limit`, maintenance checkpoints and `PRAGMA optimize`.

## 5. Reports and logs

- [x] 5.1 Migration 35: `(requested_at, request_id)` and partial reservation indexes.
- [x] 5.2 One-pass daily reports with an equivalence test against the previous implementation.
- [x] 5.3 Keyset cursor for request logs.

## 6. Correctness fixes

- [x] 6.1 Atomic automation delete, release clamp, counter-based archive/owner bounds, absolute `Open` path.

## 7. Verification

- [x] 7.1 go vet, golangci-lint, go test, race tests for touched packages, web lint/typecheck/vitest, strict OpenSpec validation; record before/after numbers in context.
