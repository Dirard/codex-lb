## ADDED Requirements

### Requirement: Native WebSocket waits preserve client liveness without committing output
The runtime SHALL send application-text heartbeats every ten seconds during a pending native `/backend-api/codex/responses` WebSocket turn and its trailing-slash equivalent. It SHALL use `codex.keepalive` when the current attempt has no real response ID and `response.in_progress` with the real ID when known. Heartbeats MUST NOT change visible-output classification, token usage, diagnostics, ownership or quota-retry eligibility. Attempt changes MUST clear the previous response ID. Generic `/v1/responses` routes MUST NOT receive synthetic heartbeats. Completion, cancellation and disconnect MUST stop and join request-owned heartbeat/worker activity with bounded downstream writes.

#### Scenario: Slow native reasoning remains connected
- **WHEN** a native WebSocket upstream is silent before or after response.created
- **THEN** the client SHALL receive application-text heartbeats without fabricated response IDs or usage
- **AND** real response events SHALL retain their order and a terminal event SHALL end heartbeats

#### Scenario: Quota refusal follows a heartbeat
- **WHEN** the current attempt explicitly refuses quota without output or billable usage after a heartbeat
- **THEN** existing safe failover SHALL remain possible and the previous attempt ID SHALL NOT be reused in subsequent heartbeats

#### Scenario: A generic WebSocket waits
- **WHEN** a public v1 WebSocket request is pending
- **THEN** it SHALL receive only real response events or the existing error envelope, not synthetic native heartbeats

### Requirement: Active upstream WebSocket transport has bounded liveness checks
The runtime SHALL probe an active upstream WebSocket every thirty seconds with at most ten seconds to receive pong, independently of downstream heartbeats. Protocol pong MUST NOT count as model progress or extend the overall response deadline. An unresponsive connection after dispatch MUST be closed without automatic replay, quota refusal or owner migration, and existing unknown-usage reconciliation SHALL apply. Probe tasks MUST stop and be joined on completion and cancellation.

#### Scenario: Responsive upstream performs long reasoning
- **WHEN** the upstream responds to protocol pings but emits no application events
- **THEN** the request SHALL remain pending within the existing overall response deadline

#### Scenario: Upstream stops answering pings
- **WHEN** an active upstream fails a bounded ping after response.create was sent
- **THEN** the attempt SHALL terminate as a transport failure without another dispatch or fabricated zero usage
