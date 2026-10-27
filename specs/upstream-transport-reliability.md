# Upstream transport reliability: pooled WebSocket isolation, transport liveness and cheaper streaming

- Last edited with skill pack: `0.2.2`

## Title and scope

Make a reused upstream WebSocket carry only its own response and fail only for real transport loss, and keep HTTP/SSE upstream connections alive and reusable. In the same slice, remove the per-event and per-request JSON work that dominates large-context latency, and fix the smaller stream-bound, heartbeat, realtime and operations transport defects found by the 2026-10-28 transport audit (`specs/audits/2026-10-28/04-transport.md`, NET-*).

## Planning anchor

- Entrypoints: `internal/adapters/upstream/websocket_sessions.go#acquire`, `#read`; `internal/adapters/upstream/responses_websocket.go#streamResponsesWebSocket`; `internal/adapters/upstream/websocket_probe.go#probeWebSocket`; `internal/adapters/upstream/responses_stream.go#streamResponses`; `internal/adapters/upstream/chat_stream.go#streamChat`; `cmd/codex-lb/runtime.go` (only the `http.Transport` literal); `internal/adapters/httpapi/proxy_sse.go#stream`; `internal/adapters/httpapi/proxy_websocket_stream.go#respondWebSocket`; `internal/adapters/httpapi/proxy_websocket.go#websocket`; `internal/adapters/httpapi/native_api.go#chatStream`; `internal/adapters/httpapi/codex_operations.go`; `internal/adapters/provider/{chatgpt_request.go,native_api.go,operations_realtime.go}`.
- Failing evidence: auditor probes in `.local/audit/scratch-transport/zz_audit_upstream_probe_test.go` (NET-1/2/3/10) and the CI race in `TestWebSocketCompletionStopsPendingProbeAndReusesConnection` (8/30 runs under `-race -cpu 2`).
- Changed assumptions:
  - A pooled upstream socket is not silent between responses; a frame can arrive while no response owns it.
  - Pong is not the only liveness signal and cannot be read while the single reader waits for a slow consumer.
  - An idle pooled socket can die silently (NAT/LB idle); reuse must prove liveness before `response.create`, the only point where failure is safely retryable.
  - Cancelling the context of an in-flight coder/websocket control-frame write closes the whole connection.
  - Codex CLI's SSE parser (`eventsource-stream`) drops comment lines, so `: keepalive` does not reset its 300 s idle timer (verified in `~/dev/ai-apps/codex/codex-rs/codex-api/src/sse/responses.rs` and `eventsource-stream-0.2.3/src/event_stream.rs`).
- Non-local: touches upstream, provider, httpapi transport files, one runtime literal and the realtime/operations handlers; accounting/ledger code is untouched.
- Impacted specs in `specs/`: none (`specs/spec-lifecycle/` is lifecycle policy). Action: create this spec.
- Impacted OpenSpec capability: `go-runtime` — action `amend` through `openspec/changes/harden-upstream-transport-reliability/` (ADDED/MODIFIED requirements below).

## Connected groups or observed existing logic

### Observed Existing Logic

