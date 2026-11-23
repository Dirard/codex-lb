## ADDED Requirements

### Requirement: Voice routes balance subscription accounts

GPT-Live calls, Realtime calls (app and Codex routes) and app Realtime WebSocket sessions whose conversation has no owner SHALL choose among the key's eligible active ChatGPT accounts, minus accounts refused for voice within their cooldown. They SHALL rank accounts without a current voice warning first, then accounts without a Codex quota window at or above 100% with a future reset, then by the fewest voice calls and sessions the worker is serving on the account, then by the lowest highest-window use, then by account id. A conversation with an owner SHALL keep that owner's account and never move. When every candidate is refused, the request SHALL fail with 429 `voice_limit_reached` and `Retry-After` at the earliest cooldown end, before any reservation or dispatch.

A request SHALL try at most three ranked accounts. Only an HTTP 429 refusal at call creation, or at the session dial before anything is relayed, SHALL move it to the next account; any other failure SHALL end it as before. A refused account SHALL be cooled down for voice for the refusal's `Retry-After` seconds (1 to 86400), else 15 minutes. Each attempt SHALL release or settle its own reservation, and the next attempt SHALL reserve again for its own account. The last refusal SHALL reach the client with its status, a fixed code and the upstream `Retry-After` when present.

A Live call SHALL count on its account from creation until its observation ends, and a Realtime WebSocket session SHALL count while it is relayed. A Live usage event with a non-null `usage_limit.status` SHALL warn its account for `usage_limit.reset_seconds` (1 to 86400), else 15 minutes. A warning SHALL only lower the account's rank and SHALL NOT refuse calls, end calls or change account health. Counts, cooldowns and warnings are kept per worker.

#### Scenario: The first account refuses a Live call for its limit
- **WHEN** the subscription answers a Live call on the first ranked account with 429 and `Retry-After: 600`
- **THEN** the call is created on the next account, the refused reservation is released, and for 600 seconds new voice requests skip the first account

#### Scenario: Calls spread over accounts
- **WHEN** a key with two accounts places a second Live call while the first is observed on one account
- **THEN** the second call goes to the other account

#### Scenario: A Live call reports a voice warning
- **WHEN** a call's usage event carries `usage_limit.status: approaching` with `reset_seconds: 3600`
- **THEN** for an hour new voice requests prefer the key's other accounts, and the running call continues

#### Scenario: Every account is cooling down
- **WHEN** every eligible account was refused for voice within its cooldown
- **THEN** a new voice request receives 429 `voice_limit_reached` with `Retry-After` and nothing is reserved or dispatched
