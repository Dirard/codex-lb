## Why

A long-running installation on a small VPS outgrows its SQLite file and loses its dashboard: the overview fails at about 139 accounts because quota history is written once per minute per window and read with a hard 100 000-row limit (audit DB-3). Retention is off by default and, when enabled, removes at most 1 000 rows per hour (DB-4); settled reservations are never removed (DB-5); the WAL file is never truncated and planner statistics are never refreshed (DB-8, OPS-17, DB-22). The only backup is an offline snapshot taken during managed updates (OPS-6). Reports read raw events once per day of the window (DB-6) and deep log pages sort the whole history (DB-16).

## What Changes

- Coalesce quota polling into one history row per series and five-minute bucket (reset/window changes always start a row) and read the dashboard history per current series through its index, without a global row limit.
- Add verified online backups: `VACUUM INTO` through a dedicated read-only connection, fsync, `quick_check` plus schema and key-identity verification, atomic publication in `<data-dir>/backups/`, rotation, a persisted schedule (default every 24 h, keep 3, interval 0 disables), a free-space guard, administrator API (list, create, schedule, same-origin download), `codex-lb backup` for cron, and a dashboard card. The encryption key is never included; listings say so.
- Run retention in bounded batches through the batch writer until the backlog is cleared or a 20 s budget is spent; default quota-history retention to 45 days with hourly thinning after nine days; prune settled reservations, old automation cycles and old warm-up attempts without breaking rowid fences or slot claims. Request events stay opt-in.
- Limit the WAL to 64 MiB after checkpoints, checkpoint passively every minute and truncate when idle; run `PRAGMA optimize` daily and on close.
- Compute daily report rows in one grouped pass per measure, add a keyset cursor and `(requested_at, request_id)` index for request logs, and replace the reservation status index with partial indexes.
- Fix non-atomic automation deletion, a skipped release after downward repricing, O(N) archive/owner bounds and relative `Open` paths.

## Capabilities

### Modified Capabilities

- `go-runtime`: bounded quota history, online backups, retention defaults and pruning, WAL/planner maintenance, report/log query bounds, storage bookkeeping fixes.

## Impact

- Code: `internal/adapters/sqlite` (accounts, trends, retention, backup, maintenance, reports, logs, automations, archives, owners, ledger release line, store), new `internal/adapters/backups`, `internal/application` (backups, reports), `internal/adapters/httpapi` (backups routes, report cursor), `cmd/codex-lb` (backup subcommand, scheduler and maintenance wiring), `web/src/features/settings` (backup card).
- Schema: migrations 34 (backup settings, archive/owner counters with triggers and backfill) and 35 (request-log and reservation indexes). Additive; an older binary needs the pre-upgrade backup to roll back.
- API: new `/api/backups*` routes; optional `beforeRequestedAt`/`beforeRequestId` on request logs; settings report the 45-day effective quota-history default. No new environment variables; no dependency changes.
- Behavior: existing installations start deleting quota history older than 45 days and create a first backup about five minutes after start.
