## Why

The owner audit of 2026-10-28 measured that time-to-first-token and the gap between the upstream terminal and the client terminal are dominated by synchronous durable commits, not by CPU: a Responses turn commits twice before dispatch and four to five times after the upstream terminal, each a WAL fsync under `synchronous=FULL`, and continuation writes bypass the batch writer. The batch writer can fail every queued reservation and settlement when another transaction holds the single writer connection for more than five seconds, and a model price change reprices the whole history in one transaction. Two ownership bugs sit on the same path: a provider-side `conversation` is stripped during quota failover so another account answers without context, and compact quota replay loses its Responses Lite flag.

## What Changes

- Reservation and the soft-affinity compare-and-set are one durable write before dispatch; an affinity write failure leaves no reservation and fails the attempt before dispatch.
- Settlement, the account outcome, the continuation and its aliases are one durable write after the upstream terminal, with failure isolation so ownership persistence can never undo settlement; the buffered terminal is emitted only after that commit.
- Every runtime SQLite mutation uses the batch writer; its five-second deadline covers execution, not waiting for the connection.
- Model history repricing runs in bounded chunks after a read-only provenance check, records a pending marker and resumes after restart.
- A non-empty Responses `conversation` makes the request account-bound: no cross-account replay and a pinned continuation.
- Compact quota replay keeps the Lite flag of the replayed input.
- Conversation aliases reference the owner's stored context; continuation bounds are tracked incrementally.
- Dashboard analytics use a separate small read pool.

## Capabilities

### Modified Capabilities

- `go-runtime`: soft-affinity persistence, model repricing atomicity, terminal durability, provider-state ownership, writer scheduling, alias storage and analytics read isolation.

## Impact

`internal/application` (proxy, selection, ownership, compact), `internal/domain` (reservation, finalization types), `internal/adapters/sqlite` (writer, ledger, affinity, continuation, pricing, store) and schema version 34 (`continuations.context_response_id`, `continuation_stats` with triggers, `model_reprice_pending`). Downgrade after schema 34 is restore-from-backup. Wire protocol and dashboard API are unchanged.
