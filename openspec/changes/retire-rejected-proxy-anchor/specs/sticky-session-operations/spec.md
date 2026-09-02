## ADDED Requirements

### Requirement: Rejected proxy anchors are retired from live continuity

When OpenAI upstream returns `previous_response_not_found` for a response ID that the proxy injected from direct-WebSocket session continuity, the proxy MUST stop injecting that exact response ID into later requests for the same continuity state. The proxy MUST clear the anchor only when the live state still references the rejected ID, MUST preserve any newer completed response, and MUST NOT clear a client-supplied response chain under this rule.

The failed turn MUST retain the normalized upstream error. A later self-contained request MUST be allowed to proceed without the rejected proxy anchor and without requiring a proxy restart.

#### Scenario: Rejected proxy anchor is not injected again

- **GIVEN** direct-WebSocket continuity injects response A into a later request
- **WHEN** OpenAI returns `previous_response_not_found` for response A
- **THEN** the proxy removes response A and its dependent continuity metadata from the live state
- **AND** the next self-contained request is sent without response A

#### Scenario: Newer completion wins a stale rejection race

- **GIVEN** a request using injected response A remains in flight
- **AND** another request records newer completed response B in the same continuity state
- **WHEN** the first request receives `previous_response_not_found` for response A
- **THEN** the proxy preserves response B and its continuity metadata

#### Scenario: Client-supplied invalid response remains explicit

- **GIVEN** the client explicitly supplies response A
- **WHEN** OpenAI returns `previous_response_not_found`
- **THEN** the proxy returns the normalized error
- **AND** it does not mutate live continuity under the proxy-anchor retirement rule
