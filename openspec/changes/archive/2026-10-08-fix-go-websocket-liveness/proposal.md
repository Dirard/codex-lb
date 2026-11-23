# Restore Codex WebSocket liveness

## Why

Go omits the native WebSocket application heartbeats already specified for legacy. Buffered startup events and long upstream reasoning can leave Codex without a text frame until its stream-idle watchdog disconnects. Production has repeated uncertain requests around 300 seconds; a successful Astra request also took over 21 minutes to first text.

## What Changes

- Restore native-only application heartbeats without committing buffered upstream output or enabling unsafe replay.
- Check active upstream WebSocket transport liveness independently of client heartbeats, retaining the existing overall response deadline for long reasoning.
- Verify cancellation, terminal ordering, quota failover, accounting and generic `/v1` compatibility on local WebSocket peers.

## Capabilities

### Modified Capabilities
- `go-runtime`: Codex WebSocket liveness and safe stream lifecycle.

## Impact

HTTP adapter, application response observation, upstream WebSocket transport and adjacent tests. No schema, credentials, UI, routing policy or deployment changes. Release preparation also includes the already verified independent-thread affinity fix. The separate reported HTTP 502 is investigated from server metadata; do not presume the same cause.
