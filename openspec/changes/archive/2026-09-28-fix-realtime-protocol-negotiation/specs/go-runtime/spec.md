## ADDED Requirements

### Requirement: Realtime protocol negotiation preserves native client variants

Realtime call creation and sideband requests SHALL forward bounded `OpenAI-Alpha` and `OpenAI-Beta` negotiation values without accepting arbitrary client headers or replacing provider-owned authentication/account identity. Responses-only Beta tokens (`responses=experimental` and `responses_websockets` declarations) MUST NOT enter Realtime upstream requests. Invalid or oversized negotiation headers SHALL be rejected before provider dispatch or WebSocket upgrade.

The runtime SHALL support `/v1/realtime?call_id=<id>` and its trailing-slash equivalent for v1/v2 sidebands, forwarding to the upstream query-based `/realtime` endpoint. Exactly one valid call ID is required and SHALL retain the same active Bearer key/account-generation owner checks as v3 `/live/<id>` and backend aliases. V3 path-based requests MUST reject query-supplied call IDs. All variants SHALL retain bounded frames, private failures, cancellation cleanup and sanitized accounting.

#### Scenario: Native Codex selects frameless Realtime
- **WHEN** call creation or v3 attachment carries `OpenAI-Alpha: quicksilver=v2`
- **THEN** the subscription transport preserves that value with the selected account's own authentication

#### Scenario: A v1/v2 client attaches by query
- **WHEN** an authenticated client supplies one owned `call_id` on the legacy route
- **THEN** the upstream request uses `/realtime` with that single call ID and other bounded query values, not the v3 live path

#### Scenario: A caller mixes ownership selectors
- **WHEN** a legacy route has zero/multiple call-ID values or a path-based route also supplies a call-ID query value
- **THEN** the request fails before WebSocket upgrade and upstream dispatch without changing ownership
