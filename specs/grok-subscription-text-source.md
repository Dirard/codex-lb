# Grok text models from the signed-in SuperGrok account, as a model source in the shared text pool

- Last edited with skill pack: `0.2.2`

## Title and scope

A Grok account already signed in on the Video page can back a model source of the new kind `grok_subscription`. Its text models (`grok-4.7`, `grok-build-0.1` and the others xAI lists) then appear in the Codex catalog and serve Responses requests like any external source, billed to the SuperGrok weekly pool rather than to API credits. The source takes the account's current OAuth access token for each request instead of a stored API key, because that token lives six hours and rotates on refresh.

## Planning anchor

- Entrypoints:
  - existing `internal/adapters/provider/provider.go#Adapter.respondExternal` (dispatch to a model source);
  - existing `internal/application/model_sources.go#ModelSourceService.Create` and `Update` (`POST`/`PATCH /api/model-sources`);
  - existing `internal/application/video_pool.go#VideoService.accessToken` (guarded token refresh of Grok accounts);
  - existing `web/src/features/model-sources/components/model-source-create-dialog.tsx`.
- Changed assumption: model sources authenticate with a static API key, and Grok accounts serve only `/video/v1`. The owner asked to add the signed-in account to the shared pool of text models «так же как и zai».
- Evidence: an owner-approved probe on 2026-11-02 17:38 UTC from prod with the access token of the video account `vacc_zpp…`:
  - `GET https://api.x.ai/v1/language-models` returned 200 with eight text models, their aliases and prices in 1e-4 USD per 1M tokens;
  - one Codex-shaped `POST /v1/responses` with `grok-4.7` (function tool, `tool_choice`, `parallel_tool_calls`, `store:false`, `stream:true`, `include:["reasoning.encrypted_content"]`) streamed a reasoning item and a `function_call` in 0.98 s, with `cost_in_usd_ticks` 4032400;
  - billing read before and after: pool 8.0 %, product `Api`, `prepaidBalance` 0, so the request was not paid from API credits.
