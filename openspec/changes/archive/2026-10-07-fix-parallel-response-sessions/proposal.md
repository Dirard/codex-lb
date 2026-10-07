# Proposal

## Why

Concurrent Codex subagents can share a logical session while issuing independent Responses requests. The Go upstream adapter serializes these requests behind one WebSocket, producing `local_capacity_exceeded` after 15 seconds despite spare stream capacity; waiters can also inherit a retired connection and receive a spurious continuation failure.

## What Changes

- Lease a separate bounded upstream connection for independent concurrent work sharing a logical session, while retaining exact previous-response ownership and sequential connection reuse.
- Recover connection-local continuation loss only before execution and only through the existing safe same-owner replay contract; never duplicate a dispatched request.
- Separate idle downstream WebSocket admission from HTTP request-body admission so subagent sockets cannot consume the unrelated body-reading budget.
- Add deterministic concurrent HTTP/WebSocket regressions covering cancellation, connection retirement, accounting, isolation and real capacity exhaustion.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: Bounded parallel Responses sessions without logical-session head-of-line blocking, preserving continuation and accounting invariants.

## Impact

Go upstream session leasing and HTTP proxy admission, with application-level regression coverage. No database migration, client configuration change, new dependency, paid provider probe, service update or publication is required.
