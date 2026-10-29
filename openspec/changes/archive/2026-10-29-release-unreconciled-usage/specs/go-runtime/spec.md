## ADDED Requirements

### Requirement: Unreconciled usage is released after one hour

A reservation flagged for reconciliation (interrupted owner or unreported usage) SHALL stay reserved for one hour after its creation, during which the offline reconciliation command can still settle it. After that the active worker SHALL release it without charging through the ordinary release settlement, refunding limit deltas only within their current window and recording no usage event, in bounded batches during its maintenance pass. Reservations that are not flagged SHALL never be released this way.

#### Scenario: A backlog of interrupted usage
- **WHEN** a database holds flagged reservations older than one hour
- **THEN** maintenance releases them in batches, they disappear from pending request-log entries, and reports and key usage totals do not change

#### Scenario: A request still in flight
- **WHEN** a reservation older than one hour is not flagged because its request is still running
- **THEN** it stays reserved and is settled by its request
