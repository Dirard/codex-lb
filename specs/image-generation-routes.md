# Image generation routes: Codex `$imagegen` and `/v1/images/*` through subscription accounts

- Last edited with skill pack: `0.2.2`

## Title and scope

The runtime routes Codex's built-in image generation and editing, and OpenAI-compatible image clients, to the ChatGPT Codex images endpoint of the pool's subscription accounts. It adds four proxy routes, accepts JSON bodies (plus multipart on `/v1/images/edits`), applies key model policy and the shared body budget, picks a ChatGPT account with the shared balancer, bills the key from the response usage, and keeps image outcomes away from account health. If one account's image limit is exhausted, the request goes to another account.

GPT Image 2.5 Sunburst and Flare are not part of this slice. They need an OpenAI API key source; see Deferred.

## Planning anchor

- Entrypoints:
  - planned `internal/adapters/httpapi/codex_images.go` (routes);
  - planned `internal/application/codex_images.go#CodexOperations.ImagesWithOptions`;
  - planned `internal/adapters/provider/images.go#Adapter.Images`.
- Changed assumption: the Go rewrite excluded a public Images API («Deliver selected functionality before cutover»). Codex 0.165 now generates images only through `{base_url}/images/*`, so without these routes the capability is lost for every pool user. OpenSpec change `restore-image-generation-routes` modifies that requirement and adds «Image generation routes use the subscription images endpoint».
- Non-local:
  - HTTP adapter (routes, multipart);
  - application (validation, policy, selection, settlement, failover);
  - shared balancer (`responseRequest` flag);
  - provider adapter;
  - builtin prices;
  - OpenSpec `go-runtime` spec and context;
  - release notes.
- Impacted specs in `specs/`: none. `specs/request-body-budget.md` lists the routes that read bodies; the new routes use the same lease, so its R4 remains true without an edit.
- Evidence: probes of 2026-10-29 and the openai/codex rust-v0.165.0 wire contract, both recorded in `openspec/changes/restore-image-generation-routes/context.md`.

## Connected groups or observed existing logic

### Observed Existing Logic

1. Ancillary Codex routes live in `CodexOperationsHandler`:
   - each authenticates the key, takes a `BodyBudget` lease and reads the body with `readOperationJSON` / `parseTranscription`;
   - each then calls a `CodexOperations` use case and writes the result with `writeResult`, which passes the upstream status, body, content type and a small header whitelist.
2. `CodexOperations.billedAdmitted` holds the operation billing contract:
   - it takes the API-key reservation before the call and calls the provider;
   - it prices usage, settles once, or retains uncertain usage for reconciliation;
   - it records the account outcome. A quota refusal marks the account `quota_exceeded`/`rate_limited`, and a success marks it active;
   - for compaction and warmup it holds a per-account admission lease from the shared Proxy.
3. Account selection:
   - `CodexOperations.selectCompact` wraps `Proxy.selectAccountAdmitted`;
   - `accountCandidates` limits `CompactionTrigger` requests to ChatGPT accounts, filters candidates through the model catalog (`filterCatalogAccounts`, 404 `model_not_available` for unknown models) and prices each candidate.
4. File owners: `CreateFileWithOptions` records the owner account of every file created through `/backend-api/files`, and `singleFileOwner` resolves the owner of the `file_id` values in a body (409 when unknown or conflicting).
5. Provider:
   - `chatGPTBody` sends an operation to a ChatGPT endpoint with the account token, `ChatGPT-Account-ID` and session headers, and retries a definitive uncharged 401 once after a token refresh;
   - `readOperationResponse` caps the body at 32 MiB, parses `usage` (`input_tokens`/`output_tokens` with cached and reasoning details) and maps 429 `usage_limit_reached` to `insufficient_quota`.
6. Pricing:
   - `gpt-image-2`, `gpt-image-1.5`, `gpt-image-1` and `gpt-image-1-mini` exist as builtin prices (5/2/30 USD per 1M input/cached/output tokens), kept for historical reports;
   - aliases match `<model>*`, so `gpt-image-2.5-*` already resolves to `gpt-image-2` rates;
   - `priceUsageEventTx` re-prices subscription events from tokens at settlement.
7. No `/v1/images/*` or `/backend-api/codex/images/*` route exists. The proxy mux returns 404 for them; at the default log level that 404 is not logged.

## Use cases

### 1. Accept and validate an image request

