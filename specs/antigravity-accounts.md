# Antigravity accounts as model sources in the shared pool

- Last edited with skill pack: `0.2.2`

## Title and scope

The owner can add Google Antigravity accounts, «добавлять аккаунты антигравити»: sign in with a Google account on the dashboard, and the account's Antigravity models (Gemini 4, Gemini 3.8 Flash, Claude 5.5 and others its plan allows) serve Codex Responses and Chat Completions requests from the shared pool. Each account is a model source of the new kind `antigravity` with its own OAuth tokens, starts sticky-only (see `specs/sticky-only-accounts.md`), and is stopped for good when Google suspends it.

## Planning anchor

- Entrypoints:
  - existing `internal/adapters/provider/provider.go#Adapter.respondExternal` (dispatch to a model source);
  - existing `internal/application/model_sources.go#ModelSourceService` and `/api/model-sources`;
  - existing `internal/adapters/upstream` translation of Responses to Chat Completions (`translateToChat`, `chatStreamState`);
  - new dashboard flow «Add Antigravity account» (paste-code OAuth).
- Changed assumption: model sources authenticate with a static key or borrow a Grok token; none speaks Google's Cloud Code `v1internal` protocol. Research: `.local/research/antigravity/protocol.md` (2026-11-22).
- Owner decision (2026-11-22): build it although Antigravity's Terms (Section 6) call third-party access a breach and Google suspended such pools on 2026-11-06 and 2026-11-14; the owner takes the risk («Делать, риск беру на себя»). Boundary agreed in the question: no client fingerprint emulation (TLS, HTTP/2 settings, header order, native request shapes beyond the protocol) and no prompt rewriting to evade content filters.
- Non-local: domain source kind and credential, OAuth client, Cloud Code client, upstream protocol translation, provider dispatch, account status handling, model source service, admin API, dashboard, OpenSpec `go-runtime` and `frontend-architecture`, release notes.
- OpenSpec change `add-antigravity-accounts`, archived as `openspec/changes/archive/2026-11-22-add-antigravity-accounts/`: `go-runtime` ADDED «Antigravity accounts sign in as model sources», «Antigravity sources serve Responses and Chat Completions», «Antigravity refusals are classified»; `frontend-architecture` ADDED «Dashboard signs in and shows Antigravity accounts».
- Impacted specs:
  - `specs/sticky-only-accounts.md`: Antigravity sources start sticky-only. Action: `reuse`.
  - `specs/grok-subscription-text-source.md`: the pattern of a subscription-token source. Action: `reuse`.

## Connected groups or observed existing logic

### Contracts between the parts

- `internal/domain/model_source.go`: kind `ModelSourceAntigravity` (`antigravity`), `AntigravityBaseURL`, `ModelSourceProviderConfig.Antigravity` (`AntigravityAccountInfo{Email, Project, Tier, ActionURL}`, non-secret). The refresh token is the source credential's `ExternalKeyEncrypted`.
- `internal/adapters/upstream`: `ProtocolAntigravity`, `AntigravityCapabilities()`, `Target.AntigravityProject`, `Target.AntigravityUserAgent`; `HTTPAdapter.executeAntigravity` and `streamAntigravity` (placeholders in the foundation commit); `Error.RetryAfter`, `Error.ActionURL`; codes `upstream_content_refused`, `model_capacity_exhausted`, `account_suspended`, `account_validation_required`, `model_access_denied`, plus the existing `insufficient_quota`, `rate_limit_exceeded`.
- `internal/adapters/provider`: `Config.AntigravityTokens` (`AntigravityTokens{AntigravityAccessToken, ReportAntigravityRefusal}`), `Config.AntigravityBaseURL`, `Config.AntigravityClientVersion`.

### Connected groups

1. Entry and orchestration
   - `Adapter.respondExternal` loads the source and model, checks the credential row against generation and route revision, decrypts `ExternalKeyEncrypted`, and calls the upstream adapter under the source's concurrency gate. Grok sources take a fresh OAuth token instead of the stored key.
