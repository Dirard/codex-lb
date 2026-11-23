## Why

The assembled Go server routes GET `/backend-api/codex/responses` into the Realtime call-ID wildcard instead of Responses. Real Codex WebSocket startup therefore fails with 409 `realtime_call_owner_not_found`. The separate report that All accounts rejects traffic also needs a scoped regression without weakening key/group authorization.

## What Changes

- Register the exact Responses HTTP/WebSocket routes on the runtime's shared proxy router so they take precedence over the retained Realtime alias.
- Keep both `/backend-api/codex` and `/v1` prefixes, including trailing-slash equivalents and existing authentication.
- Verify All-account key creation, explicit selection and clearing through real admin/proxy routes, retaining closed empty scopes/groups.
- Correct operating notes to identify the legacy-compatible Codex base URL explicitly.

## Capabilities

### Modified Capabilities
- `go-runtime`: Responses route precedence in the fully composed server.

## Impact

HTTP registration and command wiring, focused regression tests and compatibility notes. No database migration, key reset, provider retry, deployment or external dependency.
