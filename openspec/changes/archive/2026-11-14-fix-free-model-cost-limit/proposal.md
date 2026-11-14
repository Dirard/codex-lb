## Why

A client reported that Codex auto-review (model `codex-auto-review`) always failed through codex-lb with HTTP 500 `proxy_error` and never showed up in usage statistics, while auto-review worked for other users.

Prod prices `codex-auto-review` at zero through a price override. The reservation for such a request has tokens but no cost. On a key or group with a cost limit (the client's group caps cost at $800 a week), the ledger refused the zero cost part as a missing budget. That error had no mapping, so the request ended as 500 before an account was used or anything was recorded. Keys without a cost limit were not affected.

## What Changes

- A request with tokens but zero cost holds one microdollar on each cost limit while it runs. Settlement charges its actual cost, so the limit returns to where it was.
- A reservation without tokens and without cost is still refused as a missing budget.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for zero-priced requests on cost limits.

## Impact

- No schema change.
- Requests to zero-priced models now reach an account on cost-limited keys and are recorded like other requests.
