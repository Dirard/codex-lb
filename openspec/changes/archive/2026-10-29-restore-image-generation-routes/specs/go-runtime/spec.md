## MODIFIED Requirements

### Requirement: Deliver selected functionality before cutover

The Go implementation SHALL provide the selected account/OAuth, many-to-many group, API-key, per-key limit, model, quota, price, report, admin-security, firewall, Codex Responses/file/image/voice, warmup/scheduled-ping and reset-credit functionality before production cutover. It SHALL NOT provide upstream install telemetry, multi-replica runtime coordination, the quota phase planner, guest dashboard access, PostgreSQL runtime or proxy pools. Binary/systemd and optional ordinary Docker delivery SHALL replace Helm/Kubernetes, Nix and distroless-specific delivery. Local diagnostics and usage statistics MUST remain available independently of removed telemetry.

#### Scenario: Images used by Codex are retained
- **WHEN** Codex submits supported image input or uses its built-in image generation workflow
- **THEN** the proxy preserves that capability on a capable provider
- **AND** Codex image generation and editing reach a subscription account through the image generation routes

#### Scenario: Account groups survive removal of proxy pools
- **WHEN** one account belongs to multiple groups
- **THEN** every group retains its independent membership, key policies and limits
- **AND** shared membership does not create additional upstream quota

#### Scenario: Purchased credits and separately metered quotas survive restart
- **WHEN** usage metadata is persisted or imported and the Go server restarts
- **THEN** purchased-credit status, monthly windows and additional quota windows retain their observed values and timestamps in routing and the dashboard
- **AND** usable purchased credits remain distinct from calculated subscription credits and permit admission after included primary, weekly or monthly windows are exhausted
- **AND** a model's canonical additional quota uses its own fresh windows and registry restrictions for new admissions, without falling back to standard windows when required evidence is absent
- **AND** the established-owner continuation exception remains unchanged
- **AND** absent additional-quota metadata preserves prior samples, an explicit empty list clears them, and older samples cannot replace newer values

## ADDED Requirements

### Requirement: Image generation routes use the subscription images endpoint

The runtime SHALL accept `POST /backend-api/codex/images/generations`, `POST /backend-api/codex/images/edits`, `POST /v1/images/generations` and `POST /v1/images/edits` from an authenticated API key, with or without one trailing slash. It SHALL forward the validated JSON request body to `/images/generations` or `/images/edits` under the configured ChatGPT Codex base URL of one ChatGPT subscription account, and SHALL return the upstream status and JSON body without rewriting them. `/v1/images/edits` SHALL also accept OpenAI multipart, with `image` and `image[]` file parts, at most one `mask` file part and text fields. It SHALL convert that form to the JSON shape, with each file as a base64 `data:` URL in `images[].image_url` or `mask.image_url`, and numeric and boolean fields as JSON numbers and booleans.

Before any reservation or dispatch, a request SHALL be rejected with 400 `invalid_request_error` naming the parameter in each of these cases:
- it is not a JSON object;
- its `prompt` is missing, empty or longer than 32000 characters;
- its `model` is neither absent nor a string naming a priced `gpt-image-` model;
- `n` is not an integer from 1 to 10;
- `stream` is `true`;
- an edit has no `images`, more than 16 `images`, or an entry without a non-empty `image_url` or `file_id` string.

An absent `model` SHALL become `gpt-image-2`. An enforced key model SHALL replace the image model only when the enforced model is itself an image model. A key with allowed models SHALL receive 403 `model_not_allowed` for an image model outside them. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum. The `model`, `quality`, `size`, `background` and other generation fields SHALL be forwarded as sent, because the subscription endpoint decides which of them it honors.

Accounts SHALL be selected by the shared balancer among the key's eligible ChatGPT accounts without model-catalog filtering. An edit that references any `file_id` SHALL be sent only to that file's recorded owner account. Unknown or conflicting owners SHALL return 409, and the edit SHALL never be retried on another account.

The API-key reservation SHALL be taken before dispatch. It SHALL be settled exactly once from the response `usage` at the image model's token rates. A successful response without at least one `data` entry carrying `b64_json` SHALL return 502. A successful response without usage SHALL return 502 and keep its reservation for reconciliation. Image requests SHALL NOT change account health or quota state, whether they succeed or are refused.

An upstream HTTP 429 usage-limit refusal that arrives before any output or usage SHALL be settled as a failed, uncharged attempt. That account SHALL then be excluded from image routing for 10 minutes, and the same request SHALL be sent to another eligible account, trying at most four accounts per request. Any other failure, and any response that carried output or usage, SHALL NOT be retried on another account.

Usage events SHALL record request kind `image_generation` or `image_edit` with the effective public image model. Error diagnostics of image requests SHALL NOT store `images` or `mask` payloads.

#### Scenario: Codex image generation through codex-lb
- **WHEN** Codex posts `{"prompt":"…","model":"gpt-image-2","background":"opaque","quality":"auto","size":"auto"}` to `/backend-api/codex/images/generations` with a valid key
- **THEN** one eligible ChatGPT account receives the same body at `/backend-api/codex/images/generations` under its access token and account header
- **AND** the client receives the upstream 200 body with `data[0].b64_json` unchanged
- **AND** one `image_generation` usage event with model `gpt-image-2` and the response token usage is settled once, and the account's health is unchanged

#### Scenario: Multipart image edit on the OpenAI-compatible route
- **WHEN** a client posts multipart `image`, `mask`, `prompt` and `n=1` to `/v1/images/edits`
- **THEN** the upstream receives JSON `images[0].image_url` and `mask.image_url` as base64 data URLs with the same content types, `prompt` as a string and `n` as a number

#### Scenario: Invalid image requests never dispatch
- **WHEN** a request lacks `prompt`, names a non-image `model`, asks for `stream: true`, or sends an edit without images
- **THEN** the client receives 400 `invalid_request_error` with the parameter, and no reservation, account selection or upstream call happens

#### Scenario: Key model policy applies to image models
- **WHEN** a key's allowed models do not include `gpt-image-2`
- **THEN** an image request for `gpt-image-2` receives 403 `model_not_allowed` before dispatch
- **AND** a key whose enforced model is a text model keeps the requested image model

#### Scenario: An exhausted image limit moves the request to another account
- **WHEN** the first selected account answers 429 `usage_limit_reached` before producing an image and a second eligible account exists
- **THEN** the same body is sent to the second account and its image is returned
- **AND** the first attempt is settled uncharged, both accounts keep their health and quota state, and the first account receives no image request for 10 minutes

#### Scenario: A file-pinned edit stays on its owner
- **WHEN** an edit references a `file_id` created through codex-lb on one account and that account refuses with 429
- **THEN** the refusal is returned to the client and no other account receives the edit
