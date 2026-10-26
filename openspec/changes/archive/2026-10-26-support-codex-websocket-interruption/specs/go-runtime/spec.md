## ADDED Requirements

### Requirement: Codex interruption preserves the active response and its accounting

Authenticated Responses WebSockets, including canonical and trailing-slash Codex and v1 routes, SHALL accept `response.interrupt` with a nonempty bounded `response_id` and `mode: discard_partial_items`. A valid command for the connection's current ChatGPT response SHALL be relayed to that response's exact leased upstream WebSocket. The runtime MUST NOT substitute context cancellation, fabricate acceptance or move the request to another account. Actual interruption acknowledgements, output-item interruption events and the terminal response SHALL reach the client, and reported terminal usage SHALL settle once through the existing ledger.

#### Scenario: User input interrupts an active response
- **WHEN** Codex sends a valid interruption after response creation
- **THEN** upstream receives the interruption on the socket executing that response
- **AND** the client receives the actual acknowledgement and terminal with actual usage
- **AND** a subsequent explicit response.create can continue the interrupted response on its retained owner

#### Scenario: Stale or foreign interruption target
- **WHEN** the response ID is not the connection's current response, the response has ended, or interruption is unsupported by the selected provider transport
- **THEN** the client receives response.interrupt.failed identifying that target
- **AND** the runtime does not cancel another response, dispatch a new request or change an account binding

#### Scenario: Malformed control message or revoked key
- **WHEN** an interruption has invalid fields or the proxy key is no longer authorized
- **THEN** the existing validation and authorization error semantics apply without forwarding the control
- **AND** short interruption messages do not acquire a large-body permit

### Requirement: Interruption acknowledgement races cannot contaminate reused sockets

The runtime SHALL bound pending interruption controls to the active response. When a terminal arrives after an interruption was sent but before its acknowledgement, the runtime SHALL retain terminal usage and wait at most five seconds for the matching acknowledgement before retiring that upstream socket. A queued control not yet sent when the response terminates SHALL receive a local response.interrupt.failed, not be sent on a released socket. Missing or invalid acknowledgement MUST NOT discard known terminal usage, leak an acknowledgement to the next response, automatically replay a request or count as quota exhaustion. Disconnect and existing response.cancel SHALL retain bounded cleanup.

#### Scenario: Terminal precedes acknowledgement
- **WHEN** upstream sends terminal usage before acknowledging a sent interruption
- **THEN** the matching acknowledgement and terminal are delivered without releasing the socket early
- **AND** the next response receives none of the previous response's control events

#### Scenario: Acknowledgement never arrives
- **WHEN** terminal usage is known but the acknowledgement deadline expires
- **THEN** the runtime reports interruption failure, preserves terminal accounting and retires only that upstream connection

#### Scenario: Upstream rejects interruption
- **WHEN** upstream emits response.interrupt.failed
- **THEN** it is forwarded as a control acknowledgement rather than a fatal generic response error
- **AND** the current response can still reach its terminal event
