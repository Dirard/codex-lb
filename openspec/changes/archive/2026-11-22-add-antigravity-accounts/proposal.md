## Why

The owner wants to add Google Antigravity accounts to the pool («нужно добавить возможность добавлять аккаунты антигравити») and accepted the risk that Google's Antigravity Terms (Section 6) treat third-party access as a breach and suspend such accounts.

## What Changes

- A new model source kind `antigravity`: the operator signs in a Google account on the dashboard (PKCE paste-code flow); codex-lb stores its refresh token encrypted, its Cloud Code project and tier, and lists its models.
- Requests routed to such a source are translated from Responses (and Chat Completions) to Cloud Code `v1internal:streamGenerateContent` and back, with tool calls, reasoning (thought parts), thought-signature replay and usage.
- Google's refusals are classified: quota (account outcome), rate and capacity (transient), bare 429 (content refusal, request only), TOS suspension (account deactivated, appeal link shown), verification required (re-authorization).
- New Antigravity sources start sticky-only.
- No client fingerprint emulation and no prompt rewriting to pass content filters.

## Capabilities

### Modified Capabilities

- `go-runtime`: added requirements for Antigravity sources (sign-in, serving, refusals).
- `frontend-architecture`: added requirement for the Antigravity sign-in and account status on the Model sources page.

## Impact

- No schema change: a source and its credential use the existing tables.
- New admin routes `POST /api/antigravity/sign-in`, `POST /api/antigravity/sign-in/{flow}`, `POST /api/model-sources/{id}/antigravity/refresh-models`.
- New environment variable `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION`.
