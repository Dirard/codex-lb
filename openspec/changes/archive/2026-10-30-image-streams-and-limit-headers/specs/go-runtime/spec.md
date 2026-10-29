## MODIFIED Requirements

### Requirement: Image generation routes use the subscription images endpoint

The runtime SHALL accept `POST /backend-api/codex/images/generations`, `POST /backend-api/codex/images/edits`, `POST /v1/images/generations` and `POST /v1/images/edits` from an authenticated API key, with or without one trailing slash. It SHALL forward the validated JSON request body to `/images/generations` or `/images/edits` under the configured ChatGPT Codex base URL of one ChatGPT subscription account, and SHALL return the upstream status and JSON body without rewriting them. `/v1/images/edits` SHALL also accept OpenAI multipart, with `image` and `image[]` file parts, at most one `mask` file part and text fields. It SHALL convert that form to the JSON shape, with each file as a base64 `data:` URL in `images[].image_url` or `mask.image_url`, and numeric and boolean fields as JSON numbers and booleans.

Before any reservation or dispatch, a request SHALL be rejected with 400 `invalid_request_error` naming the parameter in each of these cases:
- it is not a JSON object;
- its `prompt` is missing, empty or longer than 32000 characters;
- its `model` is neither absent nor a string naming a priced `gpt-image-` model;
- `n` is not an integer from 1 to 10;
- `stream` is present and is not a boolean;
- an edit has no `images`, more than 16 `images`, or an entry without a non-empty `image_url` or `file_id` string.

An absent `model` SHALL become `gpt-image-2`. An enforced key model SHALL replace the image model only when the enforced model is itself an image model. A key with allowed models SHALL receive 403 `model_not_allowed` for an image model outside them. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum. The `model`, `quality`, `size`, `background`, `partial_images` and other generation fields SHALL be forwarded as sent, because the subscription endpoint decides which of them it honors.

A request with `stream: true` SHALL be sent with `Accept: text/event-stream`. Two kinds of upstream answer are possible:
- **Event stream.** It SHALL be relayed to the client event by event with each `event:` name and `data:` payload unchanged, flushed as it arrives, and settled from the `usage` of its `image_generation.completed` or `image_edit.completed` event. If the stream ends without a completed event carrying `b64_json`, the client SHALL receive a final `error` event and the reservation SHALL be kept for reconciliation.
- **JSON answer.** It SHALL be returned as JSON, exactly as for a non-streaming request.

Accounts SHALL be selected by the shared balancer among the key's eligible ChatGPT accounts without model-catalog filtering. An edit that references any `file_id` SHALL be sent only to that file's recorded owner account. Unknown or conflicting owners SHALL return 409, and the edit SHALL never be retried on another account.

The API-key reservation SHALL be taken before dispatch. It SHALL be settled exactly once from the response `usage` at the image model's token rates. A successful response without at least one `data` entry carrying `b64_json` SHALL return 502. A successful response without usage SHALL return 502 and keep its reservation for reconciliation. Image requests SHALL NOT change account health or quota state, whether they succeed or are refused.

An upstream HTTP 429 usage-limit refusal that arrives before any output or usage SHALL be settled as a failed, uncharged attempt. That account SHALL then be excluded from image routing for 10 minutes, and the same request SHALL be sent to another eligible account, trying at most four accounts per request. Any other failure, and any response that carried output or usage, SHALL NOT be retried on another account.

When the request finally fails with an upstream 429, the response SHALL carry these upstream headers unchanged:
- `Retry-After`;
- `X-Codex-Active-Limit`;
- every `X-Image-Gen-*` header;
- the `X-Codex-Primary-*` and `X-Codex-Secondary-*` used-percent, window and reset headers, when `X-Codex-Active-Limit` names an image limit.

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
- **WHEN** a request lacks `prompt`, names a non-image `model`, sends a non-boolean `stream`, or sends an edit without images
- **THEN** the client receives 400 `invalid_request_error` with the parameter, and no reservation, account selection or upstream call happens

#### Scenario: Key model policy applies to image models
- **WHEN** a key's allowed models do not include `gpt-image-2`
- **THEN** an image request for `gpt-image-2` receives 403 `model_not_allowed` before dispatch
- **AND** a key whose enforced model is a text model keeps the requested image model

#### Scenario: A streamed generation is relayed and settled once
- **WHEN** a client posts a generation with `stream: true` and the upstream answers `keepalive` events followed by `image_generation.completed` with `b64_json` and `usage`
- **THEN** the client receives the same events in the same order as `text/event-stream`
- **AND** one `image_generation` usage event is settled from the completed event's usage

#### Scenario: A streaming edit answered with JSON stays JSON
- **WHEN** a client posts an edit with `stream: true` and the upstream answers `application/json`
- **THEN** the client receives that JSON body unchanged

#### Scenario: An exhausted image limit moves the request to another account
- **WHEN** the first selected account answers 429 `usage_limit_reached` before producing an image and a second eligible account exists
- **THEN** the same body is sent to the second account and its image is returned
- **AND** the first attempt is settled uncharged, both accounts keep their health and quota state, and the first account receives no image request for 10 minutes

#### Scenario: A final image-limit refusal carries the reset headers
- **WHEN** every tried account answers 429 `usage_limit_reached` with `Retry-After`, `X-Codex-Active-Limit: image_gen` and `X-Image-Gen-Primary-Reset-At`
- **THEN** the client receives the last 429 body together with those headers unchanged

#### Scenario: A file-pinned edit stays on its owner
- **WHEN** an edit references a `file_id` created through codex-lb on one account and that account refuses with 429
- **THEN** the refusal is returned to the client and no other account receives the edit

## ADDED Requirements

### Requirement: Image-limit refusals on Responses leave the account usable

The runtime SHALL classify an upstream refusal as `image_generation_limit` instead of account quota exhaustion when all of these hold:
- the upstream is a Responses request over HTTP, or the WebSocket handshake for one;
- the upstream answers HTTP 429;
- the `X-Codex-Active-Limit` header names an image generation limit, that is an identifier containing `image`.

For such a refusal the runtime SHALL NOT mark the account `quota_exceeded`, record quota evidence or replay the request on another account. It SHALL settle the attempt uncharged. It SHALL return HTTP 429 to the client with an `error` object whose `type` and `code` are `usage_limit_reached`, together with the reset headers defined for the image routes.

A 429 without such a header SHALL keep the existing account quota handling.

#### Scenario: Hosted image tool refused by the image limit
- **WHEN** a `/backend-api/codex/responses` request with the hosted `image_generation` tool is refused with 429 `usage_limit_reached` and `X-Codex-Active-Limit: imagegen_premium`
- **THEN** the client receives 429 with `error.type` `usage_limit_reached` and the upstream reset headers
- **AND** the account stays active, no other account receives the request, and the reservation is released uncharged

#### Scenario: Account quota refusal is unchanged
- **WHEN** a Responses request is refused with 429 `usage_limit_reached` without an image `X-Codex-Active-Limit`
- **THEN** the account is marked as quota-exhausted and the request moves to another account as before
