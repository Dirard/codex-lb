# Storage growth, report speed and online backups

- Last edited with skill pack: `0.2.2`

## Title and scope

Keep a long-running codex-lb SQLite installation small, fast and recoverable: the dashboard overview keeps working at 500+ accounts, operational history is retained by a bounded policy that keeps up with its write rate, WAL and planner statistics are maintained automatically, and the operator gets verified online backups with rotation, a CLI for cron and a dashboard card. Financial request events are never deleted by default; quick correctness fixes (atomic automation delete, release after repricing, O(1) archive bounds, relative `Open` paths) ride along.

## Planning anchor

- Entrypoints:
  - `internal/application/reports_dashboard.go#DashboardOverview` and `reports_projections.go#DashboardProjections` → `internal/adapters/sqlite/reports_account_trends.go#RecentAccountQuotaHistory` (fails above 100 000 rows, audit DB-3).
  - `internal/adapters/sqlite/accounts.go#saveAccountQuotaTx` (one history row per poll and window).
  - `cmd/codex-lb/maintenance.go#maintainReports` / `#maintainReportRetention` (one 1000-row batch per hour, DB-4).
  - `cmd/codex-lb/update_backup.go#Backup` (only backup: offline, managed updates only, OPS-6).
  - `internal/adapters/sqlite/store.go#Open`, `#sqliteDSN`, `#Close` (DB-8, DB-22, DB-23).
  - `internal/adapters/sqlite/reports_aggregate.go#QueryReports`, `reports_logs.go#ListRequestLogs` (DB-6, DB-16), `ledger.go` release (DB-21), `automations.go#DeleteAutomationJob` (DB-20), `error_archive.go#SaveErrorArchive`, `codex_resource_owners.go#SaveCodexResourceOwner` (DB-19).
- Changed assumptions:
  - Quota polling is a time series consumed only through a six-hour pace horizon, a 64-observation EWMA tail, hourly trend buckets (≤ 8 days) and seven-day positive deltas; one-minute raw resolution is not required by any consumer.
  - Retention defaults: operational history has a bounded default; request events (financial detail) stay opt-in.
  - SQLite in WAL mode can produce a consistent online snapshot through an independent read-only connection without stopping the service; `VACUUM INTO` preserves implicit rowids used by reservation fences (verified on SQLite 3.53.4, covered by a regression test).
- Non-local: storage adapter, application report/backup use cases, HTTP admin API, CLI, runtime background loops, dashboard settings, OpenSpec `go-runtime`.
- Impacted specs in `specs/`: none besides this one (`specs/spec-lifecycle/` is lifecycle policy). Action: none.
- Impacted OpenSpec capabilities: `go-runtime` — action `amend` through change `openspec/changes/storage-growth-backups/`. Legacy `data-retention` capability describes the retired Python runtime (opt-in for both windows) and is not normative for Go; it is left unchanged.

## Connected groups or observed existing logic

1. Entry and orchestration
   - `cmd/codex-lb/main.go#run` dispatches `serve`, `import-legacy`, `reconcile-usage`, `version`, `update-info`; `cmd/codex-lb/config.go#parseConfig` owns flags.
   - `cmd/codex-lb/runtime.go#serveListener` starts four background loops (`maintainReports`, usage poller, catalog, automations) and joins them on drain.
   - `cmd/codex-lb/lock_unix.go#lockDatabase` holds an exclusive `flock` on the database inode for the lifetime of `serve`/`reconcile-usage`.
2. Persistence
   - `store.go#Open`: one writer connection (FULL, WAL), eight `_query_only` readers; `writer.go#batchWriter` groups `s.write` jobs under a 5 s batch deadline (owned by the hot-path slice; reused, not modified).
   - `account_quota_history` with index `(account_id,window,observed_at DESC,id DESC)`; `account_quotas` holds the current window set.
   - `reports_retention.go#FoldAndPruneRequestLogs` folds then deletes events (direct transaction, ≤ 1000 rows); `usage_reservations` / `usage_reservation_items` are never deleted; `usage_events.reservation_id` references reservations (FK, no cascade); `account_outcomes.reservation_rowid`, `continuations.reservation_rowid` and affinity versions compare implicit reservation rowids.
   - `error_archive.go#SaveErrorArchive` evaluates a window function over all archives per insert; `codex_resource_owners.go` counts all owners per insert.
   - `automations.go#DeleteAutomationJob`: two autocommits, second error ignored.
   - `ledger.go` release applies a negative limit delta only while `current_value+delta>=0`.