2. Upstream translation
   - `translateToChat` turns Responses input items into `[]ChatMessage` (system, user, assistant with tool calls and reasoning text, tool results), keeping per-call reasoning in the in-memory `Continuation`.
   - `chatStreamState` turns chat stream chunks (text, reasoning text, tool call deltas, usage) into Responses events, the final response object and the continuation.
3. Persistence
   - A source is a `legacy_model_sources` row plus an `accounts` row of kind `external` and a credential row; `ExternalKeyEncrypted` holds the secret. Saving a different kind or base URL increments `route_revision`.
4. Response and propagation
   - Source models carry per-million prices; an unpriced model is refused, a zero price is allowed.
   - The account outcome of a quota refusal feeds balancing; `stickyOnly` keeps owned conversations on the account.
5. Frontend
   - The model source dialogs pick a kind; Grok sources pick a signed-in Grok account.
6. Validation
   - Loopback stubs for every upstream; no real Google traffic in tests.

## Use cases

### 1. Sign in an Antigravity account

operator --start sign-in--> PKCE authorization URL --operator signs in at Google and pastes the code--> code --exchange it, read the email, load the project and tier, list the models--> new Antigravity source with its models, sticky-only

Requirements:

  R1:
`POST /api/antigravity/sign-in` SHALL return an authorization URL for Google's Antigravity OAuth client with PKCE S256, the hosted redirect `https://antigravity.google/oauth-callback`, the Antigravity scopes, `access_type=offline`, `prompt=consent` and a random `state`, plus a flow id that expires after 10 minutes. `POST /api/antigravity/sign-in/{flow}` with the pasted code SHALL exchange it once, read the email from userinfo, call `loadCodeAssist` (and `onboardUser` when no project comes back), and list the account's models with `fetchAvailableModels`.

  R2:
A successful sign-in SHALL create a model source of kind `antigravity` named after the email (or re-authorize the existing source of that email, keeping its models, prices and options) with every non-internal, non-disabled model at price 0 and `stickyOnly` on. The refresh token SHALL be encrypted in the credential row; the email, project, tier and action link are non-secret and live in `providerConfig.antigravity`. Tokens SHALL never be logged or returned.

Logic Details:
- The client id and installed-app secret are Google's published Antigravity values; the secret is not confidential, but it is kept in code, not configuration.
- A consumer project is `aicode-consumers`; it goes in the envelope's `project` field and never in `x-goog-user-project`.

### 2. Serve a request from an Antigravity source

Responses or Chat request routed to an Antigravity source --take a fresh access token--> authorized target --translate to the Cloud Code envelope--> envelope --stream from v1internal:streamGenerateContent--> Gemini frames --translate frames to Responses events--> answer with usage

Requirements:

  R3:
The provider SHALL refresh the access token before it expires (one refresh at a time per source) and retry once after a 401 when nothing was visible. A refused refresh (`invalid_grant`) SHALL mark the source's account `reauth_required`.

  R4:
Every Cloud Code call SHALL send `User-Agent: antigravity/hub/<version> (aidev_client; os_type=<GOOS>; arch=<GOARCH>)`, where the version is the runtime setting `antigravityClientVersion` (default `2.34.0`, see use case 5) the way codex-lb presents the Codex client version to OpenAI, and only `Content-Type`, `Authorization` and `User-Agent` headers. It SHALL NOT invent a changelist, emulate TLS or HTTP/2 fingerprints, copy native-only request fields (`labels`, native `requestId` shapes) or rewrite prompts to pass content filters.

  R4a:
The translation SHALL map system text to `systemInstruction`, user text and images to `contents` parts, assistant text, tool calls and tool results to `model`/`user` turns with `functionCall`/`functionResponse`, tools to cleaned `functionDeclarations`, `reasoning.effort` to `thinkingConfig`, and `max_output_tokens` to `maxOutputTokens`. It SHALL drop empty parts, keep one stable `sessionId` per conversation, and send `userAgent: "antigravity"`, `requestType: "agent"` and a fresh `requestId`.

  R5:
