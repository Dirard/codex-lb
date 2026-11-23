## ADDED Requirements

### Requirement: HTTP-only model sources trigger client transport fallback before dispatch

For every Responses WebSocket route and its trailing-slash equivalent, a request assigned to an external account with HTTP upstream transport SHALL return a WebSocket error with status 503 and code `model_source_requires_http_transport` before reserving key budget, saving request affinity or dispatching upstream. This SHALL include empty `generate:false` prewarm requests and incremental continuations. The fallback MUST NOT bypass current key, group, model, account or hard-owner authorization. The same authorized model SHALL remain available through HTTP Responses with complete client context. Subscription WebSocket and explicitly configured external upstream WebSocket SHALL retain their native transport behavior.

#### Scenario: Codex starts an external-source WebSocket session
- **WHEN** an authenticated Codex client sends a prewarm or generation frame for an external source using HTTP upstream transport
- **THEN** the proxy returns `503 model_source_requires_http_transport` without provider work or a usage reservation
- **AND** a subsequent authorized HTTP request can complete through that source

#### Scenario: Continue a native WebSocket request
- **WHEN** the selected account is a subscription or an external source explicitly using upstream WebSocket
- **THEN** the new fallback rule does not reject or silently convert that request

#### Scenario: Reject an unauthorized request
- **WHEN** the key cannot use the requested model or account
- **THEN** existing authorization and hard-owner errors remain enforced without dispatch or reservations
