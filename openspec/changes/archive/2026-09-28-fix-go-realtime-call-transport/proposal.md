## Why

Contract inspection during real E2E found that Go rejects the original supported SDP/multipart Realtime call request, drops call query parameters, and leaves both WebSocket peers at the library's 32 KiB default despite its declared 4 MiB frame limit. The Realtime error path can also archive private SDP or expose provider error details that original codex-lb deliberately suppresses.

## What Changes

- Preserve bounded JSON, SDP and multipart call bodies, complete Content-Type and query parameters.
- Bind successful call ownership before returning the SDP response; sanitize Realtime errors and keep their payloads out of diagnostic archives.
- Apply the declared message size on both peers and distinguish normal closure from upstream errors.
- Verify these contracts through actual handlers, provider adapters, SQLite and WebSocket peers, retaining key/account ownership and cancellation cleanup.

## Capabilities

### Modified Capabilities
- `go-runtime`: retained Codex Realtime transport, privacy and bounded frame behavior.

## Impact

Realtime handler, application operation, provider WebSocket relay and focused tests. No new provider dependency, model, schema, deployment or reset operation.
