## ADDED Requirements

### Requirement: Ambiguous HTTP bridge send settlement survives caller cancellation

After an HTTP bridge `response.create` send has started and reports an ambiguous transport failure, the proxy MUST finish the durable ambiguity update and release the request's local queue, admission, and reservation ownership before propagating caller cancellation. It MUST retire the affected upstream session and MUST NOT resend the ambiguously dispatched request. Failure of the durable ambiguity update MUST NOT prevent local ownership cleanup.

#### Scenario: Cancellation interrupts durable ambiguity marking

- **GIVEN** an HTTP bridge request may have been dispatched upstream
- **AND** its durable operation is being marked `unknown`
- **WHEN** caller cancellation arrives before that write completes
- **THEN** the proxy finishes the write attempt and local request cleanup before propagating cancellation
- **AND** the request is not sent upstream again

#### Scenario: Durable ambiguity marking fails

- **GIVEN** an HTTP bridge send reports an ambiguous transport failure
- **WHEN** persisting the operation as `unknown` fails
- **THEN** the proxy still releases the request's queue, admission, and reservation ownership
- **AND** the affected upstream session is retired without replaying the request

### Requirement: Pre-dispatch HTTP bridge cleanup survives caller cancellation

When an HTTP bridge request is rejected before upstream dispatch, the proxy MUST finish any durable operation rollback and release the request's local queue and admission ownership before propagating caller cancellation. It MUST preserve the original pre-dispatch classification and MUST NOT send the rejected request upstream.

#### Scenario: Cancellation interrupts pre-dispatch rollback

- **GIVEN** an HTTP bridge request is rejected before its upstream send begins
- **AND** its pre-dispatch operation rollback is in progress
- **WHEN** caller cancellation arrives before the rollback completes
- **THEN** the proxy finishes rollback and releases the response-create gate before propagating cancellation
- **AND** no upstream request is sent
