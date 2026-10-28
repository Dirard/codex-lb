# Seamless runtime updates

- Last edited with skill pack: `0.2.2`

## Title and scope

Switch a managed installation to another release without waiting for a moment with no active requests and without refusing a single connection. The launcher keeps the listening socket and the database lock for the whole life of the service; the new version starts beside the current one, verifies its data, starts taking new connections, and the previous version finishes every request it already accepted — streaming generations, SSE, active WebSocket turns — before it exits. During that overlap both versions share the SQLite database and the per-account admission limits safely, and exactly one of them runs background work. Releases that cannot overlap (an older worker protocol or a database change older code cannot run on) keep the existing idle-gated switch. The launcher's own update logic follows the installed release from the next start on, so later releases can improve the update mechanism itself without a manual binary replacement.

Out of scope: replacing the launcher image while it runs (recorded as use case 8 for a later slice), choosing among several newer releases, multi-host deployments.

## Planning anchor

- Entrypoints:
  - `cmd/codex-lb/update_runtime.go#serveManagedRuntime` — launcher: loads installation state, starts the first worker, runs `application.RuntimeUpdates` in the launcher process (its logic is always the bootstrap binary's).
  - `cmd/codex-lb/update_host.go#updateHost` — one child at a time; `PrepareStart` refuses a second worker; `Stop` commits shutdown inside the worker's idle gate; workers die with the launcher through `Pdeathsig`.
  - `cmd/codex-lb/update_worker.go#serveUpdateWorker` — worker binds the TCP port itself, takes the database `flock` itself (`openRuntime` → `openDataWithOptions` → `lock_unix.go#lockDatabase`), control endpoints `/ready`, `/activate`, `/prepare-stop`, `/cancel-stop`, `/stop`.
  - `internal/application/runtime_updates.go#install` — download → verify → `WaitForIdle` (up to 10 min) → `Stop` → `Backup` → `PrepareStart` → persist → `Activate`, with restore paths.
  - `cmd/codex-lb/runtime.go#openRuntime` / `#serveListener` — `MarkInterruptedReservations` at open, background loops and metrics start together with serving, drain = `beginDrain` + `Proxy.BeginDrain` + `Shutdown(grace)`.
  - `internal/adapters/sqlite/store.go#OpenWithOptions` / `#migrate` / `#sqliteDSN`; `usage_reconciliation.go#MarkInterruptedReservations`; `ledger.go` reservation insert; `model_pricing_reprice.go#resumeModelReprices`.
  - `internal/application/account_admission.go#admittedAccount`, `proxy.go#acquire` / `#BeginDrain` / `#DrainStarted`, `internal/adapters/httpapi/proxy_websocket.go` idle loop.
- Changed assumptions:
  - "Only one server process owns the live database" becomes "one installation owns it": the launcher holds the inode `flock` and the listening socket and lends both to its workers; during a handoff two workers of that installation use the database at the same time, exactly one of them is *active* (accepts connections and runs background work).
  - SQLite with two writer processes: a deferred write transaction can fail with `SQLITE_BUSY_SNAPSHOT` regardless of the busy timeout, so every write transaction begins `IMMEDIATE`.
  - A reservation in status `reserved` is no longer proof of an interrupted request at startup; it is interrupted only when its owning process is not alive.
  - An older binary can operate on a newer database when every migration in between is additive; the database records the lowest code schema that may use it.
  - The launcher's update logic is no longer frozen at the bootstrap version: a bootstrap that knows delegation re-executes the installed newer release as the launcher.
- Non-local: domain descriptor, application update use case and admission, SQLite adapter, HTTP WebSocket adapter, launcher/worker runtime, dashboard settings card, OpenSpec `runtime-updates` and `go-runtime`.
- Impacted specs in `specs/`: `adaptive-capacity.md` sizes one process per host (`GOMEMLIMIT` 75 %); use case 3.2 lowers a worker's limit by its peers' resident memory during an overlap. Action: none (the capacity plan itself is unchanged; this spec owns the overlap rule).
- Impacted OpenSpec capabilities: `runtime-updates` — action `amend` (single-owner, idle-gate, schema and UI requirements; new seamless handoff, shared database and delegation requirements) through change `openspec/changes/seamless-updates/`. `go-runtime` — action `amend` (interrupted-reservation marking at startup, multi-process write transactions) in the same change.

## Connected groups or observed existing logic

1. Entry and orchestration
   - `main.go#run` → `serve` → `serveManagedRuntime` on versioned Linux builds with a fixed port, otherwise `serveWithoutUpdates` / `runtime.serve`.
   - Launcher: `updatefiles.New` (holds `updates/install.lock`), `Initialize` (adopts a replaced bootstrap), private control directory with `parent.sock` and a random token, `prepareManagedBoot` (verify → `update-info` → spawn → `/ready`), `updatecontrol.Handler(service)` serves the dashboard's update API forwarded by the worker, `RunChecks` hourly, monitor loop.
   - Worker: `CODEX_LB_INTERNAL_WORKER_SOCKET` / `PARENT_SOCKET` / `UPDATE_TOKEN`; `cfg.updater` is an `updatecontrol.Client` to the launcher; a two-minute activation timeout; idle gate = `requestLifecycle.tryBeginUpdateDrain` + `Proxy.TryBeginIdleDrain` with a 15 s lease.
   - Descriptor: `update-info` prints `domain.RuntimeDescriptor{version, goos, goarch, schemaVersion, updateProtocol}`; launchers compare descriptors with `!=` and accept only `UpdateProtocol == 1`. Released launchers (go-v1.0.10 … go-v1.1.1) ignore unknown JSON fields.
2. Persistence
   - One writer connection per process (`writer.go#batchWriter`, `MaxOpenConns(1)`), deferred `BEGIN`, `_busy_timeout=5000`, WAL, FULL synchronous; readers `_query_only`; maintenance connection for checkpoints.
   - `migrate` refuses `user_version > len(migrations)`; schema 40 is current.
   - `usage_reservations(status, needs_reconciliation, …)`; settlement (`ledger.go`) finalizes `status='reserved'` rows and clears the flag, so a flagged live reservation still settles; `continuation.go` treats `reserved AND needs_reconciliation=1` as refusable history.
   - `token_rotation.go#MarkAccountReauthRequired` is compare-and-set on the refresh-token ciphertext; `account_tokens.go#exchange` returns the refresh error when another process already rotated the token.
   - `resumeModelReprices` starts a store-owned goroutine inside `OpenWithOptions`.
   - `update_backup.go#Backup` takes the inode lock itself and requires that no worker runs; `RestoreBackup` likewise.
3. Admission and transport
   - Global admission: `p.admitted` (active + queued) and `p.streams` channels sized by the capacity plan; `BeginDrain` closes `p.draining`, which rejects queued and new work.
   - Per-account admission: in-memory `accountPressure{creates, streams, keys}` against `proxy_account_response_create_limit`, `proxy_account_stream_limit`, recovery reserve and API-key fair share.
   - Client Responses WebSockets close with 1001 when `DrainStarted()` fires while idle; `requestLifecycle.track` counts handler lifetimes including hijacked sockets.
4. Frontend consumers
   - `web/src/features/runtime/runtime-update-settings.tsx`: phases from `settings.runtime.phases.*` with raw fallback, `restartWarning` in the confirmation, reconnect tolerance while busy.
5. Validation
   - `cmd/codex-lb/update_integration_test.go` / `update_failure_test.go` build real binaries and switch versions; `internal/application/runtime_updates_test.go` drives the use case with a fake host.
6. Specs
   - `openspec/specs/runtime-updates/spec.md` promises the idle gate and "seamless handoff is not promised"; this change replaces those parts.

## Use cases

### 1. Handoff capability and switch decision

downloaded release --inspect descriptor--> candidate descriptor --compare with the running worker's descriptor--> [seamless switch, classic switch, refused]

Requirements:

  R1:
`update-info` SHALL additionally report `handoff` (the worker/launcher handoff protocol level, 1 in this slice) and `schemaFloor` (the lowest code schema that may operate on a database at this binary's schema). `updateProtocol` SHALL stay 1 so released launchers keep accepting the release. Both new fields SHALL be omitted when zero.

  R2:
Persisted and inspected descriptors SHALL be compared by identity (version, platform, schema, update protocol); capability fields SHALL be taken from the latest inspection of the verified executable, so state written by an older launcher stays valid.

  R3:
A switch (update or rollback) SHALL be seamless when the running worker and the candidate both report `handoff ≥ 1`, the launcher holds the listener (use case 2), the platform matches, and the older of the two schemas is at least the newer one's `schemaFloor`. Otherwise it SHALL use the classic idle-gated switch with its existing rules; an update to an older schema SHALL still be refused. A classic switch SHALL start only after every retiring worker exited.

  R4:
Rollback and boot recovery SHALL accept a retained version with an equal schema, or a handoff-capable version whose schema is at least the current version's `schemaFloor`; such a rollback SHALL NOT restore a backup.

### 2. Launcher owns the listener and the database lock

launcher start --bind configured address and lock database inode (handoff-capable current only)--> owned socket and lock --spawn worker with inherited descriptors, instance id and lifeline--> worker serving on the shared socket

Requirements:

  R5:
When the selected worker is handoff-capable, the launcher SHALL bind the configured TCP address and take the exclusive inode lock of `codex-lb.sqlite3` before starting it, and SHALL keep both until it exits. It SHALL pass the listener, the lock and the read end of a lifeline pipe to every handoff-capable worker as inherited descriptors, together with a random instance id and the ids of the other live workers.

  R6:
A worker started with inherited descriptors SHALL serve on the inherited listener, SHALL verify that the inherited lock refers to the database file and is held, and SHALL NOT bind or lock on its own; an empty database file SHALL count as a new store. When the lifeline reaches end of file the worker SHALL shut down gracefully as on SIGTERM.

  R7:
A worker started without inherited descriptors (standalone `serve`, `--self-update=false`, or a released launcher) SHALL behave exactly as before: bind, lock, mark every interrupted reservation, start background work on activation, and support the idle-gate endpoints.

  R8:
A worker that is not handoff-capable SHALL be started only after the launcher released its listener and lock, and the launcher SHALL re-acquire both before it starts a handoff-capable worker again.

### 3. Seamless switch

verified candidate --online backup--> backup recorded --start candidate beside the active worker, deferred background--> prepared candidate --persist current/previous--> committed selection --activate candidate--> two workers accepting --retire previous worker (stop accepting, stop background)--> candidate sole acceptor --start candidate background--> switch complete --previous worker finishes accepted work and exits--> single worker

Requirements:

  R9:
A seamless switch SHALL take the consistent pre-switch backup online, before the candidate opens the database, while the active worker keeps serving; it SHALL NOT wait for an idle moment and SHALL NOT pause admission.

  R10:
The candidate SHALL open and migrate the database, validate its data and become ready while the active worker serves. If it fails before activation it SHALL be stopped and the active worker SHALL continue without a backup restore (its schema is at least the candidate's floor), and the failure SHALL be reported.

  R11:
Activation SHALL make the candidate accept connections on the shared socket before the previous worker stops accepting, so no connection attempt is refused and no accepted connection is dropped by the switch. If activation fails, the candidate SHALL be stopped, the previous selection restored and the previous worker SHALL keep serving.

  R12:
The update SHALL be reported as succeeded once the candidate is active and the previous worker is retiring; the previous worker's drain SHALL NOT block later status checks. A later update MAY start while a retiring worker still drains.

### 3.1 Retirement of the previous worker

retire request --stop metrics and background loops, close the listener copy and idle keep-alive connections--> retiring worker --close idle client WebSockets with 1001, finish active turns, streams and queued requests--> drained --exit--> launcher notifies the active worker

Requirements:

  R13:
On retirement a worker SHALL stop accepting connections, stop its metrics listener and background loops (joined before the retirement is acknowledged), and SHALL keep serving every request it already accepted, including queued admissions, without rejecting them as draining.

  R14:
A retiring worker SHALL close each idle client Responses WebSocket with status 1001 immediately and each active one after its current turn reaches a terminal event, so clients reconnect to the active worker.

  R15:
A retiring worker SHALL exit when its accepted work is complete, or at the retirement deadline (30 minutes) after the ordinary drain with the configured shutdown grace. When it exits the launcher SHALL ask the active worker to mark the reservations of workers that are no longer alive as interrupted.

### 3.2 Shared admission during the overlap

| while more than one worker is alive: live workers --report own load (streams, admitted, per-account creates/streams/keys, resident memory)--> launcher --sum the other workers' load--> each worker's peer load --apply to global slots, account limits, fair share and memory limit--> admission over the union |

Requirements:

  R16:
While more than one worker is alive the launcher SHALL relay, at least every 500 ms, to each worker the sum of the other workers' load, and SHALL send an empty peer load when only one worker remains.

  R17:
A worker SHALL count its peers' active and queued generations against its global admission capacity, its peers' per-account creates and streams against the account response-create and stream limits, and its peers' per-key streams in API-key fair share; a decrease SHALL wake waiting admissions.

  R18:
While peers exist a worker SHALL lower its Go memory limit by its peers' resident memory, not below a quarter of the planned limit, and SHALL restore the planned limit when the peers are gone.

### 3.3 Background work belongs to the active worker

candidate start (overlap) --defer background loops, metrics, stale-temp cleanup and reprice resumption--> fenced candidate --previous worker acknowledged retirement--> start background on the candidate

Requirements:

  R19:
A worker started beside another live worker SHALL NOT start background loops, the metrics listener, backup temp cleanup or interrupted reprice resumption until the launcher tells it to; a worker started alone SHALL start them on activation as before.

  R20:
At no time after a switch SHALL two workers run background loops; the launcher SHALL start the candidate's background only after the previous worker acknowledged retirement or exited.

### 4. Two versions on one database

writer job --BEGIN IMMEDIATE (busy timeout)--> serialized write across processes; reservation insert --record owner instance--> owned reservation; open by older floor-aware code --read recorded floor--> operate or refuse

Requirements:

  R21:
Every write transaction of the writer and maintenance connections SHALL begin `IMMEDIATE`, so concurrent writers in two processes wait on the busy timeout instead of failing a read-then-write transaction.

  R22:
A new reservation SHALL record the instance id of the process that created it. Startup SHALL mark as interrupted only `reserved` rows whose owner is not a live worker; rows without an owner SHALL count as not alive.

  R23:
Each migration SHALL be classified as additive (older code keeps working on the result) or not; migrating SHALL record in the database the lowest code schema that may operate on it (`schemaFloor`). Code whose schema is older than the database but not older than the recorded floor SHALL open it without migrating; otherwise it SHALL refuse as before.

  R24:
When a token refresh fails permanently but the stored refresh token changed meanwhile (another process rotated it), the refresh SHALL return the stored current access token instead of an error.

### 5. Classic switch fallback

candidate not seamless --wait for idle gate--> stopped worker --backup, start, persist, activate--> classic result

Requirements:

  R25:
Non-seamless switches SHALL keep the existing idle gate, offline backup, restore-on-failed-migration and recovery behavior, including when the launcher holds the listener (it hands the same socket to the new worker) and when it switches between handoff-capable and older workers (R8).

### 6. Launcher follows the installed version

bootstrap start --initialize installation state--> selected version --selected is newer, handoff-capable and not marked failed--> re-execute selected binary as launcher --> delegated launcher --first worker activated--> delegation confirmed

Requirements:

  R26:
A launcher whose selected version is newer than itself and handoff-capable SHALL record a pending delegation for that digest and re-execute the selected binary as the launcher with the same arguments and process id; the delegated launcher SHALL load the installation state without bootstrap adoption.

  R27:
The delegated launcher SHALL confirm the delegation after its first worker is active. After two starts for the selected digest ended unconfirmed, a launcher SHALL NOT delegate to it again, SHALL record that the delegation failed and SHALL serve with its own launcher logic; flags in the service command line must be understood by the bootstrap, which parses them before delegating.

  R30:
Whenever the selected version's schema is newer than the existing database's `user_version` (a replaced bootstrap, a first managed start on existing data, or a start that failed earlier), the launcher SHALL back up the database before the worker opens it. If that worker fails before serving, boot recovery SHALL restore this boot's backup and start the previous version, so the one manual installation that enables this slice is as recoverable as an update.

### 7. Administrator interface

status --seamless capability--> confirmation text; phases --preparing, switching--> progress labels

Requirements:

  R28:
The status SHALL expose whether the running installation can switch seamlessly; the confirmation SHALL then say that active requests finish on the current version while new requests go to the new one, and that a release with an incompatible database change waits for an idle moment instead. New phases `preparing` and `switching` SHALL have labels in every locale.

### 8. Later slice: launcher replacement without restart (not implemented)

running launcher after a seamless switch --hand socket, lock, lifeline, control socket and child records to the new image--> re-executed launcher

Requirements:

  R29:
Not part of this slice. Recorded so the handoff protocol keeps workers independent of the launcher process image (lifeline instead of `Pdeathsig`, descriptors passed by number, child state reconstructible from the control socket).

## Implementation checklist

1. [ ] OpenSpec change `seamless-updates` (proposal, tasks, context, `runtime-updates` and `go-runtime` deltas); strict validation.
2. [ ] Descriptor `handoff` / `schemaFloor`, identity comparison, `SeamlessSwitch` and widened rollback/boot compatibility with tests (R1–R4).
3. [ ] SQLite: `IMMEDIATE` writer transactions, migration 41 (`owner_instance`, `schema_compatibility`), instance-scoped interrupted marking, floor recording and newer-compatible open, explicit reprice resumption; two-store contention tests (R21–R23).
4. [ ] Token refresh re-read after a permanent error (R24).
5. [ ] Proxy peer load (global ghost slots, account and key pressure), load snapshot, retirement signal for WebSockets with tests (R14, R17).
6. [ ] Launcher-owned listener and lock, inherited descriptors, lifeline, instance ids, legacy-worker release/re-acquire (R5–R8).
7. [ ] Worker handoff endpoints (`/background`, `/retire`, `/load`, `/peer-load`, `/peer-exited`), deferred background, retirement drain, memory limit (R13, R15, R18–R20).
8. [ ] Application seamless flow with failure paths; host multi-worker bookkeeping and relay (R9–R12, R16, R25).
9. [ ] Boot delegation with crash guard; backup before an adopted bootstrap migrates (R26, R27, R30).
10. [ ] Status flag, phases, confirmation text, locales and tests (R28).
11. [ ] Integration test with real binaries: switch under continuous load and a long stream, no refused connection, stream completes on the old worker, background on one worker; classic fallback and delegation tests.
12. [ ] go vet, golangci-lint, go test, go test -race, web checks; record results.

## Open questions

None blocking. Assumptions are recorded in the decision log.

## Decision log

- 2026-10-28: The listener is shared by descriptor inheritance from the launcher, not `SO_REUSEPORT`. With one socket the kernel keeps queued connections when the previous worker closes its copy, so a connection is never refused or reset by the switch; `SO_REUSEPORT` would drop the accept queue of the closing socket.
- 2026-10-28: `updateProtocol` stays 1 and capability travels in new optional descriptor fields. Released launchers require protocol 1 exactly; bumping it would make every released launcher refuse all future releases.
- 2026-10-28: The launcher, not the worker, holds the database `flock`, because two workers of one installation must open the database at once while a second installation must still be refused. The lock is an inode `flock` on its own open file description, independent of SQLite's POSIX locks.
- 2026-10-28: Writes begin `IMMEDIATE` everywhere: with one writer connection per process this costs nothing in a single process, and across two processes it turns `SQLITE_BUSY_SNAPSHOT` failures of read-then-write transactions (settlement, admission records) into bounded waits.
- 2026-10-28: Interrupted-reservation marking is scoped by owner instance instead of being skipped during an overlap. Settlement already clears the flag, but a falsely flagged live reservation would show as interrupted usage and become refusable continuation history while its request still runs.
- 2026-10-28: The previous worker keeps admitting what it already accepted (queued requests and the current WebSocket turn) instead of rejecting it as draining. Those requests are already in flight from the client's view; the only work moved is new connections and the next WebSocket turn, which the client opens on a new socket.
- 2026-10-28: Load is relayed by polling every 500 ms rather than per event. The previous worker only loses load after retirement, so a slightly stale peer load overestimates it; the only window where both workers admit new work is between activation and retirement (milliseconds).
- 2026-10-28: Background work starts on the candidate only after the previous worker joined its loops, which can delay the candidate's first quota poll by the length of one in-flight background call; duplicate automations, retention or backups would be worse than a late poll.
- 2026-10-28: The schema floor lives in the database because older code cannot know later migrations. Migrations 1–40 are classified as non-additive (unknown history), so the first overlap across a schema change is possible from schema 41 code on.
- 2026-10-28: Boot delegation instead of in-place launcher replacement: it needs no state transfer between process images and keeps the process id that systemd and Docker track. The cost is that improved launcher logic applies from the next start; in-place replacement is use case 8.
- 2026-10-28: Installations on a released launcher (go-v1.0.10 … go-v1.1.1) keep the classic switch, because their launcher process runs the old update logic. One manual installation of the bootstrap binary (any release from this slice) enables seamless switching and delegation from then on.
- 2026-10-28: An additive migration runs while the previous worker serves and holds the write lock for its whole transaction; the previous worker's writes wait for the lock within their own deadlines (settlement 10 s, at most 15 s). Additive therefore also means "finishes in a few seconds" (no table rewrite or large backfill); anything longer is classified non-additive and switches classically. Recorded in `AGENTS.md` and beside `additiveMigrations`.
- 2026-10-28 (review): The writer no longer relies on SQLite's busy handler, whose sleeps grow to 100 ms and lose to a peer committing back to back: its connection has `busy_timeout=0`, BEGIN IMMEDIATE is retried every 1–5 ms with jitter while a job can still wait (cap 15 s), a job whose context ended is answered at once, and a writer that held the lock for 250 ms pauses 10 ms before its next batch (at most 4 % of a saturated writer). Measured worst wait of the other store: 5 s with failures before, about 250 ms after.
- 2026-10-28: The writer's 5 s batch deadline starts after `BEGIN IMMEDIATE` obtained the lock, so waiting for the other worker does not consume the execution budget of a batch.
- 2026-10-28: Snapshot writing and verification (`WriteSnapshot`, `VerifySnapshot`) accept a database newer than the code when its recorded floor allows it, so a rolled-back worker can still take online backups.
- 2026-10-28: A loser of a refresh race may see `refresh_token_reused` before the winner committed its rotation; for that code only the re-read is retried for up to one second (50 ms steps) before marking the account for re-authentication. Empty access tokens are never returned and a forced refresh never returns the rejected token.
- 2026-10-28 (review): A classic switch waits for retiring workers: it may migrate non-additively and restoring its backup needs no live worker. The peer load expires in the worker after 5 s without a relay update; exited workers are reported by instance id with retries; retention and reprice resumption wait while another worker lives; retirement stops accepting before joining background work.
- 2026-10-28 (review): The boot backup is decided from the database's `user_version`, not from whether this start adopted the bootstrap, so a first start that failed while backing up cannot migrate unprotected on the next start. A backup taken in the same boot may be restored by boot recovery because no traffic reached the migrated schema.
- 2026-10-28 (review): Delegation is retried once: an ordinary stop can end a delegated start before its confirmation; a launcher that crashes keeps failing and is given up after two starts.
- 2026-10-28 (review): `/load` and `/peer-load` belong to handoff protocol 1: a launcher decodes them with its own `WorkerLoad`, so fields a later worker adds are not relayed by an older launcher; future workers must also keep accepting protocol level 1.
- 2026-10-28 (review): Retirement does not use `http.Server.Shutdown`, which closes an accepted connection whose request it reads after shutdown began. The retiring worker disables keep-alives, closes its copy of the shared listener, waits until every non-hijacked connection closed (counted through `ConnState`) and only then waits for the request lifecycle, so that wait cannot race a new request. The only remaining loss is the inherent HTTP/1.1 race of a request sent on an idle keep-alive connection as it closes.
- 2026-10-28 (review): The shared listener is duplicated with `F_DUPFD_CLOEXEC` instead of `TCPListener.File`: passing a `File()` to exec calls `Fd()`, which clears `O_NONBLOCK` on the shared open file description and can leave a serving worker blocked in `accept`. The worker control listener accepts bodies up to 4 MiB (peer loads grow with accounts and keys); relay calls run in parallel and failures are logged once per worker.
