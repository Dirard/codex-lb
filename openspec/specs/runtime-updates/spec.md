# Runtime updates

## Purpose

Provide administrator-controlled updates and rollback of the installed Go runtime without depending on its external process manager and without restoring stale user data.

## Requirements

### Requirement: Detect stable releases without installing them
The runtime SHALL check stable `go-vX.Y.Z` releases from Dirard/codex-lb, expose current/latest versions and check failures, and offer a manual refresh. Discovery SHALL NOT install or execute release contents. Draft, prerelease, legacy tags and incomplete platform packages MUST NOT be offered. A failed check MUST NOT mean that the installation is up to date.

#### Scenario: A newer complete release exists
- **WHEN** discovery finds a newer stable release with the current platform archive and checksum file
- **THEN** the administrator sees its version and an update action
- **AND** the running version and requests remain unchanged

### Requirement: Authenticate and serialize update actions
Only administrator-authorized requests SHALL start an update or rollback; existing cross-origin protections SHALL apply. Actions SHALL name a known version, never a client-supplied URL, path or shell command. Only one operation SHALL run at a time, independent of the browser request lifetime, with observable progress and sanitized errors.

#### Scenario: Duplicate or unauthorized operation
- **WHEN** another operation is active, or a key-report-only client requests installation
- **THEN** the request is rejected without installing another artifact or interrupting requests

### Requirement: Verify artifacts before switching
Installation SHALL use bounded HTTPS downloads from the fixed release origin, verify SHA-256, reject unsafe archive paths and links, and validate the executable's version, platform and update protocol. It SHALL accept a candidate whose database schema is equal to or newer than the running schema and reject an older schema before changing the running server. Rollback and boot recovery SHALL accept a retained version with an equal schema, or a handoff-capable version whose schema is at least the current version's reported schema floor. Persisted descriptors SHALL be compared by version, platform, schema and update protocol; capability fields SHALL come from inspecting the verified executable. Invalid downloads MUST NOT replace a verified executable or be run before integrity verification.

#### Scenario: Corrupt or incompatible release
- **WHEN** a checksum, archive, executable version, platform, protocol or schema check fails
- **THEN** the administrator receives a retryable failure and the current server continues serving

#### Scenario: A retained executable was changed
- **WHEN** a retained executable fails integrity verification on restart
- **THEN** it is not executed, including for descriptor inspection
- **AND** recovery may use only another verified compatible retained version

#### Scenario: State written by an older launcher
- **WHEN** a released launcher recorded a descriptor without capability fields for a handoff-capable executable
- **THEN** the executable still verifies and its inspected capabilities are used

### Requirement: Run independently of external supervisors
Self-update SHALL use the delivered Go binary and same-user child-process control, without sudo, systemctl, Docker API or root privileges. It SHALL preserve startup arguments, environment, listening address and data directory. Unsupported platform, private storage or execution permissions SHALL yield an explicit unavailable state, not a false successful update.

#### Scenario: Container or direct execution
- **WHEN** the binary is launched directly or in a container with persistent private executable data storage
- **THEN** the same update mechanism applies without controlling the host's process manager

### Requirement: Preserve active work and single-writer ownership
Downloading SHALL leave the current server operational. The system MUST NOT force-cancel an active generation to install an update. Only one installation SHALL own the live database: its launcher holds the exclusive database lock, and only workers it started SHALL use the database. A seamless switch SHALL NOT wait for an idle moment. A classic switch SHALL wait until every retiring worker exited and SHALL require a race-free admission gate with no active work, SHALL reject new work after that gate before upstream dispatch, and its waiting SHALL be bounded and leave the current runtime usable if no safe window occurs.

#### Scenario: Long-running work during a seamless switch
- **WHEN** an update is seamless while a response is streaming
- **THEN** the switch proceeds immediately and the response completes on the previous version

#### Scenario: Long-running work remains active
- **WHEN** the safe-switch waiting deadline of a classic switch expires while a response is active
- **THEN** installation is deferred or fails clearly without terminating that response or leaving admission disabled

#### Scenario: A second installation on the same data
- **WHEN** another codex-lb process opens the same database while the launcher holds the lock
- **THEN** it is refused as before

### Requirement: Verify startup and recover a failed update
The runtime SHALL retain the last verified executable and durable transition state. A new process SHALL validate its data and become ready before the operation is reported successful. If startup fails, it SHALL stop that process; in a seamless switch the running worker continues, otherwise the previous compatible version is restarted. An interrupted transition SHALL recover deterministically after restart; at most one worker SHALL run background work and processes of another installation SHALL never use the database.