1. Entry and orchestration: `streamResponsesWebSocket` acquires a socket lease, writes `response.create`, starts `probeWebSocket`, then consumes frames until a terminal; `release(keep, responseID)` returns the socket to the pool (idle up to 30 min) or discards it.
2. Socket reader: before this slice one reader per socket pushed every frame into a capacity-1 channel shared by all leases, also while idle; a stale frame was delivered to the next lease (probe: `deltas=[STALE from resp_1, turn resp_2]`). The reader blocked on that push, so a slow consumer stopped pong processing and the probe killed a healthy response.
3. Probe: ticker (30 s) → `Ping` with 10 s timeout derived from the lease context; `stopProbe` cancels that context. Cancellation during `writeControl` makes coder/websocket v1.8.15 close the connection.
4. HTTP upstream: `cmd/codex-lb/runtime.go` builds one `http.Transport` with `ForceAttemptHTTP2: true`, no `HTTP2Config` (no h2 health ping) and no TLS session cache. SSE streams stop reading at the terminal/`[DONE]` and close the body; on HTTP/1.1 a late body end forces a new TCP+TLS connection per request (probe: 20/20).
5. Chat sources: `chatStreamState.push` appends tool-call arguments with `+=` (quadratic) and emits all tool-call events only at the terminal.
6. Per-event cost: upstream decodes every SSE/WS event into `map[string]json.RawMessage` and copies it twice; `writeSSE` compacts with `json.Compact` and `fmt.Fprintf` (747 ns, 3 allocs); `readSSE` starts with a 64 KiB scanner buffer per stream.
7. Request bodies: transport code scans/re-encodes a Responses body about 15 times; every `json.Marshal(map[string]json.RawMessage)` re-compacts, sorts and HTML-escapes values. The WebSocket reader re-unmarshals the prepared payload into the already populated map.
8. Bounds and heartbeats: `maxSSEEvents = 100000` is applied to SSE and WS; SSE/WS heartbeats tick every 10 s regardless of real writes; there is no downstream protocol ping; realtime writes have no timeout and no ping.
9. Operations: `operationOptions` takes `ClientIP` from `RemoteAddr` (trusted proxies ignored) and `UserAgentGroup` from a client header; `CodexOperationsHandler.ServeHTTP` builds a mux per request.
10. Specs: `openspec/specs/go-runtime/spec.md` requirements "Active upstream WebSocket transport has bounded liveness checks", "Native WebSocket waits preserve client liveness without committing output", "Independent Responses sharing a session can run concurrently" and "Bound streaming and finalize local accounting once" govern this behavior and stay authoritative after amendment.

## Use cases

### 1. Pooled upstream WebSocket carries only its own response
idle pooled socket --frames read with no armed lease are dropped and mark the socket--> leased socket --arm lease and write response.create--> armed lease --deliver frames whose response identity matches--> terminal for this response --disarm and release--> idle pooled socket

Requirements:

  R1:
The upstream socket reader SHALL hand a frame only to the lease armed for the response.create currently in flight, through an unbuffered hand-off; a frame read while no lease is armed, or offered after that lease stopped reading, SHALL be dropped and SHALL mark the socket as having sent unsolicited frames.

  R2:
A socket marked by R1 SHALL be fenced by a successful ping round trip before every later response.create on it.

  R3:
A response.created or response.in_progress whose response ID differs from the current response, a second response.created with another ID, and a terminal whose response ID differs from the current response or equals the socket's previous response ID SHALL fail the attempt as `invalid_stream` and retire the socket; such a terminal MUST NOT settle the request.

Files And Functions:
 - existing: internal/adapters/upstream/websocket_sessions.go#websocketSession - lease pointer, liveness atomics
 - planned: internal/adapters/upstream/websocket_sessions.go#websocketLease - per-response frame hand-off
 - existing: internal/adapters/upstream/websocket_sessions.go#read - drop unleased frames, mark unsolicited
 - existing: internal/adapters/upstream/responses_websocket.go#streamResponsesWebSocket - arm/disarm, response identity checks

Tests:
internal/adapters/upstream/websocket_reuse_test.go
 - description: trailing frames after a terminal never reach the next response; the fenced socket keeps its continuation
   requirements: R1, R2
   input: stub writes a delta and a timing event after each terminal
   workflow: first response --idle 100 ms--> second response --continuation--> third response
   expected outcome: second and third responses contain no frame of the previous response; one handshake
 - description: foreign, duplicated-created and replayed previous terminals are rejected
   requirements: R3
   input: stub sends a terminal for another response ID
   workflow: seed response --next response with foreign terminal--> invalid_stream
   expected outcome: no terminal emitted, usage unknown, socket retired (second handshake on next request)

### 2. Upstream WebSocket liveness tolerates a slow consumer
armed lease with probe --tick after an interval without received frames--> detached ping --pong, received frame or reader waiting on the consumer--> healthy lease --no pong and nothing received--> transport failure without replay

Requirements:

  R4:
