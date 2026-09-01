## ADDED Requirements

### Requirement: Active hard-owner failover requires upstream quota evidence

An active Responses thread that depends on a previous-response owner or another hard continuity owner MUST remain bound to that account until OpenAI upstream returns an error event or response that is explicitly classified as quota exhaustion. Local usage snapshots, persisted `RATE_LIMITED` or `QUOTA_EXCEEDED` status, local capacity, transient health, transport failure, overload, model rejection, authentication failure, and generic rate limiting MUST NOT authorize cross-account replay.

After an upstream quota rejection, the proxy MAY transfer the turn only when its existing replay-safety proof produces an account-neutral, self-contained request. If transfer is not authorized, safe, or possible, the proxy MUST preserve the original upstream error code and message and MUST NOT replace it with an owner-unavailable error. Direct WebSocket, HTTP-bridge, and HTTP/SSE Responses transports MUST enforce the same rule.

#### Scenario: Local zero-percent quota does not move an active thread

- **GIVEN** an active hard-continuity thread is owned by account A
- **AND** local usage data reports 100% used or account A is locally marked rate-limited or quota-exceeded
- **WHEN** no quota error has been received from OpenAI upstream for the turn
- **THEN** the proxy keeps the turn on account A
- **AND** it does not prepare or dispatch a replay on account B

#### Scenario: Non-quota upstream failure remains owner-bound

- **GIVEN** an active hard-continuity turn is sent through account A
- **WHEN** OpenAI upstream returns an overload, model, authentication, transport, or generic rate-limit failure that is not classified as quota exhaustion
- **THEN** the proxy does not transfer the turn to account B
- **AND** the client and request log retain the normalized upstream error

#### Scenario: Verified upstream quota rejection permits safe transfer

- **GIVEN** an active hard-continuity turn is sent through account A
- **AND** OpenAI upstream returns a quota-exhaustion error before visible output
- **AND** the proxy proves that a self-contained account-neutral replay is safe
- **WHEN** account B is eligible
- **THEN** the proxy may exclude account A and replay the turn through account B
- **AND** no account-scoped continuity token from account A is sent to account B

#### Scenario: Unsafe quota replay preserves the upstream error

- **GIVEN** OpenAI upstream returns a quota-exhaustion error for an active hard-continuity turn on account A
- **AND** the request cannot be proven safe for account-neutral replay or no replacement account is eligible
- **WHEN** the turn terminates
- **THEN** the proxy keeps the thread owner-bound
- **AND** it returns and records the original normalized upstream quota error instead of `previous_response_owner_unavailable`
