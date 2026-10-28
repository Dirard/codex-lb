## ADDED Requirements

### Requirement: Pooled upstream WebSockets deliver frames only to their own response

The runtime SHALL hand an upstream WebSocket frame only to the response whose response.create is currently in flight on that socket. A frame read while no response owns the socket, or after the owning response stopped reading, SHALL be dropped and SHALL cause every later reuse of that socket to be preceded by a successful protocol ping round trip. An event announcing or terminating a response whose ID differs from the current response, a second response.created with another ID, or a terminal repeating the socket's previous response ID SHALL fail the attempt as an invalid stream and retire the socket; such a terminal MUST NOT settle the request, and the existing unknown-usage reconciliation SHALL apply.

#### Scenario: A frame arrives after a terminal while the socket is idle
- **WHEN** upstream sends frames after a response's terminal event and the socket is reused
- **THEN** the next response SHALL receive none of those frames
- **AND** the socket SHALL be reused only after a successful ping, keeping its exact continuation

#### Scenario: A terminal belongs to another response
- **WHEN** a leased response receives a terminal whose response ID differs from its response.created or equals the previous response on the socket
- **THEN** the attempt SHALL fail as an invalid stream without emitting that terminal or settling its usage
- **AND** the next request SHALL use a new upstream connection

### Requirement: Reused idle upstream WebSockets are proven alive before dispatch

Before writing response.create on a pooled upstream WebSocket that has been idle for at least twenty seconds, or that sent unsolicited frames, the runtime SHALL ping it under the lease with at most three seconds to receive pong. On failure it SHALL discard the socket; a request without previous_response_id SHALL continue on another socket or a new handshake, and an exact continuation SHALL be rejected as continuation-not-found before execution. A recently used socket without unsolicited frames SHALL be reused without an extra round trip. Request cancellation during this check SHALL release the lease without discarding the socket. The response.create write SHALL be bounded by thirty seconds.

#### Scenario: An idle socket died silently
- **WHEN** a pooled socket stopped answering while idle and a new request leases it
- **THEN** the request SHALL be sent on a new upstream connection within the ping bound
- **AND** no response.create SHALL be written on the dead socket

#### Scenario: An exact continuation anchor died while idle
- **WHEN** a continuation's exact socket fails the liveness check
- **THEN** the request SHALL be rejected before execution and its unused reservation released
- **AND** it MUST NOT move to another connection or be replayed

#### Scenario: A warm socket is reused
- **WHEN** a socket is reused shortly after its previous response without unsolicited frames
- **THEN** response.create SHALL be written without a preceding ping

### Requirement: Upstream HTTP connections are health-checked and reusable

The shared upstream HTTP transport SHALL send HTTP/2 health pings after thirty seconds without frames, fail a connection whose ping is unanswered for ten seconds or whose writes stall for thirty seconds, limit the per-stream receive buffer to 1 MiB, and resume TLS sessions from a bounded client session cache. After a Responses terminal event or a Chat `[DONE]` marker, an HTTP/1.x response body SHALL be drained for at most 64 KiB and 100 milliseconds before closing so that a keep-alive connection can be reused; draining MUST NOT change the result.

#### Scenario: HTTP/1.1 body ends shortly after the terminal
- **WHEN** an HTTP/1.1 upstream ends each stream body a few milliseconds after its terminal event
- **THEN** sequential streams SHALL reuse one upstream connection

#### Scenario: Upstream keeps the body open after the terminal
- **WHEN** the body does not end after the terminal
- **THEN** the stream SHALL complete with its terminal result after the drain bound

### Requirement: Upstream streams are bounded by size and time

Upstream SSE and WebSocket response streams SHALL be bounded by 256 MiB of total event payload per response, the existing 16 MiB per-event bound and the overall response deadline, not by an event count. Exceeding the payload bound SHALL fail the stream as incomplete.

#### Scenario: A long response has many small events
- **WHEN** a healthy upstream sends more than one hundred thousand small events before its terminal
- **THEN** the response SHALL complete with its terminal usage