Any frame received from upstream SHALL count as transport liveness; the probe SHALL skip a ping when a frame arrived within the ping interval and SHALL NOT fail while the reader waits for the consumer to accept a frame or when a frame arrived after the ping was sent.

  R5:
A ping SHALL be bounded only by its own timeout and MUST NOT be cancelled by lease completion or request cancellation; stopping the probe SHALL return without waiting for a pong, and in-flight pings SHALL be joined when the adapter closes.

  R6:
The existing contract stays: an active upstream that answers no ping and sends nothing for the ping timeout after response.create fails as a transport error without replay, quota refusal or fabricated usage.

Files And Functions:
 - existing: internal/adapters/upstream/websocket_probe.go#probeWebSocket - frame-aware liveness
 - planned: internal/adapters/upstream/websocket_probe.go#ping - detached, joined ping
 - existing: internal/adapters/upstream/websocket_sessions.go#close - join pings after readers

Tests:
 - description: a consumer slower than ping interval plus timeout keeps a healthy response
   requirements: R4
   input: ping 100/100 ms, consumer blocks 1 s on the first delta
   workflow: response with blocked consumer --probe ticks--> completed response
   expected outcome: resp_slow completed with usage and all five deltas
 - description: completion while a ping is in flight keeps the owner socket
   requirements: R5
   input: `TestWebSocketCompletionStopsPendingProbeAndReusesConnection` under `taskset -c 0,1 go test -race -count=50 -cpu 2`
   expected outcome: no `continuation_not_found`; one handshake (previously 6–8 failures per 30 runs)
 - description: existing "upstream stops answering pings" tests keep failing the attempt
   requirements: R6

### 3. Idle pooled socket is proven alive before response.create
idle pooled socket --lease reused socket--> leased socket --[idle under 20 s and never unsolicited, otherwise ping with 3 s timeout]--> [proven socket, dead socket] --dead socket: discard before create--> [fresh request redials, exact continuation rejected before execution]

Requirements:

  R7:
Before writing response.create on a reused socket idle for at least 20 s, or marked by R1, the runtime SHALL ping it under the lease with a 3 s bound; on failure it SHALL discard the socket and, because response.create was not written, either use another socket/handshake for a request without previous_response_id or reject an exact continuation as `continuation_not_found` with `RejectedBeforeExecution`.

  R8:
A recently used clean socket SHALL be reused without an extra round trip; request cancellation during the fence SHALL release the lease without discarding the socket and report the existing pre-create cancellation.

  R9:
The response.create write SHALL be bounded by the 30 s WebSocket write timeout.

Files And Functions:
 - existing: internal/adapters/upstream/websocket_sessions.go#acquire - fence on reuse, redial loop
 - planned: internal/adapters/upstream/websocket_sessions.go#fenceReuse
 - existing: internal/adapters/upstream/adapter.go#New - `websocketReuseProbeAfter` 20 s, `websocketReuseProbeTimeout` 3 s

Tests:
 - description: black-holed idle socket is replaced by a new handshake before create
   requirements: R7
   expected outcome: second request succeeds on handshake 2 in under 2 s
 - description: black-holed exact continuation anchor is rejected before execution
   requirements: R7
   expected outcome: continuation_not_found, RejectedBeforeExecution, no second handshake
 - description: warm reuse pays no ping
   requirements: R8
   expected outcome: three sequential responses, zero pings at the stub

### 4. Upstream HTTP connections stay healthy and reusable
upstream HTTP stream --h2 health ping and TLS session cache in the shared transport--> live multiplexed connection --terminal or [DONE] seen--> bounded body drain --body end within bound--> keep-alive connection reused

Requirements:

  R10:
The shared upstream `http.Transport` SHALL configure HTTP/2 health checks (`SendPingTimeout` 30 s, `PingTimeout` 10 s, `WriteByteTimeout` 30 s, `MaxReceiveBufferPerStream` 1 MiB) and a TLS client session cache of 512 entries; only the transport literal in `cmd/codex-lb/runtime.go` changes.

  R11:
