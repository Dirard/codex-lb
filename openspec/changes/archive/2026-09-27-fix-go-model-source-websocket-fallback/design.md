# External-source WebSocket fallback

- Last edited with skill pack: `0.2.2`

## Title and scope

Make Codex continue external-source tasks over HTTP when WebSocket semantics cannot be preserved upstream. This is the first repair in the active full-product verification goal, not a replacement for that goal. No limit-reset work or deployment is included.

## Planning anchor

`Proxy.Respond` calls `responseTransport` before reserving budget. For downstream WebSocket and an external account the result can be HTTP, yet the WebSocket-only `generate:false` and `previous_response_id` flow is dispatched unchanged. The real GLM CLI test fails after a tool call. Amend `openspec/specs/go-runtime/spec.md`; preserve all other contracts.

## Connected groups or observed existing logic

- Ingress: `httpapi.ProxyHandler.websocket` validates and authenticates each frame, delegates to the application and emits the existing typed error envelope. No UI changes are required.
- Selection/transport: `Proxy.Respond` applies model policy, scope, owner and account admission before `responseTransport`; this is the shared guard boundary, not a provider-specific Z.AI workaround.
- Accounting: reservations occur after transport resolution. Early rejection prevents new uncertain reservations without inventing zero usage for previous attempts.
- Upstream: the Responses HTTP adapter forwards opaque payloads; native subscription WebSocket owns real connection state. Do not emulate upstream response persistence or turn on `store:true` without user intent.
- Original: upstream commit `09a140fa9979a908e60acc97232367e0a08ef32c`, `app/modules/proxy/_service/websocket/mixin.py`, explicitly emits a 503 source-transport error to activate Codex HTTP fallback.
- Verification: reuse existing `wireFixtureWithProxy`, actual SQLite accounting and a real CLI run against an isolated rebuilt runtime. No new test framework or dependencies.

Focused inspection and live error archives already establish the behavior; separate reverse documentation is unnecessary. This narrow compatibility repair does not require a new whole-product design.

## Use cases

### 1. Select a supported transport before reserving budget

authorized selected account --resolve upstream transport--> resolved transport --check external WebSocket compatibility--> [HTTP-only external account --return retryable transport error--> client HTTP fallback, compatible account --reserve and dispatch normally--> existing response flow]

Implementation Logic:

After `responseTransport`, reject only downstream WebSocket plus an external account plus resolved non-WebSocket upstream. Return the existing `ProxyError` type, status 503 and `model_source_requires_http_transport`. Keep authorization and ownership checks ahead of this guard and reservation/affinity writes behind it. Existing defers release request/account admission on return.

Files And Functions:
- existing: internal/application/proxy.go#Respond - pre-reservation guard
- existing: internal/application/response_transport.go#responseTransport - reuse existing decision unchanged
- existing: internal/adapters/httpapi/proxy_websocket.go#sendWebSocketError - preserve wire envelope
- planned: internal/adapters/httpapi/model_source_websocket_test.go - public route regression

Tests:
- description: Prewarm and generation use the original retryable error on canonical/alias/slash WebSocket routes; an HTTP retry reaches the same external account.
  expected outcome: No rejected-frame provider call, reservation, affinity write or key charge; exactly one settled HTTP request after fallback.
- description: Explicit external WebSocket and native subscription WebSocket remain supported; unauthorized models remain rejected.
  expected outcome: Existing transport, authorization and settlement contracts remain enforced.
- description: Real Codex CLI configured with WebSocket support runs GLM tool and resume scenarios against the rebuilt isolated runtime.
  expected outcome: Client falls back to HTTP and completes the synthetic task without uncertain reservations.

## Implementation checklist

1. [x] Establish the real failure and original contract; prepare delta requirements.
2. [x] Add the early compatibility guard and public route regression.
3. [x] Run focused/race/full tests and real isolated CLI verification.
4. [x] Synchronize the main spec and record remaining full-product work.

## Open questions

None for this repair. Real quota exhaustion across two subscription accounts is not available with the current single ChatGPT account; controlled upstream quota signals and existing ownership tests remain available. Deployment still requires a separate user command.

## Decision log

- 2026-09-27: Preserve the original HTTP fallback contract instead of fabricating server-side persistence or retransmitting a possibly accepted generation. No schema change and no change to existing uncertain reservations.
- 2026-09-27: Spec completeness/consistency review is scoped to this narrow transport repair; the broader product goal is not considered satisfied by these tests. The implementation flow remains authorized from the ongoing rewrite.
- 2026-09-27: Real CLI 0.156.0 with WS enabled completed GLM answer/tool/resume on an isolated rebuilt server by falling back to HTTP. Five provider requests accounted for 45,969 tokens and 13,661 microdollars, matching the key's cost limit, with no uncertain test reservations. The test key was created/revoked through that isolated server's admin API; its process and private credential-bearing data directory were removed. The existing local service retained PID 733312. CLI emits a fallback notice; catalog discovery/configuration remains part of the broader verification goal.
- 2026-09-27: `go test ./...`, full race tests for `internal/adapters/httpapi` and `internal/application`, targeted real-route race regressions, `go vet ./...`, and the Go build pass. Strict delta/main OpenSpec validation and structural design validation pass. No provider billing was fabricated for the three old reservations from the pre-fix test.
