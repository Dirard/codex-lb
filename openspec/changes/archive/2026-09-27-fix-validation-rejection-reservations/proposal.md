## Why

Real Luna E2E reproduced a pre-stream HTTP 400 `invalid_request_error` / `unknown_parameter` leaving an ordinary Responses reservation pending. The adapter knows this is a request validation rejection, but only passes the broad `Dispatched` flag into accounting. No generation output or billing was reported.

## What Changes

- Carry an explicit definitive validation-rejection classification only from actual upstream HTTP rejection boundaries.
- Release the unused reserve as a failed request without retry, while keeping partial billing, output, ambiguous errors and in-stream failures uncertain.
- Verify authenticated routes and repeat the real malformed-input test without modifying the working service.

## Capabilities

### Modified Capabilities
- `go-runtime`: definitive request rejection accounting.

## Impact

Upstream HTTP error classification, provider failure propagation, shared usage uncertainty check and route regressions. No schema or retry policy change.