3. Response and propagation
   - `internal/adapters/httpapi/server.go#Handler`: `/api/` routes run behind `requireAdmin`; `http.CrossOriginProtection` rejects cross-origin unsafe methods; `/api/*` responses are `no-store`.
   - `internal/adapters/httpapi/admin.go#getSettings` exposes effective `requestLogRetentionDays` / `usageHistoryRetentionDays` (0 when the stored override is NULL).
4. Frontend consumers
   - `web/src/features/settings/components/settings-page.tsx` composes cards; `data-retention-settings.tsx` shows the effective value as the inherited hint.
5. Validation
   - `store.go#migrate` refuses foreign/newer schemas; `runtime_identity.go#ValidateRuntime` proves the key matches the database and decrypts every stored credential.
6. Specs
   - `openspec/specs/go-runtime/spec.md` (weekly pace from persisted observations, grouped writes, error archive policy) remains authoritative and is amended by the change.

## Use cases

### 1. Bounded quota history for the dashboard
poll observation --coalesce into the series' current five-minute bucket--> bounded history --read horizon and tail per current series through the identity index--> projection input --weekly pace and depletion--> overview response

Requirements:

  R1:
A new history row SHALL be inserted only when the series has no row, the observation falls into a later five-minute UTC bucket than the latest row, the observation is older than the latest row, or `reset_at`/`window_minutes` differ from the latest row; otherwise the latest row SHALL be updated in place to the newer observation. `account_quotas` semantics are unchanged.

  R2:
`RecentAccountQuotaHistory` SHALL return, for every current `(account, window)` in `account_quotas`, every retained observation inside the six-hour horizon plus the 64 newest observations in the requested lookback, ordered by account, window, observation time and id, without a global row limit or failure; `PositiveQuotaDeltas` SHALL read only current windows that can be the weekly pace window. Both SHALL walk each series in index order through a covering series index.

  R3:
The dashboard overview and projections SHALL complete for 200 or more accounts with a day of legacy one-minute history, and the history read for 500 accounts SHALL stay bounded by the number of current series.

Files And Functions:
 - existing: internal/adapters/sqlite/accounts.go#saveAccountQuotaTx - bucket coalescing
 - existing: internal/adapters/sqlite/reports_account_trends.go#RecentAccountQuotaHistory - per-series bounded read
 - existing: internal/application/reports_projections.go#DashboardProjections - unchanged consumer
 - existing: internal/application/weekly_pace.go#BuildWeeklyCreditPace - unchanged consumer

Data:
quota history constants:
- bucket: 5 minutes (aligned to UTC epoch)
- pace horizon: 6 hours (unchanged)
- EWMA tail: 64 observations (unchanged)

Tests:
internal/adapters/sqlite/account_quota_history_test.go
 - description: polling every minute keeps one row per five-minute bucket and preserves reset changes
   requirements: R1
   input: 30 observations one minute apart, one reset in the middle
   workflow: observations --SaveAccountQuota--> history rows
   expected outcome: ≤ 7 rows plus one extra row for the reset; latest row equals latest observation
 - description: overview at 250 accounts with legacy dense history does not fail
   requirements: R2, R3
   input: 250 accounts × 2 windows × 1440 one-minute rows
   workflow: seeded store --DashboardOverview and DashboardProjections--> response
   expected outcome: no error; every series contributes at most horizon rows plus 64

### 2. Verified online backups
backup trigger (schedule, admin request or CLI) --free-space guard--> admitted backup --VACUUM INTO a temporary file through a dedicated read-only connection--> raw snapshot --fsync and verify (quick_check, schema, key identity)--> verified snapshot --atomic rename and directory fsync--> published backup --rotate to the newest N--> retained backups

Requirements:

  R4:
A snapshot SHALL be produced from a live database without stopping request handling, through a connection that cannot modify the source, into a temporary file in the destination directory; the published name SHALL appear only after the file and its directory entry are durable.

  R5:
