## ADDED Requirements

### Requirement: Quota history stays bounded and is read per current series

The runtime SHALL store at most one quota-history row per account window and five-minute UTC bucket: an observation in the latest row's bucket with the same reset time and window length SHALL update that row, while a later bucket, an older observation or a changed reset time or window length SHALL insert a row. Current quota state SHALL remain unchanged by this rule. Dashboard overview and projections SHALL read, for each current account window, every retained observation in the six-hour pace horizon plus the 64 newest observations of the lookback, through the series index and without a global row limit; the read MUST NOT fail because of the number of accounts.

#### Scenario: Frequent polling does not multiply history rows
- **WHEN** an account window is polled every minute without a reset
- **THEN** history contains one row per five-minute bucket whose values equal the latest observation in that bucket

#### Scenario: A reset inside a bucket is preserved
- **WHEN** the reset time changes between two polls in the same five-minute bucket
- **THEN** both the pre-reset and the post-reset observations remain in history

#### Scenario: Many accounts
- **WHEN** 200 or more accounts each have a day of one-minute history
- **THEN** the dashboard overview and projections complete without an observation-limit error

### Requirement: Online backups are verified snapshots without the encryption key

The runtime SHALL create database backups without stopping request handling by running `VACUUM INTO` through a dedicated connection that cannot modify the source, writing to a temporary file in the destination directory. Before publication the snapshot SHALL be flushed to stable storage and SHALL pass `PRAGMA quick_check`, carry the application id and a supported schema version, and decrypt its runtime identity probe and every stored credential with the installation key. Publication SHALL be an atomic rename followed by a directory flush; a failed attempt MUST remove its temporary file and MUST NOT remove an existing backup. Snapshots SHALL preserve the implicit rowids used as reservation fences. The encryption key MUST NOT be written to the backups directory or included in a downloadable snapshot, and every backup listing SHALL state that the key must be stored separately.

#### Scenario: Backup while requests are being settled
- **WHEN** a backup runs while reservations are created and settled
- **THEN** the published snapshot passes quick_check and opens as a runtime database with the same key
- **AND** request handling continues during the snapshot

#### Scenario: Verification fails
- **WHEN** the snapshot does not pass quick_check or cannot be decrypted with the installation key
- **THEN** no backup file is published and the attempt is reported as failed

#### Scenario: Downloaded snapshot without the key
- **WHEN** an administrator downloads a backup
- **THEN** the file contains no encryption key and the listing warns that the key is stored separately

### Requirement: Backups are scheduled, rotated and bounded by free space

The backup schedule SHALL persist an interval of 0–168 hours (0 disables scheduled backups) and a keep count of 1–30, defaulting to every 24 hours and 3 backups, and changes SHALL apply without restart. A scheduled backup SHALL start when the newest backup is older than the interval, checked periodically after a startup delay. After a successful backup, backups beyond the newest keep count SHALL be removed; skipped or failed attempts MUST NOT remove backups. Only one backup SHALL run at a time; a concurrent request SHALL be rejected as busy. A backup SHALL be refused when the estimated snapshot would leave less than the larger of 256 MiB and 5 % of the filesystem free. The last attempt's time, trigger and outcome SHALL be visible to the administrator.

#### Scenario: Rotation after a scheduled backup
- **WHEN** a scheduled backup succeeds with keep count 3 and four older backups exist
- **THEN** exactly the three newest backups remain

#### Scenario: Not enough disk space
- **WHEN** the estimated snapshot would leave less than the free-space reserve
- **THEN** no snapshot is written, existing backups remain and the outcome reports insufficient space

### Requirement: Backup administration is administrator-only

`GET /api/backups`, `POST /api/backups`, `PUT /api/backups/schedule` and `GET /api/backups/{name}` SHALL require an authenticated dashboard administrator and SHALL keep the existing cross-origin protection for unsafe methods. The download SHALL reject a request whose `Sec-Fetch-Site` header is present and is neither `same-origin` nor `none`, SHALL accept only names of the backup file pattern, SHALL serve only files inside the backups directory and SHALL respond with an attachment and `Cache-Control: no-store`. Proxy API keys MUST NOT access these routes.

#### Scenario: Unauthorized or cross-site access
- **WHEN** an unauthenticated caller, a proxy API key or a cross-site page requests a backup
- **THEN** the request is rejected without reading the file

#### Scenario: Traversal attempt
- **WHEN** the requested backup name contains a path or does not match the backup pattern
- **THEN** the request is rejected as not found

### Requirement: A CLI backup works for cron while the service runs

`codex-lb backup --data-dir DIR --output PATH` SHALL write a verified snapshot of DIR's database to the absolute PATH, or to a standard backup name inside PATH when it is an existing directory, while the service is running or stopped. The command MUST NOT take the service lock, migrate or write the source database, or overwrite an existing file, and SHALL print the written path and size as JSON.

#### Scenario: Cron backup of a running service
- **WHEN** the command runs against the data directory of a running service
- **THEN** it writes a verified snapshot and the service continues to own its database

### Requirement: Retention keeps up with operational growth

