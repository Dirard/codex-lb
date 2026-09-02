## Context

See [proposal.md](proposal.md). Cross-account replay is currently driven by broad pre-visible retry classifications. Direct WebSocket and HTTP bridge accept overload and generic replay codes, while HTTP/SSE can move a verified owner replay after refresh, connect, model, or transient failures. Those paths already share replay-safety checks but lack one authorization gate proving that OpenAI rejected the owner for quota.

## Goals / Non-Goals

**Goals:**

- Use one explicit upstream-quota classification for account-transfer authorization.
- Keep existing account-neutral replay-safety and settlement rules unchanged.
- Preserve the original upstream error whenever no transfer occurs.

**Non-Goals:**

- Change ordinary unpinned failover, account health classification, local quota polling, or account-cap behavior.
- Add settings, database state, retries, or quota probes.

## Decisions

### Use a narrow explicit quota-code predicate

The transfer gate recognizes normalized upstream quota codes: `usage_limit_reached`, `insufficient_quota`, `usage_not_included`, and `quota_exceeded`. Generic `rate_limit_exceeded`, overload, model-capacity, transport, authentication, and model-rejection codes remain outside the gate.

This predicate is intentionally narrower than the existing transparent-replay set. Reusing that set would preserve the bug because it also contains overload and generic rate-limit codes. Reusing only the broad failure class would incorrectly exclude `usage_limit_reached`, which the existing health classifier treats as rate limiting even though it is explicit usage exhaustion.

### Separate authorization from replay safety

An upstream quota rejection only authorizes consideration of transfer. Existing checks must still prove a self-contained account-neutral replay and reject file-, tool-, conversation-, or account-bound state. The authorization does not persist beyond the failed turn.

Direct WebSocket and HTTP bridge will derive the quota code from the received upstream `error` or `response.failed` event. HTTP/SSE verified-owner replay will pass the parsed upstream error code into its owner-move helper; pre-dispatch refresh/connect failures have no quota evidence and therefore cannot move the owner.

For HTTP bridge, an authorized replay must also replace the exhausted bridge generation before account selection. Reusing the existing hard-key generation would feed its original account back as the preferred owner and dispatch the replay to the exhausted account again. The replacement keeps the canonical thread key but clears account-scoped response and turn-state continuity before selecting another eligible account.

Once the replacement socket and durable owner have moved to account B, a later admission or send failure must retire that replacement generation. It must not restore only the in-memory account object to A while leaving B's socket, lease, headers, and durable ownership attached.

### Preserve the upstream terminal when transfer does not happen

The proxy must not synthesize `previous_response_owner_unavailable` after an upstream failure merely because replay is unsafe. Leaving the normalized upstream terminal intact preserves the actual quota or non-quota cause in both the client response and request-log finalization.

## Risks / Trade-offs

- **OpenAI introduces a new quota code** → the proxy fails closed on the owner and exposes the original error until that code is explicitly classified.
- **A previously replayed overload/model failure stops moving active threads** → this is the required behavioral correction; ordinary unpinned failover remains unchanged.
- **Transport implementations diverge** → focused tests cover direct WebSocket, HTTP bridge, and HTTP/SSE with quota and non-quota controls.

## Migration Plan

No data migration is required. Deploy the new image over the existing container and volume. Rollback uses the previous image because no persisted schema or setting changes.