The stream SHALL produce Responses events for text, reasoning text (thought parts) and function calls, and usage with `input_tokens = promptTokenCount`, `cached = cachedContentTokenCount`, `output_tokens = candidatesTokenCount + thoughtsTokenCount`, `reasoning_tokens = thoughtsTokenCount`. A stream without `finishReason` or with usage in a later frame SHALL still complete; a mid-stream error frame SHALL be classified like an HTTP error.

  R6:
Thought signatures returned on a part SHALL be kept with the turn and replayed on the same part in later requests of the conversation. A Gemini function call whose signature is unknown SHALL carry `skip_thought_signature_validator`; Claude history SHALL never carry invented thinking.

Logic Details:
- Schema cleaning removes `$schema`, `$ref`/`$defs` (inlined), `title`, `default`, `examples`, `x-*` keys and unsupported constraints, turns `["T","null"]` into `T`, adds `items` to arrays, and for Claude models flattens `anyOf`/`oneOf` to the first non-null branch and gives empty object schemas a placeholder property.
- Hosted tools (web search, image generation) are refused as unsupported, like other model sources.

### 3. Classify Antigravity refusals

Google error --read status, reason and details--> classified refusal --apply the account effect--> client error

Requirements:

  R7:
A 429 with reason `QUOTA_EXHAUSTED` or `INSUFFICIENT_G1_CREDITS_BALANCE` SHALL be a quota refusal (account outcome, sticky rules apply). A 429 with reason `RATE_LIMIT_EXCEEDED` and a 503 `MODEL_CAPACITY_EXHAUSTED` SHALL be transient (`Retry-After` from the retry delay) without an account outcome. A bare 429 without details SHALL be refused to the client as `upstream_content_refused` and SHALL NOT mark the account or move the request.

  R8:
A 403 `TOS_VIOLATION` SHALL deactivate the source's account with reason `tos_violation`, keep the appeal URL for the dashboard, and refuse the request; the account SHALL NOT be retried automatically. A 403 `VALIDATION_REQUIRED` SHALL put the account in `reauth_required` with the validation URL shown on the dashboard. A 403 `MODEL_ACCESS_DENIED` SHALL refuse only that request.

### 4. Manage Antigravity accounts on the dashboard

operator --open Model sources and add an Antigravity account--> sign-in dialog --paste the code--> listed source --refresh models, re-authorize, read status--> managed account

Requirements:

  R9:
The Model sources page SHALL offer «Add Antigravity account» with the authorization link and a code field, show each Antigravity source's email, tier and status (including the appeal or validation link), offer re-authorization and «Refresh models», and keep the existing price, enable and sticky controls.

### 5. Change the Antigravity client version

operator --save a new Antigravity client version in Settings--> stored setting --read it on the next Cloud Code call--> User-Agent with the new version, without restart

Requirements:

  R10:
`runtime_settings.antigravity_client_version` SHALL hold the Antigravity client version (default `2.34.0`, format `<major>.<minor>.<patch>` with up to 4, 4 and 6 digits). `GET /api/settings` SHALL return it as `antigravityClientVersion`, and `PUT /api/settings` SHALL accept and validate it the way it handles `codexClientVersion`. Every Cloud Code call (sign-in control calls and inference) SHALL read the current value, so a saved version applies to the next call without restart; an unreadable setting SHALL refuse the call before dispatch. The environment variable `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION` is retired.

  R11:
The Settings page SHALL show an «Antigravity client» section beside «Codex client» with the version field and a Save button that sends only that change.

Tables:
runtime_settings
 - antigravity_client_version: TEXT NOT NULL DEFAULT '2.34.0' # Antigravity client version in the Cloud Code User-Agent

