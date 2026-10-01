## Why

The live Luna probe initially failed with `Unsupported parameter: max_output_tokens` because the Go port omitted the legacy subscription wire filter. Real verification then exposed a second port discrepancy: valid subscription SSE without Content-Type was rejected before the first body read, unlike the original implementation.

## What Changes

- Omit `max_output_tokens` at the shared ChatGPT Responses wire boundary for HTTP/WS and synthetic probes; preserve external-provider requests and the caller's original payload.
- Do not let an unsupported tiny output cap reduce the key reservation below the normal uncapped output estimate; settle actual usage as before.
- Make probe and transport tests reject the observed unsupported parameter and verify external passthrough.
- Match the original's acceptance of headerless subscription SSE through a narrowly scoped ChatGPT capability while retaining strict event/terminal/usage validation and external MIME checks.
- Correct the stale probe token-floor requirement and update the local binary with a backup. The initial single-probe limit was later expanded by the user; finish with successful real Luna checks and settled usage, not only mocks.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: subscription output-token compatibility and reservation behavior.
- `usage-refresh-policy`: probe requests must not send a rejected subscription parameter or promise a hard 16-token cap.

## Impact

Shared subscription wire normalization, existing proxy output estimates and regression tests. No schema or UI changes, model fallback, dependency, authentication change or release of historical unknown usage. External Responses and Chat Completions keep their existing token-limit semantics.
