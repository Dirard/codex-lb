## Why

Warm HTTP Responses bridge sessions reacquire their per-account stream lease without passing the cached dashboard concurrency caps. That fallback silently restores the process startup default, so a persisted dashboard stream limit of `0` (unlimited) can still fail with `account_stream_cap`.

When that false cap starts a recoverable capacity wait, the startup probe can then wait forever for the paired ready signal because one branch omits the already-computed discovery deadline. The client receives neither HTTP `200` nor the initial heartbeat while the internal request retries every 30 seconds.

## What Changes

- Reuse one dashboard-settings cache snapshot when a warm bridge session reacquires its stream lease, and pass the derived effective caps into the load balancer.
- Bound the capacity-marker branch of the Responses startup probe by its existing signal-discovery deadline and hand the running stream to the response when that deadline expires.
- Add focused regression coverage for dashboard `0` overriding a positive startup cap and for a capacity wait whose ready signal never arrives.

## Capabilities

### Modified Capabilities

- `proxy-admission-control`: cached dashboard caps govern warm-session stream lease reacquisition.
- `responses-api-compat`: a capacity wait cannot hold the HTTP response inside startup probing indefinitely.

## Impact

- Affected runtime files: `app/modules/proxy/_service/http_bridge/request_submit.py` and `app/modules/proxy/api.py`.
- No API, database, migration, setting, or deployment contract changes.
- Existing request-budget and recovery semantics remain unchanged after the startup stream is handed off.