client image request --authenticate, charge the body, validate and apply key model policy--> [validated image request, 400 invalid_request_error, 403 model_not_allowed, 413/503 body budget]

Requirements:

  R1:
The runtime SHALL serve `POST /backend-api/codex/images/generations`, `POST /backend-api/codex/images/edits`, `POST /v1/images/generations` and `POST /v1/images/edits`, each also with one trailing slash. Authentication SHALL use the existing proxy key rules, and the body SHALL be charged to the shared body budget with the 32 MiB route maximum.

  R2:
JSON bodies SHALL be accepted on all four routes. `/v1/images/edits` SHALL also accept multipart with `image` and `image[]` file parts (at most 16 together), at most one `mask`, at most 32 text fields of at most 256 KiB each, and file content types `image/png`, `image/jpeg` or `image/webp` (sniffed when the part header is missing). The form SHALL become JSON: each file becomes a `data:<type>;base64,…` URL in `images[].image_url`, or in `mask.image_url` for the mask; `n`, `output_compression` and `partial_images` become JSON integers and `stream` a boolean; other text fields stay strings.

  R3:
Before reservation, selection or dispatch, validation SHALL reject with 400 `invalid_request_error` and the parameter name: a body that is not an object; a missing, empty or over-32000-character `prompt`; a `model` that is not a string naming a priced `gpt-image-` model; `n` outside the integers 1–10; `stream: true`; and an edit without `images`, with more than 16 entries, or with an entry lacking a non-empty `image_url` or `file_id` string. An absent `model` SHALL become `gpt-image-2` in the forwarded body.

  R4:
An enforced key model SHALL replace the image model, in the forwarded body too, only when the enforced model is itself an image model. A key with allowed models SHALL get 403 `model_not_allowed` for an image model outside them.

Implementation Logic:
1. `prepareImageRequest(body, edit, key)` decodes a top-level `map[string]json.RawMessage` and validates the fields above. It rewrites `model` only when it changes, keeping every other raw value byte-for-byte, and returns the forwarded body and the effective model.
2. Model check: the name has the prefix `gpt-image-`, at most 128 bytes, and `pricing.LookupCodex` succeeds.
3. Multipart conversion lives in httpapi (`parseImageEditForm`). It streams parts through the charged reader with per-part limits. File bytes count against the route maximum; the base64 copy is ordinary process memory, like other prepared payloads.

Files And Functions:
 - planned: internal/adapters/httpapi/codex_images.go#registerImageRoutes, #images, #parseImageEditForm, #writeImageResult
 - existing: internal/adapters/httpapi/codex_operations.go#RegisterRoutes - register the image routes
 - planned: internal/application/codex_images.go#prepareImageRequest, #isImageModel
 - existing: internal/domain/pricing/pricing.go#codexPrices - add `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`

Tests:
internal/adapters/httpapi/codex_images_test.go
 - description: missing prompt, non-image model, unpriced image model, stream true, n=0, edit without images and a 17-image edit each get 400 with the parameter, and the upstream stub sees no call
   requirements: R3
 - description: allowed models without gpt-image-2 give 403 before dispatch; an enforced text model keeps the requested image model; an enforced image model replaces it in the forwarded body
   requirements: R4
 - description: multipart edit with image, image[], mask, prompt and n=1 arrives upstream as JSON data URLs with the same content types and n as a number; an unknown file part and a non-image file give 400
   requirements: R2
 - description: trailing-slash variants route to the same handlers; a body over 32 MiB gets 413
   requirements: R1

### 2. Forward to a subscription account and settle

validated image request --select a ChatGPT account, reserve, forward, settle--> [upstream image response with an account-neutral settled event, 429 image-limit refusal handled by use case 3, 502 invalid or unknown usage]

Requirements:

  R5:
Without file references, the account SHALL be chosen by the shared balancer among the key's eligible ChatGPT accounts, without model-catalog filtering, and under the per-account admission lease used for compaction.

  R6:
An edit that references any `file_id` (in `images` or `mask`) SHALL be sent only to the recorded owner account of those files. An unknown owner SHALL give 409 `file_owner_not_found`, and owners on different accounts 409 `file_owners_conflict`.

  R7:
The body SHALL be posted unchanged with `Content-Type: application/json` to `<ChatGPT Codex base>/images/generations` or `/images/edits`, with the account's access token and `ChatGPT-Account-ID`, under a 5-minute timeout. The upstream status and JSON body SHALL be returned without rewriting.

  R8:
