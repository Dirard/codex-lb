## Why

Real Codex CLI E2E succeeds with Z.AI over HTTP but fails after a tool call over WebSocket: the Go runtime forwards stateless HTTP `previous_response_id` and empty `generate:false` prewarm requests to an HTTP-only external source. This also retains three uncertain test-key reservations. The original rejects this transport before dispatch so Codex can fall back to HTTP.

## What Changes

- Reject downstream WebSocket requests before reservation or provider dispatch when their selected external account requires HTTP; return the original `503 model_source_requires_http_transport` contract.
- Preserve native subscription WebSocket and explicitly selected external upstream WebSocket behavior, authorization and account ownership.
- Verify the fallback through the real Codex CLI against an isolated rebuilt runtime; keep the running local installation unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: preserve a model source's supported transport without converting WebSocket continuation into stateless HTTP requests.

## Impact

Application transport admission and public Responses WebSocket regressions. No schema, dependency, user setting or changes to existing uncertain usage records. The broader product goal remains active: all retained functionality except limit resets and quota-triggered account failover still require verification beyond this repair.