- Why a static key does not work: the access token expires after six hours and the refresh token rotates on each refresh, so only the pool's guarded refresh may renew it.
- Non-local: domain model source rules, xAI client, Grok pool service, model source service, SQLite source save, provider adapter, runtime wiring, admin API, dashboard form and list, locales, OpenSpec `go-runtime` and `frontend-architecture`, release notes.
- OpenSpec change `add-grok-subscription-text-source`, archived as `openspec/changes/archive/2026-11-02-add-grok-subscription-text-source/`:
  - `go-runtime`: ADDED «A Grok subscription account can back a model source»; MODIFIED «Grok video generation is a separate pool and REST surface» (the pool's token may serve such a source);
  - `frontend-architecture`: ADDED «Dashboard creates a Grok subscription model source».
- Impacted specs in `specs/`: `specs/grok-video-generation.md` says video accounts are invisible to other routes. That stays true for routing and reports: the source is the routed account, and the Grok account only lends its token. Action: `amend` its scope note with a pointer to this spec.

## Connected groups or observed existing logic

### Connected groups

1. Entry and orchestration
   - `Proxy` selects an eligible account for the requested model. A model source is an `accounts` row of kind `external` whose id equals the source id; `EligibleAccounts` requires `requires_egress_decision=0` and a credential row.
   - `provider.Adapter.Respond` sends kind `external` to `respondExternal`. It loads the source and model, validates streaming and reasoning effort, checks the credential row against the account generation and route revision, decrypts `ExternalKeyEncrypted`, builds `upstream.Target{BaseURL: source.BaseURL, Credential: key}` and calls `a.call` under the source's concurrency gate.
   - `respondChatGPT` already retries once after `ForceRefresh` on a definitive 401 when no output, usage or visible event was observed. That is the pattern to reuse.
2. Loaders and resolvers
   - `VideoService.accessToken(ctx, accountID, usedVersion)` returns a token and its version. It refreshes through one in-process flight per account and a database claim (`ClaimVideoRefresh`), and keeps a rotated refresh token even if the caller leaves. A refused refresh marks the account `reauth_required`.
   - `xai.Client` owns `https://api.x.ai`, `https://auth.x.ai` and the billing host; tests point all three at one loopback stub.
3. Persistence
   - `SaveModelSource` upserts `legacy_model_sources` (`provider_config_json` holds `ModelSourceProviderConfig`), the `accounts` row and the credential row in one transaction.
   - A change of kind, base URL or protocol is a route change: it deletes the source's continuations, resource owners and affinity bindings and increments `route_revision`.
   - `video_accounts` holds Grok tokens (Fernet), `token_version`, status `active|paused|reauth_required|access_denied`.
4. Response and propagation
   - `ModelSourceService.Price` prices external usage from the source model's per-million rates. The catalog renders source models with `shell_type: shell_command`, a default context of 128000, and no search tool.
   - Usage events of a source carry `source=model_source` and `model_source_id`.
5. Frontend consumers
   - The model source dialogs take the kind from a select (`openai_compatible`, `zai`), apply one set of model settings to every model id, and never show a stored key.
   - The Video page lists Grok accounts through `GET /api/video/accounts`.
6. Validation
   - Tests use `wireFixtureWithProxy`, `provider.New` against loopback stubs, and the video fixture's `xaiStub`, in which access tokens name their account and version.
7. Specs
   - `openspec/specs/go-runtime/spec.md` «Integrate external providers without losing Codex semantics» and «Live external-source edits fence retired routes without losing usage» stay authoritative and apply unchanged to the new kind.
   - «Grok video generation is a separate pool and REST surface» is modified, as listed above.
   - `specs/grok-video-generation.md`: `amend`, a scope pointer only.

## Use cases

### 1. Create a Grok subscription source

admin request with kind grok_subscription --validate the account link and fixed endpoint--> valid source draft --fill models from xAI when none are given--> [saved source with xAI's text models, rejected request]

Requirements:

  R1:
A model source of kind `grok_subscription` SHALL name an existing Grok account in `providerConfig.grokAccountId`, SHALL use base URL `https://api.x.ai/v1`, SHALL take no API key and SHALL support only Responses and Chat Completions. A create or update request that gives an API key, another base URL, no account or an unknown account SHALL be rejected with 422 and change nothing. A source of another kind SHALL NOT carry `grokAccountId`.

  R2:
A create request of this kind without models SHALL fill them from the account's `GET https://api.x.ai/v1/language-models`: one enabled model per listed id, with xAI's aliases, input, cached-input and output prices converted from 1e-4 USD to USD per 1M tokens, vision when the input modalities include `image`, streaming and tools on, and a context window of 256000. When that read fails the request SHALL answer 502 `grok_models_unavailable` and create nothing. Explicit models SHALL be stored as given. Responses SHALL default to on and Chat Completions to off.

Logic Details:
- The base URL is forced to the constant on create, and a different base URL on update is rejected. The provider sends the token only to its own configured xAI base, so a stored URL can never redirect the subscription token (R3).
- Prices are list prices. The probe's single request reported a cost about one third of the list estimate; the reason is unknown, and settlement keeps list prices (see Open questions).
- 256000 is the smallest context window among current Grok text models (`grok-code-fast-1`). Codex compacts below it, and the operator may raise it per source.
- No reasoning metadata is set, so Codex sends no `reasoning.effort`. xAI's acceptance of that field is unverified.

Files And Functions:
 - planned: internal/domain/model_source.go#ModelSourceGrokSubscription - the kind
 - planned: internal/domain/model_source.go#GrokTextBaseURL - fixed endpoint
 - existing: internal/domain/model_source.go#ModelSourceProviderConfig - gains `GrokAccountID`
 - existing: internal/domain/model_source.go#ModelSource.Validate - kind rules
 - planned: internal/adapters/xai/client.go#Client.LanguageModels - `GET /v1/language-models`
 - planned: internal/application/grok_text.go#VideoService.GrokTextModels - workflow step "fill models from xAI when none are given"
 - existing: internal/application/model_sources.go#ModelSourceService.Create - workflow step "validate the account link and fixed endpoint"
 - existing: internal/application/model_sources.go#ModelSourceService.Update - rejects a key or another URL for this kind
 - existing: internal/adapters/httpapi/model_sources.go - supplies the Grok pool and maps `ProxyError`

Tests:
internal/domain/model_source_test.go
 - description: the kind needs an account and the fixed URL and rejects audio and embeddings; other kinds reject `grokAccountId`
   requirements: R1
internal/adapters/httpapi/grok_text_source_test.go
 - description: create without models fills xAI's list with converted prices, aliases, vision and context 256000; the source has no key
   requirements: R1, R2
 - description: an API key, another URL or an unknown account answers 422; a failed model read answers 502 `grok_models_unavailable`; nothing is saved
   requirements: R1, R2

### 2. Serve a text request with the account's token

request routed to the source --take the account's current token--> authorized target --dispatch to xAI Responses--> [completed response, one retry after a 401 with a newer token, failure before dispatch]

Requirements:

  R3:
A request routed to a `grok_subscription` source SHALL carry the named Grok account's current access token, obtained through the pool's guarded refresh, and SHALL be sent only to the provider's configured xAI base URL. The token MUST NOT be stored in the source, returned by an API or logged.

  R4:
When the named account is missing, needs a new sign-in or was denied access, the request SHALL fail before dispatch with `grok_account_unavailable`, with no upstream call and no charge. Pausing the account on the Video page SHALL NOT stop its text source.

  R5:
An upstream 401 that arrives before any output, usage or event visible to the client SHALL be retried once on the same account with a token newer than the rejected one. Every other outcome SHALL follow the existing external-source rules, and the request MUST NOT move to another account.

  R6:
Changing a source's `grokAccountId` SHALL retire its routing identity in the same way as a base URL change.

  R7:
Catalog visibility, key scopes, model allowlists, pricing, reservation and settlement, concurrency, timeouts and enable/disable SHALL apply to this kind exactly as to other model sources.

Logic Details:
- Token version: `GrokAccessToken(ctx, id, -1)` returns `(token, version)`. After a 401 the provider asks for `GrokAccessToken(ctx, id, version)`, which `accessToken` treats as stale and refreshes once, or returns a token another worker already renewed.
- The credential row still exists, empty, so the generation and route-revision fence in `credentialForAccount` keeps working.
- The provider id in the upstream target is the kind string, so continuations and capability owners stay per source.

Files And Functions:
 - planned: internal/application/grok_text.go#VideoService.GrokAccessToken - workflow step "take the account's current token"
 - existing: internal/adapters/provider/provider.go#Adapter.respondExternal - Grok branch and 401 retry
 - planned: internal/adapters/provider/provider.go#GrokTokenSource - narrow port for the token
 - existing: internal/adapters/sqlite/model_sources.go#Store.SaveModelSource - account link change is a route change
 - existing: cmd/codex-lb/runtime.go - creates the Grok pool before the provider and passes it in

Tests:
internal/adapters/httpapi/grok_text_source_test.go
 - description: a Codex Responses request for a listed model reaches the stub's `/v1/responses` with the account's bearer token and returns its stream; usage settles at the source prices
   requirements: R3, R7
 - description: a 401 on the first token is retried once with a refreshed token; a second 401 is returned without a third call
   requirements: R5
 - description: an account needing a new sign-in fails with `grok_account_unavailable` and no upstream call; a paused account still serves
   requirements: R4
internal/adapters/sqlite/model_sources_test.go
 - description: changing `grokAccountId` increments the route revision and clears continuations; a name change does not
   requirements: R6

### 3. Create and show the source in the dashboard

operator opens New source --choose Grok subscription and a signed-in account--> create request without URL, key or models --list the source with its account--> visible Grok source

Requirements:

  R8:
The model source dialog SHALL offer the provider `Grok subscription`. For it, creation SHALL ask only for a name, one of the Grok accounts from `GET /api/video/accounts`, a timeout and a concurrency limit, and SHALL send `kind`, `providerConfig.grokAccountId`, the fixed base URL, Responses on and no models. Editing such a source SHALL hide the API key field and keep the base URL read-only. The source list SHALL show the account's label or e-mail. Texts SHALL be localized in `en`, `ko` and `zh-CN`.

Files And Functions:
 - existing: web/src/features/model-sources/schemas.ts - kind enum and `providerConfig`
 - existing: web/src/features/model-sources/components/model-source-form.ts - draft kind
 - existing: web/src/features/model-sources/components/model-source-form-fields.tsx - provider option, hidden key, read-only URL
 - planned: web/src/features/model-sources/components/grok-source-fields.tsx - account choice for creation
 - existing: web/src/features/model-sources/components/model-source-create-dialog.tsx - Grok payload
 - existing: web/src/features/model-sources/components/model-sources-settings.tsx - account label in the list

Tests:
web/src/features/model-sources/components/model-source-create-dialog.test.tsx
 - description: choosing Grok subscription and an account sends the kind, the account id, the fixed URL, Responses on and no models or key
   requirements: R8
web/src/features/model-sources/schemas.test.ts
 - description: a source with `providerConfig.grokAccountId` parses
   requirements: R8

## Implementation checklist

1. [x] Write this spec and the OpenSpec change `add-grok-subscription-text-source`; validate both.
2. [x] Domain kind, fixed URL, `GrokAccountID` and validation with tests (R1).
3. [x] xAI `LanguageModels` and pool methods `GrokTextModels` and `GrokAccessToken` (R2, R3, R4). Docstrings name the workflow steps.
4. [x] Model source service rules for the kind and the admin API error mapping (R1, R2).
5. [x] SQLite route change on account link change, with a test (R6).
6. [x] Provider Grok branch with the 401 retry; runtime wiring (R3, R4, R5, R7).
7. [x] End-to-end tests through the admin API and the proxy against the xAI stub (R1–R5, R7).
8. [x] Dashboard provider option, Grok creation fields, edit and list changes, locales and tests (R8).
9. [x] Gates: `make check`, `make web-check`, `make build`, `make test-web-contract`.
10. [x] Release notes `go-v1.11.0`, context notes, sync OpenSpec into `openspec/specs/`, archive the change, amend `specs/grok-video-generation.md`.
11. [ ] Publish go-v1.11.0 and stop before the rollout; the owner updates prod.
12. [ ] After the update, with the owner's approval, create the source on prod and verify one Codex request end to end.

Deferred:
- Refreshing a source's model list from xAI after creation. Recreating the source picks up new models.
- Settling at xAI's reported `cost_in_usd_ticks` instead of list prices.
- Advertising reasoning efforts for Grok models.
- Failover between several Grok accounts serving the same model. Each source names one account, and existing rules forbid implicit cross-provider switching.

## Open questions

- non-blocking: xAI's reported cost for the probe request was about one third of the list-price estimate (4032400 ticks against roughly 11.9 M at list prices). Settlement uses list prices, which errs toward higher reported spend. Compare with real traffic after release.
- non-blocking: The answer xAI gives when the weekly pool is exhausted is unknown. It is handled by the generic external-source rules.

## Decision log

- 2026-11-02: A new model source kind instead of a new account kind or a separate text pool. It reuses the catalog, key scopes, reservations, settlement, concurrency and route fencing; only the credential differs. The owner asked for it «так же как и zai».
- 2026-11-02: The account link lives in `provider_config_json`, so no schema migration is needed. A changed link retires the route.
- 2026-11-02: The provider sends the subscription token only to its configured xAI base and never to the source's stored URL.
- 2026-11-02: A pause on the Video page does not stop text. The source has its own enable switch, and only `reauth_required`, `access_denied` or a missing account make it unavailable.
- 2026-11-02: Key access follows ordinary model-source rules, since the owner asked for the shared pool. xAI's terms bar making an account available to others; operators can restrict keys with source assignment.
- 2026-11-02: Spec actions: `specs/grok-video-generation.md` is amended with a scope pointer; OpenSpec `go-runtime` and `frontend-architecture` receive the deltas of `add-grok-subscription-text-source`.
- 2026-11-02: Validation: domain, SQLite, xAI-stub end-to-end and dashboard tests; all local gates green (vet, lint, test, race in four shards, govulncheck, web-check, build, web contract, OpenSpec). A mutation that disables the 401 retry fails `TestGrokSourceRenewsARejectedTokenOnce`.
- 2026-11-02: The verification loop ran: completeness and consistency were checked against the owner's request and the probe evidence, without findings that changed the scope.
