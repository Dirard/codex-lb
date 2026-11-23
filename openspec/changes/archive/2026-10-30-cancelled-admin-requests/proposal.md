## Why

When a browser drops a dashboard request (for example during a brief network stall), the admin API maps the request's `context.Canceled` to 500 `internal_error`. It also logs `admin operation failed` at ERROR. On 2026-10-30 three such lines made a client-side stall look like a server failure during diagnosis.

## What Changes

- An admin API request that fails because its client cancelled it answers 499 `request_cancelled`, the same code the proxy routes use, and writes no error log line.
- Other unexpected admin failures keep 500 `internal_error` and the ERROR log.

## Capabilities

### Modified Capabilities

- `go-runtime`: cancelled dashboard requests are not reported as server errors.

## Impact

- Code: `internal/adapters/httpapi` (admin error mapping).
- API: a cancelled admin request returns 499 instead of 500; the client has usually gone already.
- Schema: none.
