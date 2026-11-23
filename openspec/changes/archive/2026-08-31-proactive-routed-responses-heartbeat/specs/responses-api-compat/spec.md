## ADDED Requirements

### Requirement: Routed upstream Responses heartbeats precede idle expiry

The proxy MUST configure routed upstream Responses WebSockets with a heartbeat cadence no greater than 30 seconds and strictly shorter than the downstream WebSocket idle budget. The routed heartbeat MUST NOT shorten the downstream client idle budget, change direct transport behavior, or require a new operator setting.

#### Scenario: Routed idle connection remains open through proactive pings

- **GIVEN** a routed upstream Responses WebSocket is otherwise idle and the downstream idle budget is 120 seconds
- **WHEN** the connection is established
- **THEN** the routed transport uses a heartbeat cadence no greater than 30 seconds
- **AND** the downstream idle budget remains 120 seconds

#### Scenario: Direct transport keeps native ping behavior

- **WHEN** an equivalent direct upstream Responses WebSocket is established
- **THEN** the proxy leaves the direct transport's native ping interval unchanged
- **AND** its existing pong-timeout failure classification remains unchanged