The API-key reservation SHALL be taken before dispatch, priced at the image model's token rates with an 8192-output-token allowance, and settled exactly once from the response `usage`. A 2xx body without a `data` entry carrying `b64_json` SHALL give 502 `invalid_upstream_response`; a 2xx body without usage SHALL give 502 and keep the reservation for reconciliation; a definitive upstream refusal SHALL be settled uncharged.

  R9:
Image requests SHALL NOT record account outcomes. Neither a success nor a refusal changes account status, health or quota state.

  R10:
Usage events SHALL use request kind `image_generation` or `image_edit` and the effective public model. Error diagnostics SHALL store the request without `images` and `mask`.

Implementation Logic:
1. `responseRequest.ImageGeneration` limits `accountCandidates` to ChatGPT accounts and skips `catalogAccountsWithOmissions`. Pricing and additional-quota handling stay as they are; image models have no additional quota.
2. `ImagesWithOptions` follows these steps:
   - validate;
   - resolve file owners (`singleFileOwner` with no continuation owner);
   - acquire the shared operation admission;
   - select an account, either the strict owner or `selectCompact` with `ImageGeneration` and the cooldown exclusions;
   - call `billedAdmitted` with `target.accountNeutral = true`.
3. `billedAdmitted` treats image kinds like compaction: it uses the admission lease, or takes one for a pinned owner. An image kind uses `imageBudget`, which estimates input as `len(prompt)/4 + 1500` per image and allows 8192 output tokens. With `accountNeutral` it skips `recordOutcome` on success and refusal. Settlement stays as for other operations.
4. `Adapter.Images` rejects non-ChatGPT accounts, posts through `chatGPTBody` and validates a 2xx body: at least one `data[].b64_json` string, otherwise `invalid_upstream_response` with `Dispatched`. On success it marks `OutputObserved`.

Files And Functions:
 - planned: internal/application/codex_images.go#ImagesWithOptions, #imageBudget, #imageDiagnosticBody
 - existing: internal/application/codex_operations_accounting.go#billedAdmitted - image kinds, account-neutral outcomes
 - existing: internal/application/codex_operations.go#CodexOperationTarget, #CodexOperationProvider - `accountNeutral`, `Images`
 - existing: internal/application/response_request.go#responseRequest - `ImageGeneration`
 - existing: internal/application/response_ownership.go#accountCandidates - ChatGPT-only, no catalog filter
 - planned: internal/adapters/provider/images.go#Images
 - existing: test fakes of `CodexOperationProvider` (httpapi file_pin_test.go, codex_operations_test.go; application codex_operations_test.go, codex_operations_sqlite_test.go)

Tests:
internal/adapters/httpapi/codex_images_test.go
 - description: Codex generation round trip on /backend-api/codex/images/generations — the stub receives the exact body at /codex/images/generations with the account token and account header, the client gets the stub body unchanged, one image_generation event with model gpt-image-2 and the stub tokens is settled with cost 21×5+515×30 microdollars-per-token, and no reservation stays pending
   requirements: R5, R7, R8, R10
 - description: JSON edit on /v1/images/edits is forwarded to /codex/images/edits and settled as image_edit
   requirements: R7, R10
 - description: success without data and success without usage give 502; the latter keeps a reservation needing reconciliation
   requirements: R8
 - description: the account stays active after a success and after a refusal (account-neutral), including an account whose previous outcome was rate-limited
   requirements: R9
 - description: an edit with a file_id created through /backend-api/files goes to the owner account even when another account is preferred; an unknown file_id gives 409 without dispatch
   requirements: R6
 - description: an error diagnostic of a failed edit has no images or mask payload
   requirements: R10

### 3. Move an image-limit refusal to another account

429 usage-limit refusal before output --settle the attempt uncharged, cool the account down for images, select another--> [image from another account, last refusal returned]

Requirements:

  R11:
An upstream 429 usage-limit refusal that arrives before any output or usage SHALL be settled uncharged and exclude that account from image routing for 10 minutes. The same body SHALL then go to another eligible account, trying at most four accounts per request. If no account is left, the last refusal SHALL be returned unchanged.

  R12:
Other failures, responses that carried output or usage, and file-pinned edits SHALL NOT be sent to another account.

