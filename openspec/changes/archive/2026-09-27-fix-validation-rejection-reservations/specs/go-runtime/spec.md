## ADDED Requirements

### Requirement: Definitive request validation rejection releases an unused reservation

An ordinary Responses or translated Chat request receiving an actual upstream HTTP 400 or 422 with a valid `invalid_request_error` envelope and no output, nested accepted response, usage or duration SHALL settle its unused reservation as a failed zero-charge attempt. This classification MUST NOT be inferred from HTTP status alone, malformed/truncated error bodies, an in-stream error event or an error carrying billing/output evidence. It MUST NOT authorize replay, change account quota health, mark an account healthy or erase prior unknown reservations. Valid reported billing SHALL still settle once; incomplete reported billing and ambiguous dispatched failures SHALL remain pending.

#### Scenario: An unsupported parameter is refused before streaming
- **WHEN** a complete upstream HTTP 400 response declares `invalid_request_error` without billing or output
- **THEN** the client receives the error and the failed request releases only its own unused reserve
- **AND** the key can make a subsequent permitted request without a reconciliation lock caused by that refusal

#### Scenario: A rejection cannot prove the attempt unused
- **WHEN** an error is malformed, in-stream, not a validation envelope, or carries output, usage or duration
- **THEN** no definitive-zero classification is inferred from status and existing confirmed/unknown accounting rules remain in force
