## MODIFIED Requirements

### Requirement: Streaming account-capacity waits keep clients alive

When a streaming Responses request waits for temporary account capacity to recover before account selection can continue, the proxy MUST emit downstream progress events during the wait. HTTP/SSE and HTTP bridge streams MUST emit `codex.keepalive` events with `status = "waiting_for_account_capacity"`, request id, elapsed wait seconds, and retry-after seconds when known. HTTP bridge streams MAY also emit `response.in_progress` to satisfy OpenAI Responses stream parsers before later terminal events. WebSocket clients MUST receive equivalent `codex.keepalive` JSON messages. These progress events MUST NOT expose account emails, API keys, raw affinity keys, prompt content, or request payloads. Contract-shaped streams remain subject to the direct capacity-wait progress requirement, which suppresses non-standard progress events before startup when both HTTP error propagation and the OpenAI SDK stream contract are enabled. A startup probe that observes a capacity-wait marker MUST remain bounded by its existing signal-discovery deadline; if the paired ready signal does not arrive by that deadline, the probe MUST hand the still-running stream to the HTTP response instead of withholding response startup indefinitely.

#### Scenario: HTTP/SSE capacity wait emits keepalive

- **WHEN** `/v1/responses` streaming account selection can recover after a retry hint
- **THEN** the stream emits `codex.keepalive` with `status = "waiting_for_account_capacity"`
- **AND** includes the request id, waited seconds, and bounded retry-after seconds

#### Scenario: HTTP bridge capacity wait preserves parser progress

- **WHEN** an HTTP responses bridge request waits for session creation or account selection capacity
- **THEN** the bridge stream emits a capacity-wait keepalive
- **AND** emits OpenAI-compatible in-progress events when needed so downstream Responses stream parsers do not time out before the terminal response

#### Scenario: WebSocket capacity wait emits JSON keepalive

- **WHEN** a WebSocket Responses request waits for account capacity recovery
- **THEN** the downstream WebSocket receives a JSON `codex.keepalive` message with `status = "waiting_for_account_capacity"`
- **AND** the connection remains open until selection retries, the request budget expires, or the client disconnects

#### Scenario: Capacity startup marker cannot withhold HTTP response indefinitely

- **GIVEN** a streaming Responses startup probe observes an account-capacity wait before the first upstream item
- **AND** the paired capacity-ready signal does not arrive
- **WHEN** the startup signal-discovery deadline expires
- **THEN** the probe hands the still-running stream to the HTTP response
- **AND** the downstream can receive its initial heartbeat and subsequent capacity-wait progress
- **AND** the underlying capacity wait remains bounded by the original request budget