Implementation Logic:
1. `CodexOperations.imageCooldown` is a mutex-guarded `map[accountID]until`. Expired entries are pruned on read; the map is per process.
2. The loop marks the refusing account excluded and cooled, then calls `selectCompact(…, excluded)`. A selection error after at least one refusal returns that refusal.

Files And Functions:
 - planned: internal/application/codex_images.go#imageCooldowns, #coolImageAccount
 - existing: internal/application/codex_compact.go#compactQuotaRefusal - reused to classify the refusal

Tests:
internal/adapters/httpapi/codex_images_test.go
 - description: two accounts — the first answers 429 usage_limit_reached, the second returns an image; the client gets the second image, the first attempt is settled uncharged, both accounts keep active status, and a second request within 10 minutes reaches only the second account
   requirements: R11
 - description: a file-pinned edit refused with 429 is returned without trying the other account; a 500 is returned without failover
   requirements: R12
 - description: when every account refuses, the last 429 body reaches the client
   requirements: R11

## Implementation checklist

1. [x] OpenSpec change `restore-image-generation-routes` (proposal, tasks, context, delta) and this spec.
2. [x] Builtin prices for `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst` (R3).
3. [x] `responseRequest.ImageGeneration` in candidate selection (R5).
4. [x] `CodexOperationProvider.Images`, `CodexOperationTarget.accountNeutral`, `billedAdmitted` image kinds, budget and neutral outcomes, with test fakes updated (R8, R9).
5. [x] `ImagesWithOptions`: validation, policy, file owners, selection, cooldown and failover, diagnostics (R3–R6, R10–R12).
6. [x] `Adapter.Images` (R7, R8).
7. [x] HTTP routes, JSON reading, multipart conversion, result writing (R1, R2).
8. [x] Route tests for use cases 1–3 (`internal/adapters/httpapi/codex_images_test.go`); the success-body check is covered on the route.
9. [x] `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`; sync the delta into `openspec/specs/go-runtime/` with a context section, archive the change, release notes.

Deferred:
- GPT Image 2.5 Sunburst and Flare with real `model`, `quality` (up to `max`), `size` (up to 3840 px) and transparent backgrounds need an OpenAI API key source with an images capability. This includes a per-key or per-group image model, because Codex always sends `gpt-image-2`. The subscription endpoint ignores these fields.
- Streaming image responses (`stream: true`, `partial_images`): Codex does not use them, and the subscription endpoint's streaming contract is unverified.
- Image-limit headers are not turned into pool-level limit headers. A final refusal carries the upstream account's own headers in its body only; the response header whitelist is unchanged.
- The image-limit cooldown is per process and is not persisted.
- Image input tokens of edits are re-priced at the text-input rate, because events keep one input counter.

## Open questions

None blocking.

## Decision log

- 2026-10-29: Probes showed the subscription images endpoint ignores `model`, `quality`, `size` and reasoning fields: 1254×1254, `low`, 515 output tokens every time; the Responses tool path substitutes `gpt-image-2-codex`. The owner chose subscription routes now and an OpenAI API source for Sunburst/Flare later.
- 2026-10-29: Forward instead of translating to the Responses `image_generation` tool, as the legacy adapter did. Codex itself calls the images endpoint, the endpoint returns the OpenAI Images shape with usage, and translation would add a host model and SSE folding that buy nothing.
- 2026-10-29: Account-neutral outcomes. The image limit has its own window (`x-codex-active-limit: imagegen_premium`, 1440 minutes on Pro). Recording an image refusal as account quota would remove the account from text routing, and recording an image success would re-enable an account that is limited for text.
- 2026-10-29: Request kinds `image_generation` and `image_edit` keep image traffic distinguishable in diagnostics and reports. Settlement re-prices them from tokens like other subscription events.
- 2026-10-29: 32 MiB route maximum, the shared `MaxRequestBodyBytes`. Codex edits carry at most five images, and a larger limit would let one image request take a large share of the body budget on small hosts.
- 2026-10-29: Validation, run locally because GitHub Actions was unavailable: `make check` (vet, golangci-lint 0 issues, test, race, govulncheck, openspec), `make web-check` (1093 tests), `make web-deps build`, `make test-web-contract` (17 tests). A smoke run of the built binary answered 401 `invalid_api_key` on all four image routes without a key (404 before). Credential redaction now returns bodies without the credential or any `\u` escape unchanged, which also keeps the upstream image body byte-identical.