A published snapshot SHALL pass `PRAGMA quick_check`, carry this application's id and a supported schema version, and decrypt its runtime identity probe and every stored credential with the installation key; otherwise the temporary file SHALL be removed and the attempt reported as failed.

  R6:
The encryption key SHALL NOT be written into the backups directory or any downloadable snapshot; every backup listing SHALL state that the key is excluded and must be stored separately.

  R7:
Only one backup SHALL run at a time per process; a concurrent request SHALL be rejected as busy. A backup SHALL be refused when the estimated snapshot would leave less than 5 % of the filesystem free, bounded to at least 256 MiB and at most 2 GiB.

Files And Functions:
 - planned: internal/adapters/sqlite/backup.go#WriteSnapshot - read-only VACUUM INTO, fsync, verify
 - planned: internal/adapters/sqlite/backup.go#VerifySnapshot - quick_check, schema, identity
 - existing: internal/adapters/sqlite/runtime_identity.go#ValidateRuntime - split into a read-only identity check reused by verification
 - planned: internal/adapters/backups/directory.go#Directory - list, temp, publish, open, remove, free space
 - planned: internal/application/backups.go#BackupService.Create - guard, single flight, rotation

Tests:
 - description: snapshot under concurrent writes is consistent and verified
   requirements: R4, R5
   input: writer goroutine inserting reservations while a snapshot runs
   workflow: live store --WriteSnapshot--> snapshot --open as runtime with same key--> ValidateRuntime ok
   expected outcome: quick_check ok, rowids of usage_reservations preserved, no temp files left
 - description: verification rejects a different key and a corrupted file
   requirements: R5
   input: snapshot verified with a foreign key; truncated file
   expected outcome: error; nothing published
 - description: concurrent and low-space backups are refused
   requirements: R7
   expected outcome: busy error for the second call; insufficient-space error without a file

### 2.1 Scheduled backups and rotation
runtime start --wait five minutes--> scheduler --every ten minutes compare the newest backup age with the saved interval--> [not due, due --Create--> rotated backups]

Requirements:

  R8:
The schedule SHALL default to every 24 hours keeping 3 backups; an interval of 0 SHALL disable scheduled backups without deleting existing files. The interval (0–168 hours) and keep count (1–30) SHALL persist in SQLite and apply without restart.

  R9:
After a successful backup, files beyond the newest `keep` backups SHALL be removed; failed or skipped attempts SHALL NOT remove any backup. The outcome of the last attempt (time, trigger, success or error code) SHALL be visible to the administrator.

Files And Functions:
 - planned: internal/application/backups.go#BackupService.RunScheduler
 - planned: internal/adapters/sqlite/backup_settings.go#LoadBackupSchedule, #SaveBackupSchedule
 - existing: cmd/codex-lb/runtime.go#serveListener - registers the scheduler loop

Tests:
 - description: due schedule creates and rotates; disabled schedule does nothing
   requirements: R8, R9
   input: fake clock, fake directory with four old backups, keep 3

### 2.2 Administrator API and download
admin request --requireAdmin and same-origin guard--> backup handler --[list, create, update schedule, download]--> JSON or attachment

Requirements:

  R10:
`GET /api/backups`, `POST /api/backups`, `PUT /api/backups/schedule` and `GET /api/backups/{name}` SHALL require an authenticated dashboard administrator; unsafe methods SHALL keep cross-origin protection and the download SHALL reject requests whose `Sec-Fetch-Site` is present and not `same-origin` or `none`.

  R11:
The download SHALL accept only names matching the backup pattern, stream the file as an attachment with `Cache-Control: no-store`, and never serve paths outside the backups directory.

Files And Functions:
 - planned: internal/adapters/httpapi/backups.go#registerBackupRoutes
 - existing: internal/adapters/httpapi/server.go#Handler - one registration line

Tests:
 - description: unauthenticated, cross-site and traversal requests are rejected; admin download streams the snapshot
   requirements: R10, R11

### 2.3 CLI snapshot for cron
`codex-lb backup --data-dir D --output P` --open D's database read-only without the service lock--> snapshot --verify with D's key--> P (file, or standard name inside directory P) --print JSON--> exit 0

Requirements:

  R12:
The command SHALL work while the service is running or stopped, SHALL NOT take the service lock or migrate the database, SHALL write atomically and refuse an existing target file, and SHALL require absolute paths.