After a Responses terminal event or a Chat `[DONE]`, an HTTP/1.x stream body SHALL be drained up to 64 KiB within 100 ms before closing, so an upstream that ends the body shortly after the terminal keeps its keep-alive connection; an HTTP/2 body is closed immediately and an endless body is closed at the bound without changing the result.

Files And Functions:
 - existing: cmd/codex-lb/runtime.go - transport literal only
 - planned: internal/adapters/upstream/stream_http.go#FinishStreamBody - bounded drain shared with provider native chat
 - existing: internal/adapters/upstream/responses_stream.go#streamResponses, internal/adapters/upstream/chat_stream.go#streamChat, internal/adapters/provider/native_api.go#nativeChatStream

Tests:
 - description: twenty HTTP/1.1 SSE requests whose body ends 5 ms after the terminal reuse one connection
   requirements: R11
   expected outcome: one new TCP connection (previously 20)
 - description: an upstream that never ends the body after the terminal still completes within the bound
   requirements: R11
 - description: runtime transport resumes TLS sessions and carries the HTTP/2 health configuration
   requirements: R10
   expected outcome: second handshake `DidResume`

### 5. Chat-source tool calls stream as they are generated
chat delta with tool call --accumulate in per-call builder--> [declared function with ID and name, custom or incomplete call] --declared function: emit output_item.added at first arguments delta--> streamed arguments deltas --[DONE] and validation--> output_item.done and response.completed

Requirements:

  R12:
Tool-call arguments SHALL accumulate in linear time.

  R13:
For a declared non-custom function tool whose call ID and name are known when its first arguments delta arrives, the runtime SHALL emit `response.output_item.added` and stream `response.function_call_arguments.delta` before `[DONE]`; custom tools and calls without ID/name keep terminal-time emission. A name or ID change after streaming started, an undeclared name or a duplicate ID SHALL fail the response as `invalid_tool_call`, and the final `output_item.done` SHALL carry the same item ID and the completed arguments.

Files And Functions:
 - existing: internal/adapters/upstream/chat_stream.go#push, #emitTerminal, #complete

Tests:
 - description: 20 000 small argument deltas accumulate in linear time
   requirements: R12
 - description: streamed function call deltas arrive before [DONE] and the done item matches
   requirements: R13
 - description: a call whose name changes after streaming fails as invalid_tool_call
   requirements: R13

### 6. Each streamed event costs one decode and one copy
upstream event bytes --decode only type and error presence--> owned event --one buffered write and flush--> client

Requirements:

  R14:
Upstream SSE and WebSocket events SHALL be validated as exact JSON values and decoded only for the type (and error presence/terminal data where needed), with one owned copy per SSE event and no copy for WebSocket frames.

  R15:
`writeSSE` SHALL compact data only when it contains CR or LF and SHALL emit the event with one write from a reusable buffer; the SSE line scanner SHALL start with 8 KiB.

Files And Functions:
 - existing: internal/adapters/upstream/sse.go#readSSE, internal/adapters/upstream/responses_stream.go, internal/adapters/upstream/responses_websocket.go
 - existing: internal/adapters/httpapi/proxy_sse.go#writeSSE

Tests:
 - description: benchmarks record ns/op and allocs/op before and after (decision log)
   requirements: R14, R15
 - description: existing invalid-stream, missing-type and trailing-data tests keep their classification
   requirements: R14

### 7. Request bodies are parsed fewer times
client Responses body --one top-level parse per transformation--> top-level object --raw re-assembly without re-compacting or HTML-escaping values--> upstream body

Requirements:

  R16:
Transformations of a Responses body that change only top-level fields SHALL re-assemble the object from already validated raw values (sorted keys, values verbatim) instead of `json.Marshal` on `map[string]json.RawMessage`; the WebSocket `response.create` SHALL be built from the same parse as the HTTP passthrough body.

  R17:
Image-input detection SHALL run only when the provider lacks image input; string-input normalization SHALL decode `input` only when it is a JSON string.

  R18:
The WebSocket reader SHALL decode the prepared capability payload into a fresh object, so fields removed by preparation cannot reappear, and SHALL skip that decode when preparation did not change the payload.

  R19:
`GOEXPERIMENT=jsonv2` SHALL NOT be enabled by default in this slice.

Files And Functions:
 - planned: internal/adapters/wirejson/object.go#EncodeObject - shared raw object encoder
 - existing: internal/adapters/upstream/adapter.go#passthroughBody, internal/adapters/upstream/responses_websocket.go#websocketResponseCreate, internal/adapters/upstream/request.go#validateRequestCapabilities
 - existing: internal/adapters/provider/chatgpt_request.go#chatGPTWireBody
 - existing: internal/adapters/httpapi/proxy_websocket.go#websocket

Tests:
 - description: a field removed from the prepared payload does not reach upstream
   requirements: R18
 - description: raw re-assembly is semantically equal to the previous encoding and keeps `<`, `>` and `&` unescaped
   requirements: R16
 - description: upstream request pipeline benchmark before and after (decision log)
   requirements: R16, R17

### 8. Stream bounds and heartbeats follow real silence
active response --real write--> heartbeat deadline reset --10 s without a real write--> heartbeat --native SSE: data event, other SSE: comment, native WS: application event--> client idle timer reset

Requirements:

  R20:
Upstream streams SHALL be bounded by a total of 256 MiB of event payload per response instead of 100 000 events; the per-event 16 MiB bound and the overall response deadline stay.

  R21:
SSE and WebSocket heartbeats SHALL be sent only after 10 s without any real downstream write.

  R22:
The native `/backend-api/codex/responses` SSE route (and trailing-slash form) SHALL send heartbeats as data events: `response.in_progress` with the real current response ID when known, otherwise `codex.keepalive`; generic `/v1/responses` SSE keeps SSE comments; heartbeats are still withheld before dispatch.

  R23:
During an active downstream WebSocket turn the runtime SHALL send a protocol ping every 30 s on every Responses WebSocket route; the ping is not an application event, is never cancelled mid-write by turn completion and is joined when the connection handler exits.

Files And Functions:
 - existing: internal/adapters/upstream/sse.go#readSSE, internal/adapters/upstream/responses_websocket.go, internal/adapters/provider/native_api.go#nativeChatStream
 - existing: internal/adapters/httpapi/proxy_sse.go#stream, internal/adapters/httpapi/proxy_websocket_stream.go#respondWebSocket, internal/adapters/httpapi/proxy_websocket.go#websocket

Tests:
 - description: more than 100 000 small events complete
   requirements: R20
 - description: a continuous event stream receives no heartbeat; a silent one does
   requirements: R21
 - description: native SSE heartbeat is a data event with the real response ID; v1 SSE heartbeat stays a comment
   requirements: R22
 - description: a v1 WebSocket client observes protocol pings and no synthetic events during a silent turn
   requirements: R23

### 9. Realtime and operations transport hygiene
operation request --resolve client identity through trusted proxies--> attributed operation --bound realtime writes and ping both realtime peers--> [live realtime session, ended realtime session]

Requirements:

  R24:
Realtime writes in both directions SHALL be bounded by 30 s, and both the upstream and the downstream realtime sockets SHALL be pinged every 30 s with a 10 s pong bound; a failed ping or write ends the session as a disconnect.

  R25:
Compact, files, transcription, control and realtime operations SHALL derive `ClientIP` with the trusted-proxy identity resolution used by Responses and `UserAgentGroup` from the User-Agent product token.

Files And Functions:
 - existing: internal/adapters/provider/operations_realtime.go#Realtime
 - existing: internal/adapters/httpapi/codex_operations.go#realtimeLive, #operationOptions
 - existing: cmd/codex-lb/runtime.go - pass trusted prefixes to the operations routes (one call)

Tests:
 - description: a realtime client that stops reading is disconnected within the write/ping bound
   requirements: R24
 - description: compact behind a trusted proxy records the forwarded client IP
   requirements: R25