Retention SHALL delete in batches of at most 500 rows per batch-writer job and continue until a batch is short or a 20-second cycle budget is spent. Quota history SHALL be retained for 45 days when no override is stored (an explicit 0 disables it) and observations older than nine days SHALL be thinned to the newest per account window and UTC hour; the newest observation of a series MUST NOT be deleted. Request events MUST NOT be deleted unless request-log retention is configured. Finished automation runs SHALL be kept for the newest 100 cycles of each job, and pending or running runs and each job's latest cycle MUST NOT be deleted. Limit warm-up attempts SHALL be kept for the newest 100 per account; only attempts whose reset time is more than eight days old MAY be deleted and the newest attempt of an account MUST NOT be deleted.

#### Scenario: A large backlog
- **WHEN** 100 000 quota-history rows are older than the retention cutoff
- **THEN** one retention cycle deletes them without holding the writer for longer than one bounded batch

#### Scenario: Default retention on upgrade
- **WHEN** no retention override is stored
- **THEN** quota history older than 45 days is removed and request events are kept

### Requirement: Settled reservation pruning preserves fences

A usage reservation SHALL be deleted only when it is settled, was last updated more than seven days ago, is not referenced by a usage event, an account outcome or a continuation, and is not the reservation with the largest rowid. Reservations of request events removed by request-log retention SHALL be deleted in the same transaction under the same conditions. Reserved or reconciliation-pending reservations MUST NOT be deleted, and a reservation created after pruning MUST receive a larger rowid than every deleted reservation.

#### Scenario: Fences survive pruning
- **WHEN** settled reservations older than seven days are pruned
- **THEN** account outcomes, continuations and affinity versions keep resolving and comparing as before
- **AND** a new reservation's rowid is larger than every deleted rowid

### Requirement: WAL size and planner statistics are maintained

The writer connection SHALL set `journal_size_limit` to 64 MiB. A dedicated maintenance connection SHALL run a passive WAL checkpoint every minute and a truncating checkpoint with a busy timeout of at most one second when no frames remain uncheckpointed and the WAL file is not empty; a busy result SHALL be skipped until the next cycle. `PRAGMA optimize` with a bounded analysis limit SHALL run through the batch writer once a day and when the store closes; a failure MUST NOT prevent closing.

#### Scenario: WAL growth during long reads
- **WHEN** long readers prevented checkpoints and then finish
- **THEN** the next maintenance cycle checkpoints and the WAL file no longer exceeds the size limit

### Requirement: Reports and request logs use bounded queries

Admin report responses SHALL be identical to the previous per-day evaluation for every filter, time zone and window while using a number of database statements independent of the number of days. Request-log pages SHALL be ordered through an index on `(requested_at, request_id)`, and the request-log API SHALL accept an optional keyset cursor `beforeRequestedAt` with `beforeRequestId` that returns rows strictly after the cursor in the existing descending order, while offset paging remains supported. Open reservations SHALL use a partial index on reserved rows.

#### Scenario: Daylight-saving week
- **WHEN** a report window spans a daylight-saving transition in the selected time zone
- **THEN** daily rows and medians equal the previous per-day results

#### Scenario: Keyset page
- **WHEN** a client passes the last row of a page as the cursor
- **THEN** the response equals the next offset page without scanning the skipped rows

### Requirement: Storage bookkeeping is atomic and bounded

Deleting an automation job SHALL delete the job and its runs in one transaction and SHALL return any failure. Settling or releasing a reservation whose limit was lowered by repricing SHALL clamp the limit at zero instead of skipping the adjustment. Error-archive and resource-owner bounds SHALL be enforced from counters maintained in the same transaction as each insert or delete, evicting the oldest rows in the previous order. Opening a store with a relative filesystem path SHALL resolve it to an absolute path.

#### Scenario: Release after a downward reprice
- **WHEN** a reservation's limit consumption was reduced below its reserved amount and the reservation is released
- **THEN** the limit's current value becomes zero rather than keeping the reserved amount

## MODIFIED Requirements

### Requirement: Read-only weekly-credit pace follows persisted settings

Dashboard overview/projections SHALL compute the selected read-only weekly-credit pace from fresh persisted quota observations, including saved UTC working weekdays and smoothing interval. Persisted observations are the newest observation of each five-minute bucket of an account window, with reset or window changes kept as separate observations. Missing samples SHALL remain distinguishable from zero burn, reset/window changes SHALL not be smoothed together, and stale/ineligible observations SHALL not inflate usable capacity. The report SHALL preserve its existing nullable forecast, confidence, reset-event and key-attribution contracts without controlling routing or initiating provider work. Settings SHALL survive restart and import and affect the actual report, not merely be accepted by the settings API.

#### Scenario: Saved weekday and smoothing settings reach the report
- **WHEN** the administrator saves working weekdays or a supported smoothing interval and reloads the runtime
- **THEN** overview and projections SHALL apply those saved values to the same persisted observations without fetching provider data

#### Scenario: There is no fresh usable weekly observation
- **WHEN** all candidate observations are stale, invalid or operator-ineligible
- **THEN** weeklyCreditPace SHALL be null rather than showing fictional available credits or zero consumption

#### Scenario: An idle polled account
- **WHEN** an eligible account is polled without consumption for six hours
- **THEN** its history still contains an observation in every five-minute bucket so the pace reports zero burn rather than missing samples
