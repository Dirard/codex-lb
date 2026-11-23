## Why

Original codex-lb and the existing Go application policy accept both GET and POST for `thread/goal/get`, but the Go HTTP router registers GET only. Original trailing-slash requests also resolve to the canonical control route; Go currently loses those equivalent paths.

## What Changes

- Register the missing POST goal-read route and trailing-slash equivalents for retained control and Realtime call routes.
- Normalize only the optional terminal slash before provider dispatch; preserve method, body, query and authorization.

## Capabilities

### Modified Capabilities
- `go-runtime`: Codex control route compatibility.

## Impact

HTTP registration/normalization and route regression tests; no provider, storage or deployment change.
