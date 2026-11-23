## ADDED Requirements

### Requirement: Independent Responses sharing a session can run concurrently
The runtime SHALL allow independent Responses requests sharing a logical session to execute concurrently within its bounded capacity. Each active upstream WebSocket SHALL carry at most one response.create at a time. Session sharing alone MUST NOT cause a per-conversation capacity denial while an independent connection can be admitted.

#### Scenario: Parallel full-context subagents
- **WHEN** independent requests without previous_response_id share the same authenticated logical session
- **AND** stream, account and connection admission permit them
- **THEN** each request can reach upstream without waiting for the sibling's terminal event
- **AND** each client receives only its own events and final accounting

#### Scenario: A branch continues on its exact connection
- **WHEN** a request continues a retained response while multiple sockets exist for the logical session
- **THEN** it uses the response's exact authenticated connection scope
- **AND** an arbitrary sibling socket cannot substitute its context
- **AND** sequential continuation retains connection reuse

#### Scenario: An exact anchor disappears while waiting
- **WHEN** a pending continuation's exact connection closes or advances beyond its retained response before that continuation is dispatched
- **THEN** the runtime detects the lost anchor before response.create and releases the unused reservation
- **AND** only an existing safe same-owner reconstruction may recover it, without replaying a possibly executed request

#### Scenario: A sibling is cancelled or its connection is retired
- **WHEN** one parallel attempt is cancelled or loses its connection
- **THEN** the other active connections and their response ownership remain usable
- **AND** cleanup releases only the affected attempt's resources
- **AND** no possibly executed attempt is automatically repeated

#### Scenario: Actual upstream connection capacity is exhausted
- **WHEN** every bounded upstream connection is active and no idle connection can be reused or evicted
- **THEN** excess work is rejected before provider execution without evicting an active peer
- **AND** this local rejection neither proves provider quota exhaustion nor authorizes account migration

### Requirement: Idle client WebSockets do not consume HTTP body-reader capacity
The runtime SHALL bound downstream WebSocket lifetimes independently from concurrent HTTP request-body reads and SHALL support at least 256 admitted downstream WebSockets. Idle sockets MUST NOT exhaust HTTP body-reader slots. Application stream, account, authorization and accounting rules SHALL still govern every response.create.

#### Scenario: HTTP remains usable beside idle subagent connections
- **WHEN** 256 authenticated client WebSockets remain open without active requests
- **THEN** an otherwise eligible HTTP Responses request can still read its body and execute
- **AND** an excess socket is bounded independently from that HTTP request

#### Scenario: Socket admission is released
- **WHEN** an upgrade fails, a client disconnects or the runtime drains
- **THEN** its downstream connection slot is released without weakening authentication or per-request admission