Logic Details:
- Migration 53 adds the column. It is additive: older code ignores it and adding a column with a constant default rewrites no table.

Files And Functions:
 - planned: internal/adapters/sqlite/antigravity_settings_schema.go#schemaV53
 - existing: internal/adapters/sqlite/settings.go#Store.LoadAntigravityClientVersion
 - existing: internal/domain/models.go#RuntimeSettings.AntigravityClientVersion
 - existing: internal/adapters/provider/provider.go#Config.ResolveAntigravityClientVersion
 - existing: internal/adapters/antigravity/client.go#Config.ResolveClientVersion
 - planned: web/src/features/settings/components/antigravity-client-settings.tsx

Tests:
 - description: settings round trip and refusal of an invalid version; migration default; a saved version reaches the next inference and control call User-Agent without restart; the dashboard field saves only the version
   requirements: R10, R11

## Implementation checklist

1. [x] OpenSpec change `add-antigravity-accounts`, validated strictly.
2. [x] Contracts: domain kind `antigravity`, credential payload, upstream `ProtocolAntigravity`, target fields and error codes.
3. [x] Upstream translation and stream (R4–R6) with unit tests.
4. [x] Error classification (R7, R8) with tests for each variant.
5. [x] OAuth client, Cloud Code client and sign-in service (R1, R2).
6. [x] Provider dispatch and token refresh (R3), account effects (R7, R8).
7. [x] Admin API and dashboard (R9).
8. [x] Route-level tests through loopback stubs: sign-in, a streamed Codex turn with a tool call and replayed signature, quota refusal on a sticky account, TOS suspension.
9. [x] Gates, archive OpenSpec, release notes, merge and release; stop before prod and before any real Google traffic.
10. [x] OpenSpec change `move-antigravity-client-version-to-settings`, validated strictly. (R10, R11)
11. [x] Migration 53, settings store and admin API, live resolver for the provider and the Antigravity client; retire the environment variable. (R10)
12. [x] Dashboard «Antigravity client» section with locales and tests. (R11)
13. [ ] Gates, archive, release notes, merge and release; stop before prod.

## Open questions

None blocking. Quota windows (`retrieveUserQuotaSummary`) for balancing and the dashboard are deferred.

## Decision log

- 2026-11-22: the owner accepts the Terms risk. No fingerprint emulation and no filter evasion.
- Antigravity accounts are model sources (like Grok subscription sources), so they join the pool, catalog, pricing and sticky option without a new routing path.
- New sources start sticky-only, because thought signatures and Google's heuristics tie a conversation to its account.
- Implementation decisions (2026-11-22):
  - Egress is handled like Grok subscription sources: the account row needs no separate egress decision, and the access token goes only to `Config.AntigravityBaseURL` under the source's concurrency gate.
  - Account status writes are guarded (`SetAntigravityAccountStatus`): `reauth_required` replaces only active or limit states, a TOS suspension replaces anything but deletion, and a new sign-in returns only a re-authorization or suspension state to active and clears the link. Saving or toggling the source never undoes a suspension.
  - A rotated refresh token is written with compare-and-swap, so an old rotation never overwrites a newer sign-in.
  - Antigravity sources are created only by sign-in: `POST /api/model-sources` refuses the kind, and a source update refuses any `apiKey` field so the refresh token cannot be erased.
  - Native Chat Completions skip Antigravity sources (they would send the stored refresh token as a bearer); such requests go through the Responses translation.
  - Thought signatures are kept per call id and per model turn in memory, also indexed by owner and key so stateless Codex replays without `previous_response_id` get them back; after a restart an unknown Gemini call carries the validator sentinel.
  - The model source JSON gains `accountStatus` and `deactivationReason` for the dashboard.
- 2026-11-22: the owner asked to move the Antigravity client version into Settings like the Codex client version («Да, перенеси»). The environment variable is retired rather than kept as a second source, since no installation ran go-v1.17.0 yet.
