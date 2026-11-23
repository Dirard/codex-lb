## ADDED Requirements

### Requirement: Writes serialize across processes of one installation
Every write transaction of the writer and maintenance connections SHALL begin `IMMEDIATE`, so concurrent writers of two worker processes wait on the busy timeout instead of failing a read-then-write transaction with a stale snapshot.

#### Scenario: Two workers settle requests at once
- **WHEN** two worker processes reserve and settle usage concurrently on one database
- **THEN** every transaction commits and no settlement fails because the other process wrote first

### Requirement: Interrupted reservations belong to dead processes
A new usage reservation SHALL record the instance id of the process that created it. At startup a worker SHALL mark as needing reconciliation only `reserved` rows whose owner is not a live worker of the installation; rows without an owner SHALL count as not alive. When a worker exits, the active worker SHALL apply the same rule.

#### Scenario: A new worker starts beside a serving one
- **WHEN** a worker starts while the previous worker has reservations in flight
- **THEN** those reservations stay unflagged and settle normally

#### Scenario: The previous worker is killed mid-request
- **WHEN** a retiring worker exits with an unsettled reservation
- **THEN** that reservation is marked as interrupted usage

### Requirement: Older code may open an additively migrated database
Each migration SHALL be classified as additive or not. Migrating SHALL record the lowest code schema that may operate on the database. Code whose schema is older than the database but not older than that floor SHALL open it without migrating or rewriting the schema version; otherwise it SHALL refuse the database as before.

#### Scenario: A newer additive schema
- **WHEN** a schema-41 binary opens a schema-42 database whose recorded floor is 40
- **THEN** it opens and serves without changing the schema version

#### Scenario: A newer breaking schema
- **WHEN** the recorded floor is above the binary's schema
- **THEN** the binary refuses the database

### Requirement: A lost token-refresh race uses the winner's token
When an OAuth refresh fails permanently but the stored refresh token of the same account generation changed meanwhile, the refresh SHALL return the stored current access token and SHALL NOT mark the account for re-authentication.

#### Scenario: Two processes refresh one account
- **WHEN** two workers refresh the same account token at the same moment and the provider rejects the second as reused
- **THEN** the second request continues with the token the first worker stored