### Requirement: Native Codex SSE waits keep the client idle timer alive

During a dispatched native `/backend-api/codex/responses` HTTP/SSE response and its trailing-slash equivalent, the runtime SHALL send a heartbeat as an SSE data event after ten seconds without any real downstream write: `response.in_progress` with the real current response ID when known, otherwise `codex.keepalive`. Generic `/v1/responses` SSE SHALL keep SSE comment heartbeats. Heartbeats SHALL NOT be sent before dispatch, and MUST NOT change visible-output classification, usage, ownership or retry eligibility.

#### Scenario: Native SSE reasoning is silent
- **WHEN** a dispatched native SSE response emits nothing for more than ten seconds
- **THEN** the client SHALL receive a data-event heartbeat without fabricated usage

#### Scenario: Events flow continuously
- **WHEN** real events are written at least every ten seconds
- **THEN** no heartbeat SHALL be inserted between them

#### Scenario: A generic SSE client waits
- **WHEN** a dispatched `/v1/responses` SSE response is silent
- **THEN** it SHALL receive only SSE comment heartbeats

### Requirement: Downstream WebSocket turns carry protocol pings

During an active Responses WebSocket turn on every Responses WebSocket route, the runtime SHALL send a protocol ping every thirty seconds. The ping is transport traffic, not an application event; turn completion MUST NOT cancel an in-flight ping write, and pings SHALL stop when the connection handler exits.

#### Scenario: A generic WebSocket turn is silent
- **WHEN** a `/v1/responses` WebSocket turn waits longer than the ping interval
- **THEN** the client SHALL observe protocol pings and no synthetic application events

### Requirement: Chat-source function calls stream before completion

For Chat-compatible sources, the runtime SHALL accumulate tool-call arguments in linear time. When the first arguments delta of a declared non-custom function tool arrives with a known call ID and name, the runtime SHALL emit `response.output_item.added` and stream `response.function_call_arguments.delta` before the upstream `[DONE]`; custom tools and calls without ID or name SHALL keep terminal-time emission. A call whose name or ID changes after streaming started, an undeclared tool or a duplicate call ID SHALL fail the response as an invalid tool call, and the completed item SHALL keep the streamed item ID.

#### Scenario: A long function call is generated
- **WHEN** a Chat source streams a declared function call in many argument deltas
- **THEN** the client SHALL receive the added item and argument deltas before the completed response
- **AND** the final item SHALL contain the complete arguments

#### Scenario: A streamed call changes its name
- **WHEN** a name fragment arrives after the call started streaming
- **THEN** the response SHALL fail as an invalid tool call

### Requirement: Realtime sessions bound writes and detect dead peers

Codex Realtime proxying SHALL bound each write in either direction by thirty seconds; a failed write SHALL end the session as a disconnect with joined pump activity. The upstream socket SHALL be protocol-pinged after thirty seconds without received frames with ten seconds to receive pong, and an unanswered ping while nothing was received and no frame waits for the client SHALL end the session as a disconnect. The downstream socket SHALL receive protocol pings every thirty seconds as intermediary keepalive.

#### Scenario: A realtime client stops reading
- **WHEN** the downstream realtime client stops reading while upstream sends frames
- **THEN** the session SHALL end within the write bound

#### Scenario: The realtime upstream goes silent and stops answering
- **WHEN** the upstream sends nothing and does not answer a ping
- **THEN** the session SHALL end as a realtime disconnect within the ping bound

### Requirement: Operation requests resolve client identity like Responses

Compact, file, transcription, control and realtime operation requests SHALL resolve the client IP through the configured trusted proxies exactly as Responses requests do, and SHALL derive the user-agent group from the User-Agent product token rather than a client-supplied group header.

#### Scenario: Compact behind a trusted proxy
- **WHEN** a compact request arrives through a trusted proxy with a forwarded client address
- **THEN** the operation SHALL be attributed to the forwarded client address

### Requirement: Native chat completions render incomplete Responses terminals

