## ADDED Requirements

### Requirement: Antigravity accounts sign in as model sources

The admin API SHALL start an Antigravity sign-in with an authorization URL for Google's Antigravity OAuth client using PKCE S256 and the hosted redirect, and SHALL complete it once with the pasted code by exchanging the code, reading the account email, loading its Cloud Code project and tier and listing its models. A completed sign-in SHALL create, or re-authorize, a model source of kind `antigravity` for that email with its models priced at zero, sticky-only enabled, and the refresh token encrypted at rest. Tokens MUST NOT be logged or returned.

#### Scenario: First sign-in
- **WHEN** an operator completes a sign-in with a valid code
- **THEN** a new `antigravity` source appears with the account's models and the sticky-only option on

### Requirement: Antigravity sources serve Responses and Chat Completions

A request routed to an `antigravity` source SHALL be sent to Cloud Code `v1internal:streamGenerateContent` (or `generateContent`) with a fresh access token, the account's project, `userAgent` `antigravity`, `requestType` `agent`, a stable session id per conversation and a User-Agent header built from the configured Antigravity client version. Text, images, tool calls, tool results and reasoning SHALL be translated both ways; thought signatures SHALL be replayed on the same part in later turns, and an unknown Gemini function-call signature SHALL carry `skip_thought_signature_validator`. Usage SHALL count `candidatesTokenCount + thoughtsTokenCount` as output tokens. The runtime MUST NOT emulate client TLS or HTTP fingerprints or rewrite prompts to pass content filters.

#### Scenario: A Codex turn with a tool call
- **WHEN** Codex sends a turn that ends in a function call and then the tool output
- **THEN** both requests reach Cloud Code with the call's thought signature replayed and the client receives Responses events and usage

### Requirement: Antigravity refusals are classified

A 429 with reason `QUOTA_EXHAUSTED` SHALL be a quota refusal. A 429 `RATE_LIMIT_EXCEEDED` and a 503 `MODEL_CAPACITY_EXHAUSTED` SHALL be transient refusals that carry the provider's retry delay and SHALL NOT mark the account. A bare 429 without details SHALL reach the client as `upstream_content_refused` without marking the account or moving the request. A 403 `TOS_VIOLATION` SHALL deactivate the source's account and keep the appeal link; it MUST NOT be retried automatically. A 403 `VALIDATION_REQUIRED` SHALL require re-authorization and keep the verification link.

#### Scenario: Suspension
- **WHEN** Cloud Code answers 403 `TOS_VIOLATION`
- **THEN** the source's account is deactivated, its appeal link is shown on the dashboard and no further request is routed to it
