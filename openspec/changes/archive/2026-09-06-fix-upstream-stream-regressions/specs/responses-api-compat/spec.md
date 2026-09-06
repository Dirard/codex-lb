## MODIFIED Requirements

### Requirement: Native Codex preserves upstream failure lifecycle

For a native Codex HTTP/SSE Responses request, an upstream transport timeout or stream EOF without a terminal Responses event MUST remain a failure while preserving valid downstream framing. A failure observed by the startup probe before the downstream response is committed MUST retain the existing non-success HTTP error response, unless existing explicitly eligible server recovery owns that failure. After HTTP response commitment, the proxy MUST emit the existing terminal `response.failed` shape and finish the HTTP body instead of raising a transport error out of the downstream body iterator. The proxy MUST NOT manufacture a successful terminal event or add replay eligibility for an ambiguously dispatched upstream request. Reservation, request-log, account-health, and owned-resource cleanup MUST still complete under their existing ownership rules. Native requests MUST retain the existing downstream initial and periodic liveness frames. Non-native and OpenAI-compatible clients MUST retain the existing stable terminal-error shaping.

#### Scenario: Native Codex sees a truncated upstream SSE lifecycle

- **GIVEN** a native Codex HTTP request has received a non-terminal SSE event
- **WHEN** upstream closes without a terminal event
- **THEN** downstream receives a terminal `response.failed` and a complete HTTP body
- **AND** proxy cleanup and failure accounting still complete without replaying the request

#### Scenario: A transport failure is known before HTTP startup

- **WHEN** the startup probe observes a native transport failure before committing the response and no existing eligible server recovery owns it
- **THEN** the proxy returns the existing non-success HTTP error instead of `200 OK` with an aborted body

#### Scenario: Marked synthetic transport errors retain their diagnosis

- **WHEN** a started native stream receives an internally marked synthetic transport-failure event
- **THEN** the proxy removes the internal marker and sends the structured terminal failure without aborting the HTTP body

#### Scenario: Native upstream silence retains downstream liveness

- **WHEN** a native HTTP Responses stream waits longer than the configured keepalive interval for an upstream event
- **THEN** the existing downstream keepalive is emitted without adding an upstream request

#### Scenario: Non-native client keeps the terminal umbrella

- **GIVEN** an OpenAI SDK or other non-native client receives the same upstream truncation
- **WHEN** codex-lb normalizes the stream
- **THEN** the client receives the existing terminal `response.failed` shape
