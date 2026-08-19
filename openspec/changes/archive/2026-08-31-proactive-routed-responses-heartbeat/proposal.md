## Why

An idle routed upstream Responses WebSocket can close before codex-lb's current 120-second heartbeat fires. Production evidence showed an otherwise healthy routed connection closing without a close frame after about 63 seconds.

## What Changes

- Send routed upstream Responses WebSocket heartbeats before the observed upstream idle boundary.
- Preserve the existing downstream client idle budget and direct transport behavior.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `responses-api-compat`: Use a proactive routed upstream heartbeat cadence without shortening the downstream idle budget.

## Impact

- Affects routed upstream Responses WebSocket connection setup only.
- Uses the existing idle-timeout setting; no new dependency or operator setting.
- Adds focused coverage for routed heartbeat timing.
