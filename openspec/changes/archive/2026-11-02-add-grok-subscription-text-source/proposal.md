## Why

The owner asked to add the Grok account already signed in on the Video page to the shared pool of text models, as Z.AI was added. An owner-approved probe on 2026-11-02 showed that the account's OAuth access token serves Codex-shaped requests on `https://api.x.ai/v1/responses`: `grok-4.7` streamed a reasoning item and a function call in under a second. Billing showed `prepaidBalance` 0 before and after, so the subscription paid for it, not API credits. `GET /v1/language-models` lists eight text models with prices.

A plain model source cannot hold this credential. The access token lives six hours and its refresh token rotates on each refresh, so only the Grok pool's guarded refresh may renew it.

## What Changes

- New model source kind `grok_subscription`. It names a Grok account in `providerConfig.grokAccountId`, uses the fixed base URL `https://api.x.ai/v1` and takes no API key.
- Creating such a source without models fills them from the account's `GET /v1/language-models`, with xAI's aliases and list prices.
- Each request routed to the source takes the account's current access token through the pool's refresh, and is sent only to the configured xAI endpoint. A 401 before anything visible is retried once with a newer token. If the account is missing, needs a new sign-in or was denied, the request fails before dispatch with `grok_account_unavailable`.
- All other model source behavior applies unchanged: catalog, key scopes, pricing and settlement, concurrency, route fencing. Changing the linked account retires the route.
- The dashboard's New source dialog offers `Grok subscription` with a choice of signed-in Grok accounts, and the list shows the account.

## Capabilities

### Modified Capabilities

- `go-runtime`: adds the Grok subscription source; the Grok video pool may lend its token to it.
- `frontend-architecture`: the model source dialog creates a Grok subscription source.

## Impact

- No schema change: the account link lives in the existing `provider_config_json`.
- Provider traffic:
  - one `GET /v1/language-models` when such a source is created;
  - text requests to `https://api.x.ai/v1/responses` with the subscription token, drawing on the same weekly pool as video.
- API: `kind` accepts `grok_subscription` and `providerConfig` accepts `grokAccountId` on `/api/model-sources`.