### 10. Native chat completions render incomplete Responses terminals
Responses event stream --translate to chat chunks--> [completed: stop/tool_calls, incomplete: length or content_filter with usage and DONE, failed: error chunk]

Requirements:

  R26:
`/v1/chat/completions` streaming over a Responses source SHALL map `response.incomplete` to a final chunk with `finish_reason` `length` (or `content_filter` for that reason), optional usage and `[DONE]`, and every chunk SHALL carry `created` and `model`; accounting of the attempt is unchanged.

Files And Functions:
 - existing: internal/adapters/httpapi/native_api.go#chatStreamTranslator

Tests:
 - description: route test with a stub that ends in response.incomplete
   requirements: R26

### 11. Dead transport code is removed
transport code with unused parameters and per-request mux --remove--> equivalent code

Requirements:

  R27:
Unused `catalog`/`r` parameters, the per-request `ServeMux` in `CodexOperationsHandler.ServeHTTP` and the redundant ws/wss URL rewriting SHALL be removed without behavior change.

Files And Functions:
 - existing: internal/adapters/upstream/{responses_stream.go,responses_websocket.go,adapter.go}
 - existing: internal/adapters/httpapi/codex_operations.go
 - existing: internal/adapters/provider/operations_realtime.go#websocketEndpointURL

## Implementation checklist

1. [x] Regression tests for NET-1/2/3 from the auditor probes (R1–R8).
2. [x] Per-response lease hand-off, unsolicited marking, response identity checks (R1–R3).
3. [x] Frame-aware probe, detached joined pings, create write timeout (R4–R6, R9).
4. [x] Reuse fence with redial or pre-create continuation rejection (R7, R8).
5. [ ] HTTP/2 health config and TLS session cache in the transport literal; bounded body drain (R10, R11).
6. [ ] Linear tool-call accumulation and streamed function calls (R12, R13).
7. [ ] Lighter event decode, single-copy SSE, buffered `writeSSE`, 8 KiB scanner; record benchmarks (R14, R15).
8. [ ] Raw object re-assembly, single-parse WS create, gated image detection, fresh-map WS reader; record benchmarks; NET-6 decision (R16–R19).
9. [ ] Byte-bounded streams, silence-based heartbeats, native SSE data heartbeats, downstream WS pings (R20–R23).
10. [ ] Realtime write bounds and pings; operations identity (R24, R25).
11. [ ] Native chat incomplete mapping (R26).
12. [ ] Dead code removal (R27).
13. [ ] OpenSpec change `harden-upstream-transport-reliability` validated with `--strict`; `go vet`, `golangci-lint`, `go test`, targeted `-race`.

## Open questions

None blocking. Non-blocking: whether ChatGPT emits frames after a WebSocket terminal at all is unknown; R1/R2 make the answer irrelevant for correctness and only cost one ping per reuse for a peer that does.

## Decision log

- 2026-10-28: Unsolicited frames are dropped and the socket stays usable behind a ping fence instead of being discarded, so exact WebSocket continuations survive; frames already in flight when response.create is written remain indistinguishable by timing, so R3 rejects any foreign response identity before it can settle usage.
- 2026-10-28: The lease hand-off is unbuffered so ownership of a frame is decided at hand-off time; the liveness rule R4 makes the lost read-ahead harmless.
- 2026-10-28: Pings run detached from lease contexts (coordinator CI finding): cancelling a coder/websocket control write closes the connection. Probe goroutines are still joined by `stopProbe`; ping goroutines are bounded by their timeout and joined by `HTTPAdapter.Close`.
- 2026-10-28: Reuse fence threshold 20 s / 3 s: Codex tool loops reuse sockets within seconds (no cost), NAT/LB idle limits are ≥ 60 s; a false fence failure costs one handshake or a pre-execution continuation miss that the existing lost-anchor path already handles.
- 2026-10-28: A test stub in `internal/adapters/provider/chatgpt_identity_test.go` reused one response ID for every turn on one socket; it now issues unique IDs, as a provider does, because R3 rejects a repeated previous ID.
