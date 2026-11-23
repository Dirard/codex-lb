## ADDED Requirements

### Requirement: Native HTTP preserves connection and response-read deadline budgets

When native HTTP receives both connection and response-head read budgets, its first-response watchdog MUST use the smaller of the overall request budget and the sum of those two phase budgets. If either phase budget is absent, the watchdog MUST use the overall request budget. A response-head stall MUST still terminate within that finite envelope. Any timeout after the native request command may have been dispatched MUST remain non-replayable, and the change MUST NOT alter TLS verification, endpoint selection, or helper availability fallback.

#### Scenario: Slow valid connection does not consume the read allowance

- **GIVEN** connection establishment takes longer than the configured response-read budget but less than the configured connection budget
- **WHEN** upstream returns response headers within the subsequent response-read allowance
- **THEN** the native request succeeds
- **AND** it is not cancelled at the response-read duration measured from IPC command dispatch

#### Scenario: Response head remains bounded after dispatch

- **GIVEN** the native connection is established and the request is dispatched
- **WHEN** upstream does not return response headers
- **THEN** the native adapter cancels the request no later than the combined connection and response-read envelope or the overall request deadline, whichever is earlier
- **AND** the failure does not gain replay eligibility

#### Scenario: Helper protocol stays compatible

- **WHEN** the corrected deadline is applied
- **THEN** the existing protocol version and capability handshake remain unchanged
- **AND** no post-dispatch fallback to Python is introduced

### Requirement: Native WebSocket terminal dispatch errors release task ownership

When the helper cannot enqueue a WebSocket command because that socket's bounded command channel is full or closed, it MUST treat the emitted `websocket_error` as terminal for that socket. The helper MUST remove and stop the affected active task, MUST NOT replay any queued frame, and MUST leave every other multiplexed HTTP or WebSocket operation running.

#### Scenario: Full command channel aborts only its socket

- **GIVEN** one native WebSocket command channel is full while another socket is active
- **WHEN** the helper rejects an additional command with a terminal `websocket_error`
- **THEN** the full socket is removed from the active registry and its task is aborted
- **AND** the other socket remains active
- **AND** no rejected or queued frame is replayed