#### Scenario: New binary fails its startup check
- **WHEN** a staged update cannot become ready
- **THEN** the previous version serves on the configured address and the failed update remains visible
- **AND** an unactivated candidate that ignores graceful termination is killed and joined before any recovery step that needs the database

#### Scenario: Discovery follows failed recovery
- **WHEN** automatic or manual release discovery runs after the previous version was restored
- **THEN** it updates release information without clearing the failed installation result

### Requirement: Roll back the program without rolling back data
The administrator SHALL be able to select the retained previous compatible version. Rollback SHALL use the same verification and switching rules as update. It MUST NOT restore an old database, replace encryption credentials, clear reservations, reset limits or discard new reports. A consistent pre-switch backup SHALL be preserved separately for explicit disaster recovery, not silently restored.

#### Scenario: New traffic exists after an upgrade
- **WHEN** the administrator rolls back to the retained compatible version
- **THEN** the new traffic, key/group settings and accounting remain in the same database

#### Scenario: Restart after a confirmed rollback
- **WHEN** the installation restarts using the same previously processed bootstrap executable
- **THEN** the durable rollback selection remains current, even when the bootstrap has a newer version number

### Requirement: Explain update state in the administrator interface
The administrator UI SHALL show the running version, newer version when known, update and previous-version rollback actions, confirmation, progress, failure and unavailable reasons. It SHALL reconnect after a switch without reporting success before verification. Update controls MUST NOT be exposed in key-report mode. The status SHALL say whether the installation switches seamlessly; the confirmation SHALL then state that active requests finish on the current version while new requests go to the new one and that a release with an incompatible database change waits for an idle moment instead; otherwise it SHALL disclose the brief restart and idle WebSocket reconnect. The phases `preparing` and `switching` SHALL be labeled.

#### Scenario: Observe a seamless update
- **WHEN** the administrator confirms an update on a seamless installation
- **THEN** progress moves through preparing and switching to completed without a reconnect interruption

#### Scenario: Observe an update across restart
- **WHEN** the administrator confirms an update that switches classically
- **THEN** the page tolerates temporary unavailability and displays the verified result after reconnecting

### Requirement: Updates may migrate the database forward with a restorable backup

An update to a version with a newer database schema SHALL take the consistent pre-switch backup before the new version starts and migrates the database; a seamless switch takes it online. If a classic candidate fails startup verification, cannot be persisted as current, or fails activation after migrating, the runtime SHALL restore that backup — removing the failed version's WAL and shared-memory files first — before restarting the previous version. A seamless candidate migrates only additively for the running version, so its failure SHALL NOT restore the backup. Failures before the new version starts SHALL NOT replace the database. After a schema-changing update the previous version SHALL be offered as a rollback only when it is handoff-capable and its schema is at least the new version's schema floor; otherwise the status SHALL explain that returning to it requires restoring the pre-update backup. Boot recovery MUST NOT restore a backup automatically, because traffic may already have used the newer schema, unless that backup was taken in the same boot right before the failing version opened the database.

#### Scenario: A release adds migrations
- **WHEN** the administrator applies go-v1.1.0 with schema 40 to an installation on schema 33
- **THEN** the backup is taken, go-v1.1.0 starts and migrates, and the installation runs go-v1.1.0
- **AND** the dashboard shows that rollback to go-v1.0.11 requires restoring the pre-update backup

#### Scenario: The migrating version fails to start
- **WHEN** a classic candidate migrates the database and then fails startup verification
- **THEN** the pre-update backup replaces the database and the previous version starts on it with the failure reported

#### Scenario: An additive migration is rolled back
- **WHEN** a handoff-capable version on schema 41 is updated to one on schema 42 whose floor is 40 and the administrator rolls back
- **THEN** the previous version runs on the schema-42 database without a restore and the new data remains

### Requirement: A replaced bootstrap executable takes effect

When the managed runtime starts from a bootstrap executable whose digest differs from the recorded bootstrap digest, it SHALL adopt that executable as the current version whether its version is newer or older, retaining the previously current version as the rollback candidate, and SHALL log the bootstrap and selected versions. Whenever the selected version's schema is newer than the existing database's schema version, the launcher SHALL take the pre-update backup before that worker opens the database, also when an earlier start failed. Restarting with the recorded bootstrap MUST keep the current selection, including an administrator's rollback. State without a recorded bootstrap digest SHALL keep adopting only newer bootstraps. Adoption MUST NOT restore data; an older binary still refuses a database it cannot operate on.