Streaming `/v1/chat/completions` over a Responses source SHALL render `response.incomplete` as a final chunk with `finish_reason` `length`, or `content_filter` when that is the incomplete reason, followed by optional usage and `[DONE]`; every chunk SHALL carry `created` and `model`. Accounting of the attempt SHALL be unchanged.

#### Scenario: Output limit reached after partial content
- **WHEN** a Responses source ends a chat-translated stream with response.incomplete for max_output_tokens
- **THEN** the client SHALL receive the partial content, a `length` finish reason and `[DONE]` instead of an error chunk

## MODIFIED Requirements

### Requirement: Active upstream WebSocket transport has bounded liveness checks
The runtime SHALL probe an active upstream WebSocket every thirty seconds with at most ten seconds to receive pong, independently of downstream heartbeats. Any frame received from upstream SHALL also count as transport liveness, so a socket that received a frame within the interval SHALL NOT be pinged, and the probe SHALL NOT fail while the socket reader waits for a slow consumer to accept a frame or when a frame arrived after the ping was sent; the downstream write bound limits that wait. Protocol pong MUST NOT count as model progress or extend the overall response deadline. A ping SHALL be bounded only by its own timeout and MUST NOT be cancelled by lease completion or request cancellation; stopping a probe SHALL NOT wait for pong. An unresponsive connection after dispatch MUST be closed without automatic replay, quota refusal or owner migration, and existing unknown-usage reconciliation SHALL apply. Probe tasks MUST stop and be joined on completion and cancellation, and in-flight pings SHALL be joined when the upstream adapter closes.

#### Scenario: Responsive upstream performs long reasoning
- **WHEN** the upstream responds to protocol pings but emits no application events
- **THEN** the request SHALL remain pending within the existing overall response deadline

#### Scenario: Upstream stops answering pings
- **WHEN** an active upstream fails a bounded ping after response.create was sent and sends no frame
- **THEN** the attempt SHALL terminate as a transport failure without another dispatch or fabricated zero usage

#### Scenario: A downstream client is slower than the probe
- **WHEN** a healthy upstream streams faster than the client accepts events, within the downstream write bound
- **THEN** the response SHALL complete with its actual usage

#### Scenario: A response completes while a ping is in flight
- **WHEN** the terminal arrives while a liveness ping is being written or awaiting pong
- **THEN** the owner socket SHALL remain usable for the next continuation

### Requirement: Native WebSocket waits preserve client liveness without committing output
The runtime SHALL send application-text heartbeats during a pending native `/backend-api/codex/responses` WebSocket turn and its trailing-slash equivalent after every ten seconds without a real downstream write. It SHALL use `codex.keepalive` when the current attempt has no real response ID and `response.in_progress` with the real ID when known. Heartbeats MUST NOT change visible-output classification, token usage, diagnostics, ownership or quota-retry eligibility. Attempt changes MUST clear the previous response ID. Generic `/v1/responses` routes MUST NOT receive synthetic heartbeats. Completion, cancellation and disconnect MUST stop and join request-owned heartbeat/worker activity with bounded downstream writes.

#### Scenario: Slow native reasoning remains connected
- **WHEN** a native WebSocket upstream is silent before or after response.created
- **THEN** the client SHALL receive application-text heartbeats without fabricated response IDs or usage
- **AND** real response events SHALL retain their order and a terminal event SHALL end heartbeats

#### Scenario: Continuous output needs no heartbeat
- **WHEN** real events are written at least every ten seconds
- **THEN** no heartbeat SHALL be inserted between them

#### Scenario: Quota refusal follows a heartbeat
- **WHEN** the current attempt explicitly refuses quota without output or billable usage after a heartbeat
- **THEN** existing safe failover SHALL remain possible and the previous attempt ID SHALL NOT be reused in subsequent heartbeats

#### Scenario: A generic WebSocket waits
- **WHEN** a public v1 WebSocket request is pending
- **THEN** it SHALL receive only real response events or the existing error envelope, not synthetic native heartbeats
