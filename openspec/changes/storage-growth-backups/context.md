# Storage growth, report speed and online backups: context

- Last edited with skill pack: `0.2.2`

Normative requirements are in [specs/go-runtime/spec.md](specs/go-runtime/spec.md);
the implementation plan is `specs/storage-growth-backups.md`. Source findings:
owner audit 2026-10-28, `02-sqlite.md` (DB-3…DB-6, DB-8, DB-16…DB-23) and
`05-ops-runtime.md` (OPS-6, OPS-17).

## Purpose

The production host is a 6 vCPU / 4 GB VPS with a small disk that must run for
years without manual cleanup. Before this change the database grew without
bound (quota polling alone added ≈343 KB per account per day), the dashboard
overview failed above ≈139 accounts, and the only backup was taken offline
during managed updates.

## Decisions

- **Quota history is a bucketed time series.** One row per account window and
  five-minute bucket; the latest row is updated while the bucket is current.
  A reset or window-length change always inserts, so resets are never merged.
  Idle polled accounts still produce one row every five minutes, which keeps
  "zero burn" distinguishable from "no samples". Growth drops from 2 880 to
  576 rows per account per day (two windows).
- **Per-series read.** The projection history is read per current window via
  the `(account_id, window, observed_at DESC, id DESC)` index. Rows per series
  are bounded by the six-hour horizon (72 buckets) plus the 64-row tail, so the
  response grows with the number of series instead of failing at 100 000 rows.
- **Backups default on.** Every 24 h, keep 3, first check five minutes after
  start. A same-disk copy does not survive disk loss, but it covers corruption,
  operator mistakes and failed upgrades of unmanaged installations. The guard
  refuses a snapshot that would leave less than 5 % of the filesystem free
  (at least 256 MiB, at most 2 GiB), so a
  backup can never starve SQLite of disk. Interval 0 disables the schedule.
- **The key is not in the backup.** `encryption.key` does not change after
  installation; store it once off-host (password manager, offline medium).
  A leaked snapshot alone exposes only encrypted credentials.
- **Request events are not deleted by default.** They are the per-request
  evidence behind key ledgers and reconciliation. Quota history (operational
  telemetry) gets a 45-day default and hourly thinning after one day; every
  consumer reads at most 30 days.
- **Reservation rowids are fences.** Account outcomes, continuations and
  affinity versions compare `usage_reservations` rowids. Pruning keeps the
  largest rowid (SQLite assigns max+1) and every referenced row. `VACUUM INTO`
  preserves rowids (transfer optimization); a test guards this.

## Failure modes

- Backup refused for space: the card shows `insufficient_space`; free space or
  lower `keep`. Existing backups are untouched.
- Backup verification failed: the temporary file is removed; check the log
  (`backup failed`, error code) and the database with `PRAGMA quick_check` on a
  copy.
- WAL truncation busy: skipped until the next minute; a long report or backup
  holds a read snapshot.
- Retention budget exhausted: the next hourly cycle continues; the log line
  `retention cycle` reports per-kind counts.

## Backup runbook

Scheduled backups land in `<data-dir>/backups/codex-lb-YYYYMMDDTHHMMSSZ.sqlite3`
(mode 0600). Settings → Backups shows them, the last attempt, and database/WAL
and free-disk sizes; "Back up now" creates one and each row downloads one.

Off-host copy with cron while the service runs (same user as the service):

```sh
# /etc/cron.d/codex-lb-backup
15 3 * * * codex-lb /usr/local/bin/codex-lb backup --data-dir /var/lib/codex-lb --output /srv/backup/codex-lb >> /var/log/codex-lb-backup.log 2>&1
```

The command prints `{"path":"/srv/backup/codex-lb/codex-lb-20261028T031500Z.sqlite3","sizeBytes":…}`.
Copy that file off the host (for example `rsync` or `restic`). Copy
`/var/lib/codex-lb/encryption.key` once to separate secure storage.

## Restore runbook

1. Stop the service: `systemctl stop codex-lb`.
2. Keep the damaged state: `mv /var/lib/codex-lb /var/lib/codex-lb.broken`.
3. Create a new private directory and restore the pair:
   ```sh
   install -d -m 0700 -o codex-lb -g codex-lb /var/lib/codex-lb
   install -m 0600 -o codex-lb -g codex-lb codex-lb-20261028T031500Z.sqlite3 /var/lib/codex-lb/codex-lb.sqlite3
   install -m 0600 -o codex-lb -g codex-lb /secure/encryption.key /var/lib/codex-lb/encryption.key
   ```
   Do not copy `-wal`/`-shm` files: a snapshot is self-contained.
4. Start once without self-update so the restored binary and schema match:
   `codex-lb serve --data-dir /var/lib/codex-lb --self-update=false` (or the unit
   with that flag), then check `curl -fsS http://127.0.0.1:2455/health/ready`.
   Readiness stays false and the log names the cause when the key does not
   match the snapshot.
5. Reservations that were in flight at snapshot time are marked for
   reconciliation at startup; list them with
   `codex-lb reconcile-usage --data-dir /var/lib/codex-lb` (service stopped).
6. Re-enable the normal unit. A snapshot from a newer binary (higher schema
   version) is refused by an older binary; restore with the newer one.

Example: a backup taken at 03:15 and restored at 09:00 loses six hours of
request events and settlements; reserved budgets of requests in flight at
03:15 are held pending reconciliation rather than silently released.

## Measurements

Synthetic installation (`BenchmarkStorageGrowth`): 500 accounts, 50 keys,
300 000 request events over 30 days, one day of legacy one-minute quota
history (1.44 M rows), i9-14900K, NVMe.

| Operation | Before | After |
|---|---|---|
| RecentAccountQuotaHistory, 500 accounts | error (>100 000 rows) | see tasks 7.1 |
| DashboardOverview 7d, 500 accounts | error | see tasks 7.1 |
| QueryReports 7d / 30d | 0.85 s / 3.10 s | see tasks 7.1 |
| ListConversations | 1.39 s | unchanged |
| Request logs offset 0 / 250 000 | 1.6 ms / 146 ms | see tasks 7.1 |
| Request-log options | 201 ms | unchanged |
