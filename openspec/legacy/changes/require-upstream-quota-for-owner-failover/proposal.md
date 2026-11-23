## Why

Active hard-continuity threads can currently prepare a cross-account replay after non-quota upstream failures, while the client sees a misleading owner-unavailable error instead of the upstream cause. Account transfer must require an actual upstream quota rejection, never a local usage snapshot or another failure class.

## What Changes

- Keep active hard-continuity requests on their owner when local usage reaches 100%, the account is locally marked rate-limited, or a non-quota failure occurs.
- Permit cross-account replay only after an upstream error event or response is explicitly classified as quota exhaustion and the existing replay-safety proof succeeds.
- Preserve the original upstream error when transfer is not authorized or safe, including its normalized code in request logs.
- Apply the same rule to direct WebSocket, HTTP-bridge, and HTTP/SSE Responses transports.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `sticky-session-operations`: Restrict active hard-owner cross-account replay to verified upstream quota failures.

## Impact

- Affected code: Responses WebSocket error classification/replay, HTTP-bridge upstream-event handling, and HTTP/SSE verified-owner replay.
- Affected behavior: active-thread failover and upstream error reporting.
- No database migration, dependency, API setting, or dashboard change.