Files And Functions:
 - planned: cmd/codex-lb/backup.go#backupDatabase
 - existing: cmd/codex-lb/main.go#run, cmd/codex-lb/config.go#parseConfig - subcommand registration and `--output`

Tests:
 - description: CLI snapshot of a running runtime verifies and opens
   requirements: R12

### 2.4 Dashboard backup card
settings page --load backup status--> card (schedule form, last result, list with download links, create button, key warning)

Requirements:

  R13:
The card SHALL show the key-exclusion warning, the backups with size and time, the last attempt result, database/WAL/free-space sizes, and SHALL disable mutations without write permission.

Files And Functions:
 - planned: web/src/features/settings/backups.ts - schemas and API client
 - planned: web/src/features/settings/components/backup-settings.tsx - card
 - existing: web/src/features/settings/components/settings-page.tsx - one card insertion
 - existing: web/src/i18n/locales/*.json - strings

Tests:
 - description: card renders list and warning, creates a backup and saves the schedule
   requirements: R13

### 3. Retention that keeps up with growth
hourly retention cycle --repeat bounded batches through the batch writer until a batch is short or 20 s elapse--> [request events (opt-in), quota history (default 45 days, thinned to hourly after one day), settled reservations, automation runs, warm-up attempts, affinities] --log a short summary--> next cycle

Requirements:

  R14:
Each retention kind SHALL delete in batches of at most 500 rows per `s.write` job and continue until a batch returns fewer rows or the cycle budget is spent, so a backlog of 100 000 rows clears in one cycle.

  R15:
When no override is stored, quota history SHALL be retained for 45 days; an explicit 0 disables it. Request events SHALL remain kept indefinitely unless the operator sets request-log retention.

  R16:
Quota history older than one day SHALL be thinned to the newest observation per series, UTC hour, reset time and window length; the newest observation of every series SHALL never be deleted.

  R17:
A reservation SHALL be deleted only when it is settled, last updated more than seven days ago, not referenced by a usage event, an account outcome or a continuation, and not the reservation with the largest rowid; reservations of pruned request events SHALL be deleted under the same conditions in the same transaction.

  R18:
Finished automation runs SHALL be kept for the newest 100 cycles of each job and pending/running runs and the job's latest cycle SHALL always be kept; limit warm-up attempts SHALL be kept for the newest 100 per account and only attempts whose reset time is more than eight days old may be deleted, never the newest attempt of an account.

Files And Functions:
 - existing: cmd/codex-lb/maintenance.go#maintainReportRetention - loop with budget
 - existing: internal/application/reports.go#PruneRequestLogs, #PruneAccountQuotaHistory - effective defaults, batch size
 - existing: internal/adapters/sqlite/reports_retention.go#FoldAndPruneRequestLogs - batch writer, reservation cleanup
 - existing: internal/adapters/sqlite/reports_account_trends.go#PruneAccountQuotaHistory - batch writer
 - planned: internal/adapters/sqlite/retention.go#ThinAccountQuotaHistory, #PruneSettledReservations, #PruneAutomationRuns, #PruneLimitWarmupAttempts
 - existing: internal/adapters/httpapi/admin.go#getSettings - effective default

Data:
retention defaults:
| kind | default | floor | batch |
|---|---|---|---|
| request events | disabled | 30 days | 500 |
| quota history | 45 days | 45 days | 500 |
| quota history full resolution | 1 day | — | 500 |
| settled reservations | 7 days | — | 500 |
| automation cycles per job | 100 | — | 500 |
| warm-up attempts per account | 100 (reset > 8 days ago) | — | 500 |

Tests:
 - description: 100 000 expired rows clear in one cycle
   requirements: R14
 - description: reservation pruning keeps fences monotonic and referenced rows
   requirements: R17
   expected outcome: max rowid, outcome- and continuation-referenced rows survive; a new reservation gets a larger rowid than every deleted one
 - description: automation and warm-up pruning never re-enables a slot
   requirements: R18

### 4. WAL and planner maintenance
writer open --journal_size_limit 64 MiB--> live WAL --every minute PASSIVE checkpoint on a maintenance connection--> [backlog, idle --TRUNCATE with 1 s busy timeout--> empty WAL] --daily and on Close PRAGMA optimize with analysis_limit--> fresh statistics

Requirements:

  R19:
The writer SHALL set `journal_size_limit=67108864`. A dedicated maintenance connection SHALL run `wal_checkpoint(PASSIVE)` every minute and `wal_checkpoint(TRUNCATE)` with a 1 s busy timeout when PASSIVE reports no remaining frames and the WAL file is not empty; a busy result SHALL be skipped, not retried in a loop.

  R20:
`PRAGMA optimize` with `analysis_limit=400` SHALL run through the batch writer once a day and on `Close`; failure SHALL not prevent closing.

Files And Functions:
 - existing: internal/adapters/sqlite/store.go#sqliteDSN, #Open, #Close
 - planned: internal/adapters/sqlite/maintenance.go#CheckpointWAL, #Optimize, #StorageUsage
 - existing: cmd/codex-lb/maintenance.go#maintainReports - schedule

Tests:
 - description: WAL above the limit shrinks after checkpoint once readers finish
   requirements: R19

### 5. Report and log query speed
report filter --one pass per measure grouped by local day (DST-aware day expression), raw events and hourly rollups aggregated separately and merged--> identical report --keyset or indexed offset page--> request logs

Requirements:

  R21:
`QueryReports` SHALL return the same response as before for every filter, time zone and window, using a bounded number of statements independent of the number of days.

  R22:
Request-log pages SHALL be ordered through an index on `(requested_at, request_id)` and SHALL accept an optional keyset cursor (`beforeRequestedAt`, `beforeRequestId`) that returns the rows strictly after the cursor in the existing order; offset paging remains supported.

  R23:
Open reservations SHALL be indexed by a partial index on `(needs_reconciliation, id) WHERE status='reserved'` and settled releases by `(updated_at) WHERE status='released'`, replacing `idx_usage_reservations_status_age`.

Files And Functions:
 - existing: internal/adapters/sqlite/reports_aggregate.go#QueryReports
 - existing: internal/adapters/sqlite/reports_logs.go#ListRequestLogs, #requestLogWhere
 - existing: internal/application/reports.go - cursor parameters
 - existing: internal/adapters/httpapi/reports.go - cursor query parameters
 - existing: internal/adapters/sqlite/migrations.go - schemaV35 indexes

Tests:
 - description: new QueryReports equals the previous implementation on mixed raw/rollup data across UTC, DST and half-hour zones
   requirements: R21
 - description: keyset page equals the corresponding offset page
   requirements: R22
 - description: EXPLAIN uses the partial index without a temporary B-tree
   requirements: R23

### 6. Storage correctness fixes
small defects --fix in place--> atomic, bounded and correct storage operations

Requirements:

  R24:
Deleting an automation job SHALL remove the job and its runs in one transaction and return any error.

  R25:
Releasing or settling below a reservation after repricing reduced the limit SHALL clamp the limit at zero instead of skipping the update.

  R26:
Error-archive and resource-owner bounds SHALL be enforced from counters maintained by triggers in O(evicted rows), with the same eviction order as before.

  R27:
`Open` SHALL resolve a relative filesystem path to an absolute path.

Files And Functions:
 - existing: internal/adapters/sqlite/automations.go#DeleteAutomationJob
 - existing: internal/adapters/sqlite/ledger.go - release delta (minimal change)
 - existing: internal/adapters/sqlite/error_archive.go#SaveErrorArchive
 - existing: internal/adapters/sqlite/codex_resource_owners.go#SaveCodexResourceOwner
 - existing: internal/adapters/sqlite/store.go#Open

Tables:
backup_settings
 - id: INTEGER # always 1
 - interval_hours: INTEGER # 0..168, default 24
 - keep_count: INTEGER # 1..30, default 3
error_archive_stats
 - id: INTEGER # always 1
 - archive_count: INTEGER
 - archive_bytes: INTEGER
codex_resource_owner_stats
 - id: INTEGER # always 1
 - owner_count: INTEGER

Tests:
 - description: automation delete is atomic; release after a downward reprice reaches zero; archive eviction equals the previous window-function result
   requirements: R24, R25, R26
 - description: relative path opens
   requirements: R27

## Implementation checklist

1. [ ] OpenSpec change `storage-growth-backups` (proposal, tasks, context with backup/restore runbook, go-runtime delta); strict validation.
2. [ ] Quota history bucket coalescing and bounded read; remove the 100 000-row failure; regression test at 250 accounts; benchmark before/after (R1–R3).
3. [ ] Migration schemaV34: `backup_settings`, archive/owner counters with triggers and backfill (R8, R26).
4. [ ] Snapshot writer and verifier in the SQLite adapter; identity check split from `ValidateRuntime` (R4, R5).
5. [ ] Backups directory adapter, `BackupService` with guard, single flight, rotation, status and scheduler (R6–R9); docstrings name the workflow steps.
6. [ ] Admin API with same-origin download guard (R10, R11); runtime wiring of the scheduler.
7. [ ] CLI `backup` subcommand (R12).
8. [ ] Dashboard backup card, translations and tests (R13).
9. [ ] Retention loop with budget, quota-history default and thinning, reservation/automation/warm-up pruning (R14–R18).
10. [ ] WAL size limit, checkpoints and optimize (R19, R20).
11. [ ] Migration schemaV35 indexes; one-pass reports with equivalence test; keyset log cursor (R21–R23).
12. [ ] Correctness fixes (R24–R27).
13. [ ] go vet, golangci-lint, go test, go test -race for touched packages, web lint/typecheck/vitest; record numbers in the decision log.

## Open questions

None blocking. Non-blocking assumptions are recorded in the decision log (defaults on, 45-day quota-history default, no default deletion of request events).

## Decision log

- 2026-10-28: Quota history is coalesced at write time into five-minute buckets (update the series' latest row within its bucket) rather than deduplicated by value. Value-only deduplication lets an idle series lose its zero-burn samples and an active series still writes every minute; bucket coalescing bounds every series to 288 rows/day while reset and window changes always start a new row, so resets are never smoothed together and missing polls remain visible as gaps.
- 2026-10-28: The history read is restricted to series present in `account_quotas`; every consumer looks history up by the current window set, so older windows were already unused. Demand is read only for windows that can be the weekly pace window (secondary, monthly, seven-day primary), the only window its consumer uses.
- 2026-10-28: The series index becomes covering (migration 34) and reads pin it with `INDEXED BY` and a `CROSS JOIN` from `account_quotas`: without statistics SQLite otherwise scans the time index or the whole series index. Measured at 500 accounts (steady shape): history 111 ms, demand 152 ms; with one day of legacy one-minute rows 0.49 s / 0.48 s.
- 2026-10-28: Thinning starts after one day instead of nine: the per-account trend chart shows the hourly mean, which for thinned hours becomes the hourly last value, a negligible difference; in exchange seven-day demand reads 432 instead of 2 016 rows per series. Thinning keys include reset time and window length so the last pre-reset observation and therefore the demand before a reset survive.
- 2026-10-28: Scheduled backups default to on (24 h, keep 3) instead of off. A same-disk snapshot does not replace off-host copies, but without it a corrupted file, operator mistake or failed upgrade on an unmanaged installation is unrecoverable. The free-space guard (5 % of the filesystem, clamped to 256 MiB–2 GiB) prevents the backup from starving SQLite of disk; rotation bounds the directory.
- 2026-10-28: Backups never contain `encryption.key`. A stolen snapshot then exposes only encrypted credentials; the key changes only on reinstall, so the operator stores it once off-host. The UI and API say so explicitly.
- 2026-10-28: The CLI reads through SQLite's own locking instead of the service's inode `flock`, so cron works while the service runs; it never migrates or writes the source.
- 2026-10-28: Quota history gets a 45-day default (the existing floor; every consumer reads ≤ 30 days). Request events stay opt-in: they are the per-request financial evidence for key ledgers and reconciliation, and folding preserves only aggregates. Operators who need bounded growth set request-log retention explicitly; the backup card shows database size so growth is visible.
- 2026-10-28: Reservation pruning deletes only rows nothing can reference again and never the maximum rowid, because reservation rowids are the monotonic fence for outcomes, continuations and affinity versions. Finalized reservations whose usage events are retained stay (foreign key); they are removed together with their events when request-log retention prunes them.
- 2026-10-28: `wal_autocheckpoint` on the writer is left unchanged (owned by the hot-path slice); the maintenance connection adds PASSIVE/TRUNCATE checkpoints and the size limit truncates the file after resets.
