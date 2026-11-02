## Why

After go-v1.10.5 reached prod, no account got a subscription read: `GET /api/accounts` returned `subscription: null` for every account while usage polling worked. An owner-approved diagnostic from prod with skolcov's token showed why: Cloudflare answers `GET /backend-api/subscriptions` from the Go default `User-Agent` with 403 `text/html` and `cf-mitigated: challenge`, and answers the same request with `User-Agent: codex_cli_rs/0.165.4` and `Originator: codex_cli_rs` with 200 and `active_until`. The owner's probe had sent the Codex identity, so it did not show the difference.

## What Changes

- The subscription read sends the Codex client identity that model catalog reads already send: `Originator: codex_cli_rs` and `User-Agent: codex_cli_rs/<saved Codex client version>`. Without a valid saved version it sends nothing.
- Usage and reset-credit reads are unchanged.

## Capabilities

### Modified Capabilities

- `go-runtime`: the background subscription read carries the Codex client identity.

## Impact

- Adapter `chatgpt.UsageClient` and runtime wiring of the saved Codex client version. No API, schema or dashboard change.
