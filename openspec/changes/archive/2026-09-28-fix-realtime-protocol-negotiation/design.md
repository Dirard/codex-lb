# Native Realtime protocol negotiation

- Last edited with skill pack: `0.2.2`

## Title and scope

Carry the actual Codex negotiation contract through Go and restore the original legacy sideband route.

## Planning anchor

The isolated real browser reached LB call creation and received HTTP 400. Official `core/src/realtime_conversation.rs::realtime_request_headers` declares quicksilver v1/v2 through OpenAI-Alpha; the current adapter reconstructs headers without it. Original `proxy_websocket.py::_build_realtime_live_headers` retains relevant Beta tokens while removing Responses-only values. Original `api.py::v1_realtime_websocket` selects query-based v1/v2 rather than the v3 live path. Amend the existing Realtime contract; do not assume the 400 has no additional cause until the real rerun passes.

## Connected groups or observed existing logic

- The call handler already preserves body, media type and query. Add a narrow two-field protocol-header record reused by call and sideband request DTOs; no arbitrary header map or Authorization/Cookie passthrough.
- Application call validation and a shared sideband request validator enforce header lengths, controls, protocol and query bounds before upgrade/dispatch. Existing owner lookup and active user-key checks remain unchanged.
- The provider applies only Alpha/Beta to its account-owned HTTP/WS headers and removes Responses-only Beta tokens. Call creation uses the same existing authenticated operation path. V3 attachment keeps `/live/<id>`; explicit legacy attachment uses `/realtime` and appends exactly one call-ID selector.
- The shared HTTP sideband handler determines protocol from the registered route, rejects missing/duplicate/mixed selectors and validates before accepting the socket. Do not use provider errors to guess a transport or retry.
- Tests use actual route/provider/SQLite/WS components to cover both endpoint forms, negotiation, owner isolation and malformed headers/selectors. The isolated WebRTC peer uses the official v3 model/session shape, generated speech, and no microphone; its access source cannot refresh OAuth credentials.

Focused original/client comparison is sufficient; separate reverse documentation or a broad code map is unnecessary. Existing live/test results remain valid for their scopes. A real native v3 media call and sideband now pass; legacy protocol routing is covered by local contract tests, not a separate live legacy call.

## Use cases

### 1. Negotiate the requested Realtime protocol

native client headers --validate bounded Alpha/Beta--> selected account credentials --filter Responses-only tokens--> matching private call or sideband request

Implementation Logic:
Carry explicit strings, reject controls/oversize, and set only their named headers at the provider boundary. Preserve identity headers generated for the selected account. Do not introduce protocol guessing or new operator settings.

Tests:
- description: Send native quicksilver and mixed Beta tokens on call creation and both sideband protocol families.
  expected outcome: Alpha and valid Realtime Beta survive; Responses-only Beta and arbitrary inbound secrets do not.

### 2. Attach to the owned call through either variant

required user key --parse one call selector--> validated protocol/query/headers --resolve key-scoped owner--> correct upstream WS endpoint --relay bounded frames and settle--> closed sideband

Implementation Logic:
Use the explicit route to select live or legacy protocol, preserving the prior zero-value live default for internal callers. Reject a query call ID on live paths rather than silently stripping it. A single legacy call ID is removed from forwarded query fields and re-added once by the adapter; other fields remain intact.

Tests:
- description: Attach on canonical/slash legacy routes and current live aliases with an owned call.
  expected outcome: Correct path/query and byte round trip; required key and account owner remain unchanged.
- description: Supply missing/duplicate/mixed IDs, unknown protocol or invalid negotiation values.
  expected outcome: Fail before upgrade/dispatch with no quota/ownership change.
- description: Repeat the real v3 WebRTC call and synthetic audio/sideband check.
  expected outcome: Record the actual result without exposing SDP, ICE credentials or raw provider failures; do not claim success from local stubs.

## Implementation checklist

1. [x] Confirm native headers and original legacy route, and map the connected path.
2. [x] Carry/validate/filter negotiation and select explicit upstream sideband protocol.
3. [x] Add route/provider regression and race tests; run full tests.
4. [x] Repeat isolated live Realtime check; synchronize verified specs and remaining limits.

## Open questions

No blocking question remains for this change. Upstream entitlement is still account-dependent. No real service replacement or OAuth refresh copy is authorized.

## Decision log

Active implementation flow applies. Plan checks retain privacy, required key identity and no-retry rules rather than adding a generic forwarding/recovery layer.

The corrected native v3 call passed on 2026-09-28 using an isolated Pion WebRTC peer: 202 synthetic audio packets sent, 216 inbound packets, 27 data-channel events and 73 authenticated LB sideband events. The ledger recorded two content-free operations and no uncertain reservation; this does not establish token pricing for media. An initial peer counter used Pion's generic GetStats, which omits sender reports; the corrected test uses its SSRC statistics interceptor. No production dependency was added. Browser automation was unavailable for the completed run, so this result is a real media/HTTP/WS test, not a browser UI certification.

Verification passed: focused Realtime route/provider race tests, full `go test ./...`, and `go vet ./...`. Required selectors, bounded negotiation, strict key ownership and Responses-only header filtering are exercised at the actual HTTP/WS boundary. The working local process was not replaced.
