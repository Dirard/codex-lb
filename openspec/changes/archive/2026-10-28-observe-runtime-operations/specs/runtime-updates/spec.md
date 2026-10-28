## ADDED Requirements

### Requirement: A replaced bootstrap executable takes effect

When the managed runtime starts from a bootstrap executable whose digest differs from the recorded bootstrap digest, it SHALL adopt that executable as the current version whether its version is newer or older, retaining the previously current version as the rollback candidate, and SHALL log the bootstrap and selected versions. Restarting with the recorded bootstrap MUST keep the current selection, including an administrator's rollback. State without a recorded bootstrap digest SHALL keep adopting only newer bootstraps. Adoption MUST NOT restore or migrate data; an older binary still refuses a newer database schema.

#### Scenario: Emergency downgrade by binary replacement
- **WHEN** an operator replaces go-v1.0.4 with the go-v1.0.2 binary and restarts the service
- **THEN** go-v1.0.2 runs, go-v1.0.4 is retained as the previous version, and the launcher logs the selection
- **AND** restarting again with the same go-v1.0.2 binary keeps that selection
