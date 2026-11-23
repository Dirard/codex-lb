## Why

Reservations whose usage is unknown — an interrupted worker, or a request whose provider never reported usage — are flagged for reconciliation and then wait forever: production holds 75 thousand of them (since 2026-10-01, growing by roughly 500–1 500 a day, with a burst of 43 thousand on 12–13 October). Nobody reconciles them (the CLI needs the service stopped and a decision per reservation), they hold API-key budget inside their limit windows and they are listed as pending entries on every request-log page and filter query, which dominates those reads. The owner decided that not charging this usage is acceptable.

## What Changes

- The active worker's maintenance pass releases, without charging, reservations that have been flagged for reconciliation for longer than one hour, in batches of 100 (one short write job each) within a five-second budget per pass, through the ordinary release settlement: limit deltas are refunded only while their window is current, no usage event is recorded, and settled-reservation pruning later removes the rows.
- The offline `reconcile-usage` command still settles a flagged reservation within that hour.

## Capabilities

### Modified Capabilities

- `go-runtime`: handling of unreconciled usage.

## Impact

- Code: `internal/adapters/sqlite/usage_reconciliation.go` (`ReleaseUnreconciledReservations`), `cmd/codex-lb/maintenance.go`.
- Data: on the first passes after the upgrade the existing backlog (75 thousand on production) is released in about fifteen minutes; reports do not change because released reservations never had usage events.
- No schema change, no new settings.
