## Why

The 2026-10-28 transport audit (`specs/audits/2026-10-28/04-transport.md`) confirmed with loopback probes that a pooled upstream WebSocket can deliver a frame from one response to the next (NET-1), that a slow downstream client makes the liveness probe kill a healthy response (NET-2), and that a socket that died while idle is reused and fails only after dispatch with unknown usage (NET-3). CI also showed that stopping the probe while a ping is being written closes a healthy owner socket. HTTP/SSE upstreams lack HTTP/2 health checks and lose HTTP/1.1 keep-alive after every stream; native Codex SSE heartbeats are SSE comments that the Codex CLI parser ignores; large contexts spend most of their latency re-parsing JSON.

## What Changes

- Hand upstream WebSocket frames only to the response that owns the socket, drop unsolicited frames, fence reuse of such sockets with a ping, and reject events whose response identity does not match.
- Count any received frame as transport liveness, never fail the probe while the consumer is slow, and send pings that lease completion cannot cancel mid-write.
- Prove liveness of a socket idle for 20 s before `response.create`; replace it or reject an exact continuation before execution.
- Configure HTTP/2 health pings and a TLS session cache on the upstream transport; drain a short HTTP/1.x body tail after the terminal.
- Stream function tool calls from Chat-compatible sources before completion.
- Bound upstream streams by payload size instead of event count; send heartbeats only after real silence; use data-event heartbeats on native Codex SSE; protocol-ping downstream WebSocket turns.
- Bound realtime writes and ping both realtime peers; resolve operation client IP through trusted proxies.
- Render incomplete Responses terminals as `finish_reason: length` on native Chat Completions streams.
- Reduce per-event and per-request JSON work without changing request semantics.

## Capabilities

### Modified Capabilities

- `go-runtime`: upstream WebSocket pool isolation and liveness, HTTP upstream connection health, stream bounds and heartbeats, Chat tool-call streaming, realtime/operations transport hygiene.

## Impact

`internal/adapters/upstream`, `internal/adapters/provider`, transport handlers in `internal/adapters/httpapi`, the `http.Transport` literal and the operations route wiring in `cmd/codex-lb/runtime.go`. No schema, settings, dependencies or dashboard changes; accounting and ledger semantics are unchanged. Request bodies sent upstream become byte-different (values are no longer re-compacted or HTML-escaped) but remain semantically identical. `GOEXPERIMENT=jsonv2` is evaluated and not enabled.
