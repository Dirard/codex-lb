## ADDED Requirements

### Requirement: Updates may migrate the database forward with a restorable backup

An update to a version with a newer database schema SHALL take the consistent pre-switch backup before the new version starts and migrates the database. If the new version fails startup verification, cannot be persisted as current, or fails activation, the runtime SHALL restore that backup — removing the failed version's WAL and shared-memory files first — before restarting the previous version. Failures before the new version starts SHALL NOT replace the database. After a schema-changing update the previous version SHALL NOT be offered as a rollback; the status SHALL explain that returning to it requires restoring the pre-update backup. Boot recovery MUST NOT restore a backup automatically, because traffic may already have used the newer schema.

#### Scenario: A release adds migrations
- **WHEN** the administrator applies go-v1.1.0 with schema 40 to an installation on schema 33
- **THEN** the backup is taken, go-v1.1.0 starts and migrates, and the installation runs go-v1.1.0
- **AND** the dashboard shows that rollback to go-v1.0.11 requires restoring the pre-update backup

#### Scenario: The migrating version fails to start
- **WHEN** go-v1.1.0 migrates the database and then fails startup verification
- **THEN** the pre-update backup replaces the database and go-v1.0.11 starts on it with the failure reported

## MODIFIED Requirements

### Requirement: Verify artifacts before switching
Installation SHALL use bounded HTTPS downloads from the fixed release origin, verify SHA-256, reject unsafe archive paths and links, and validate the executable's version, platform and update protocol. It SHALL accept a candidate whose database schema is equal to or newer than the running schema and reject an older schema before stopping the current server; rollback and boot recovery SHALL require an equal schema. Invalid downloads MUST NOT replace a verified executable or be run before integrity verification.

#### Scenario: Corrupt or incompatible release
- **WHEN** a checksum, archive, executable version, platform, protocol or schema check fails
- **THEN** the administrator receives a retryable failure and the current server continues serving

#### Scenario: A retained executable was changed
- **WHEN** a retained executable fails integrity verification on restart
- **THEN** it is not executed, including for descriptor inspection
- **AND** recovery may use only another verified compatible retained version
