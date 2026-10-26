## Why

Codex sends `response.interrupt` when new input interrupts an active response. The LB rejects it with `Only response.create and response.cancel are supported`, turning an ordinary interaction into a stream error.

## What Changes

- Relay validated interruption commands to the exact active ChatGPT upstream WebSocket and forward its acknowledgement and terminal response.
- Preserve actual terminal usage and continuation ownership; reject stale, foreign or unsupported interruption targets without cancelling current work.
- Handle interruption/terminal races without leaking acknowledgements into a reused connection.

## Capabilities

### Modified Capabilities

- `go-runtime`: native Codex WebSocket interruption compatibility and cleanup.

## Impact

Downstream WebSocket handler, request-scoped application/provider control plumbing, upstream Responses loop and deterministic Go tests. No schema, dependencies, settings, client edits or service updates. Public `response.steer` and automatic successor responses are not part of this fix: the inspected Codex client uses `response.interrupt` followed by an explicit `response.create`.