#### Scenario: Emergency downgrade by binary replacement
- **WHEN** an operator replaces go-v1.0.4 with the go-v1.0.2 binary and restarts the service
- **THEN** go-v1.0.2 runs, go-v1.0.4 is retained as the previous version, and the launcher logs the selection
- **AND** restarting again with the same go-v1.0.2 binary keeps that selection

#### Scenario: The adopted version fails on its first start
- **WHEN** the replaced bootstrap's worker migrates the database and fails before serving
- **THEN** the backup taken in that boot is restored and the previous version starts on it with the failure reported

#### Scenario: Manual upgrade across a schema change
- **WHEN** an operator replaces the go-v1.0.10 bootstrap (schema 33) with go-v1.2.0 and restarts the service
- **THEN** the database is backed up to `updates/` before go-v1.2.0 migrates it, and go-v1.0.10 is retained with the note that returning to it requires that backup

### Requirement: A seamless switch shares the listening socket
When the running worker and the candidate are handoff-capable and the running schema is at least the candidate's schema floor, the launcher SHALL keep the listening socket and the database lock and SHALL pass both, with a lifeline pipe and an instance id, to the candidate as inherited descriptors. The candidate SHALL start beside the running worker after an online backup, open and verify the database, and accept connections on the shared socket before the previous worker stops accepting. No connection attempt SHALL be refused because of the switch. The update SHALL be reported as succeeded once the candidate is active and the previous worker retires. Workers that are not handoff-capable SHALL be started only after the launcher released the socket and lock.

#### Scenario: Continuous traffic during an update
- **WHEN** clients open connections continuously while a seamless switch runs
- **THEN** every connection is accepted by one of the two versions and the new version receives the connections opened after activation

#### Scenario: Rollback to a classic-only version
- **WHEN** the administrator rolls back to a retained version that is not handoff-capable
- **THEN** the switch is classic and the launcher releases the socket and lock before starting it

### Requirement: The previous version finishes accepted work
A retiring worker SHALL stop accepting connections and stop its metrics listener and background loops before acknowledging retirement. It SHALL keep serving every request it accepted, including queued admissions and the current turn of each client WebSocket, without rejecting them as draining. It SHALL close idle client Responses WebSockets with status 1001 at once and active ones after their current turn. It SHALL exit when its accepted work completes, or after 30 minutes followed by the ordinary drain with the configured shutdown grace. When it exits, the active worker SHALL mark the reservations of workers that are no longer alive as interrupted.

#### Scenario: A long stream across the switch
- **WHEN** a streamed generation started on the previous version is still running after the switch
- **THEN** it completes and settles on the previous version, which exits afterwards

#### Scenario: An idle Codex WebSocket
- **WHEN** a client WebSocket is idle at retirement
- **THEN** it is closed with 1001 and the client's next turn is served by the new version

### Requirement: Overlapping versions share admission limits
While more than one worker is alive the launcher SHALL relay to each worker, at least every 500 ms, the sum of the other workers' active and queued generations, per-account creates and streams and per-key streams. Each worker SHALL count them against its global capacity, account response-create and stream limits and API-key fair share, and SHALL lower its Go memory limit by its peers' resident memory, not below a quarter of the planned limit. A decrease SHALL wake waiting admissions; the planned limits SHALL return when the peers are gone.

#### Scenario: An account at its stream limit across versions
- **WHEN** the previous version holds an account's full stream allowance during the overlap
- **THEN** the new version selects another account or waits for that account's streams to finish

### Requirement: Background work runs on one worker
A worker started beside another live worker SHALL NOT start background loops, the metrics listener, backup temp cleanup or interrupted reprice resumption until the launcher tells it to, which SHALL happen only after the previous worker acknowledged retirement or exited. A worker started alone SHALL start them on activation.

#### Scenario: Automations during a switch
- **WHEN** an automation is due while a seamless switch runs
- **THEN** exactly one worker runs it

### Requirement: The launcher follows the installed version
A launcher whose selected version is newer than itself and handoff-capable SHALL record a pending delegation for that digest and re-execute the selected binary as the launcher with the same arguments and process id; the delegated launcher SHALL load the installation state without bootstrap adoption and confirm the delegation after its first worker is active. After two starts for the selected digest ended unconfirmed, a launcher SHALL NOT delegate to it again, SHALL record the failure and SHALL serve with its own launcher logic.

#### Scenario: Restart after a dashboard update
- **WHEN** an installation whose bootstrap is go-v1.2.0 restarts after updating to a newer handoff-capable release
- **THEN** the newer release runs as both launcher and worker

#### Scenario: The delegated launcher crashes
- **WHEN** the delegated launcher exits twice before its first worker is active and the service manager restarts the bootstrap
- **THEN** the bootstrap serves with its own launcher logic and reports the failed delegation
