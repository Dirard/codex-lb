# Runtime updates

## Purpose

Provide administrator-controlled updates and rollback of the installed Go runtime without depending on its external process manager and without restoring stale user data.

## ADDED Requirements

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
Installation SHALL use bounded HTTPS downloads from the fixed release origin, verify SHA-256, reject unsafe archive paths and links, and validate the executable's version, platform and update protocol. It SHALL reject incompatible database schemas before stopping the current server. Invalid downloads MUST NOT replace a verified executable or be run before integrity verification.

#### Scenario: Corrupt or incompatible release
- **WHEN** a checksum, archive, executable version, platform, protocol or schema check fails
- **THEN** the administrator receives a retryable failure and the current server continues serving

#### Scenario: A retained executable was changed
- **WHEN** a retained executable fails integrity verification on restart
- **THEN** it is not executed, including for descriptor inspection
- **AND** recovery may use only another verified compatible retained version

### Requirement: Run independently of external supervisors
Self-update SHALL use the delivered Go binary and same-user child-process control, without sudo, systemctl, Docker API or root privileges. It SHALL preserve startup arguments, environment, listening address and data directory. Unsupported platform, private storage or execution permissions SHALL yield an explicit unavailable state, not a false successful update.

#### Scenario: Container or direct execution
- **WHEN** the binary is launched directly or in a container with persistent private executable data storage
- **THEN** the same update mechanism applies without controlling the host's process manager

### Requirement: Preserve active work and single-writer ownership
Downloading SHALL leave the current server operational. Switching SHALL require a race-free admission gate with no active work; new work after that gate SHALL be rejected before upstream dispatch. The system MUST NOT force-cancel an active generation to install an update. Waiting SHALL be bounded and leave the current runtime usable if no safe window occurs. Only one server process SHALL own the live database at a time.

#### Scenario: Long-running work remains active
- **WHEN** the safe-switch waiting deadline expires while a response is active
- **THEN** installation is deferred or fails clearly without terminating that response or leaving admission disabled

### Requirement: Verify startup and recover a failed update
The runtime SHALL retain the last verified executable and durable transition state. A new process SHALL validate its data and become ready before the operation is reported successful. If startup fails, it SHALL stop that process before restarting the previous compatible version. An interrupted transition SHALL recover deterministically after restart, never running two database owners.

#### Scenario: New binary fails its startup check
- **WHEN** a staged update cannot become ready
- **THEN** the previous version is restarted on the configured address and the failed update remains visible
- **AND** an unactivated candidate that ignores graceful termination is killed and joined before the previous process starts

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
The administrator UI SHALL show the running version, newer version when known, update and previous-version rollback actions, confirmation, progress, failure and unavailable reasons. It SHALL reconnect after a switch without reporting success before verification. Update controls MUST NOT be exposed in key-report mode. A brief listener interruption and idle WebSocket reconnect SHALL be disclosed; seamless handoff is not promised.

#### Scenario: Observe an update across restart
- **WHEN** the administrator confirms an update and the server switches
- **THEN** the page tolerates temporary unavailability and displays the verified result after reconnecting
