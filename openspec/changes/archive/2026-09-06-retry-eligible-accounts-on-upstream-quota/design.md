## Context

Direct WebSocket request preparation can prove a client-owned previous-response chain is a full, account-neutral resend and retain its fresh payload. The quota-recovery branch nevertheless requires a proxy-injected anchor, so it forwards an upstream quota rejection even when a safe replacement request already exists. Initial unanchored requests and proxy-injected anchors already have a transparent retry path.

## Decisions

Use the existing verified fresh-replay installer from the actual-quota branch without requiring proxy ownership of the previous-response identifier. Preserve the account-neutral validation, output guard, account exclusions, affinity update, and reservation lifecycle. Generic switching and non-quota connection failures retain their existing restrictions. Check the equivalent upstream handshake-quota path against the same safety conditions before deciding whether it needs an additional change.

Handshake quota recovery uses the same proof. Keyed requests retain their reservation across the retry and use the existing deferred pre-created health helper, so the exhausted account's health update occurs only after confirmed reservation settlement or release. Each rejected account is excluded from both the connection attempt and the request state.

Direct WebSocket event retries use the same deferred-health helper. A successful reconnect resets the per-attempt handled marker so a replacement account retains its own terminal health outcome. Failed HTTP-bridge replacement keeps the replacement socket's owner intact while finalizing the restored original quota error against its original account. Replay remains bounded by the existing retry count and request deadline.

Quota-authorized replacement selection uses fresh affinity and excludes the rejected account; `reallocate_sticky` alone cannot override upstream's durable hard-owner rows. The old stored mapping is not rewritten because it may identify other account-scoped continuations. The active bridge keeps its canonical key while learning replacement-owned response/turn-state metadata. Old turn-state headers are removed before opening the replacement connection on every transport.

HTTP/SSE local owner-selection failures do not consume the full-resend proof to escape ownership. Non-quota retryable events terminate through the existing external error envelope instead of raising an internal retry exception. Terminal HTTP errors share deferred keyed-health handling, preserving one health result after reservation settlement.

## Scope

The change handles explicit upstream quota exhaustion for replayable requests. Local zero-percent usage does not trigger transfer. A request with unresolved account-scoped state, file ownership, or already emitted output is not made replayable by relaxing its guards. No new background probes, data storage, dependencies, or deployment changes are introduced.

## Verification

Reproduce the client-owned full-resend failure, then exercise actual account A to B recovery through the public WebSocket path. Keep negative checks for generic rate limiting and unsafe continuity, and run the existing relevant HTTP/SSE and HTTP-bridge quota regressions.
