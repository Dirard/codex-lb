## MODIFIED Requirements

### Requirement: Grok video generation is a separate pool and REST surface

The runtime SHALL keep Grok accounts in a video pool separate from `accounts`, added only through a device sign-in started from the dashboard at `https://auth.x.ai`, with tokens encrypted at rest, refreshed through that issuer's token endpoint, and revoked on a best-effort basis when the account is deleted. The device code MUST stay in process memory. Signing in to the same xAI user SHALL replace its tokens instead of adding an account. Video accounts MUST NOT be visible to any other proxy route, routing decision or usage report, and video jobs MUST NOT consume API-key limits or appear in usage events.

The runtime SHALL serve `POST /video/v1/generations`, `GET /video/v1/generations/{id}` and `GET /video/v1/generations` to an active API key on the video allow list. Any other active key SHALL receive 403 `video_access_denied` before dispatch, and a request without a valid bearer key, including keyless local access, SHALL receive 401. A key SHALL see only its own jobs, and another key's job id SHALL answer 404.

Creation SHALL accept `model`, `prompt`, `image`, `last_frame`, `keyframes`, `reference_images`, `reference_audios`, `duration`, `aspect_ratio` and `resolution`, and SHALL forward them unchanged to `https://api.x.ai/v1/videos/generations`. It SHALL reject with 400 and the parameter name any other field, any `file_id`, an unsupported model, a duration outside 1 to 15 seconds, more than four keyframes, `last_frame`, `keyframes` or references on a model other than `grok-imagine-video-1.5`, and a request with no prompt and no pinned frame. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum.

A job row SHALL be written before dispatch, and a successful creation SHALL answer 202 with the local `id`. An upstream 401 after one token refresh, 403 or 429 SHALL mark that attempt refused, take the account out of selection and move the request to another eligible account, trying at most four accounts. Any other failure, or one that may have executed, MUST NOT be sent to another account. When all tried accounts refused, the client SHALL receive 429 `video_pool_exhausted`.

Status reads and a background sweep SHALL query only the job's account. A job SHALL leave `pending` exactly once. `done` SHALL record `usage.cost_in_usd_ticks` at 1e-10 USD per tick and the duration. `failed` and `expired` SHALL record no cost. A finished job without a video SHALL answer `error` with its code and `error_message` with its reason in words, xAI's own text when xAI gave one, and the dashboard job list SHALL show that reason. A `done` answer with `respect_moderation: false` SHALL finish the job as `failed` with `video_moderated`, and a `done` answer without a video URL SHALL finish it as `failed` with `video_failed`; neither SHALL give the client a URL or record a cost. A job still `pending` one hour after creation SHALL become `unknown`, and a job left in `dispatching` for 15 minutes SHALL become `unknown`. Responses SHALL NOT name the account or the upstream request id. A due poll of a job older than two minutes that does not finish it SHALL log a warning with the local job id, its age, xAI's HTTP status and its status word, at most once per minute per job. Logs and diagnostics MUST NOT contain tokens, prompts, image or audio payloads, or video URLs.

The runtime SHALL read each active or paused account's subscription pool right after a sign-in and every 5 minutes from `https://cli-chat-proxy.grok.com/v1/billing?format=credits`, for which sign-in SHALL request the scope `grok-cli:access`, and SHALL store the usage percentage and period end. A failed read SHALL keep the last value, SHALL be recorded on the account, and MUST NOT change the account's status or stop it from generating. Selection SHALL skip an account at 100% or more until its period ends, SHALL prefer accounts with more left in 10-point bands, SHALL treat an elapsed period as unused, and SHALL place an account without a reading after those with one. The dashboard SHALL show what is left and when it resets, without calling xAI when the page is read.

#### Scenario: First frame, last frame and text
- **WHEN** an allowed key posts `grok-imagine-video-1.5` with `image`, `last_frame` and `prompt` to `/video/v1/generations`
- **THEN** one pool account receives those fields unchanged and the client receives 202 with a local `id`
- **AND** `GET /video/v1/generations/{id}` queries only that account and returns `video.url` when `status` is `done`

#### Scenario: The same API key without video access
- **WHEN** a key that works on `/v1/responses` is not on the video allow list
- **THEN** `/video/v1/generations` answers 403 `video_access_denied` with no job and no upstream call
- **AND** `/v1/responses` keeps working for that key

#### Scenario: Other routes do not see the pool
- **WHEN** the pool has active accounts and a key sends a Responses, Chat Completions, image, compact or transcription request
- **THEN** no video account is a candidate, and the key's limits and usage reports do not change because of video jobs

#### Scenario: An exhausted account moves creation to another account
- **WHEN** the selected account answers 429 at creation and another eligible account exists
- **THEN** the first attempt is marked refused with no cost, that account leaves selection until its cooldown ends, and the second account creates the job

#### Scenario: A job's cost is recorded once
- **WHEN** a client poll and the background sweep both observe `done` for one job
- **THEN** one transition is applied and the reported cost counts once

#### Scenario: A client never polls
- **WHEN** a client creates a job and never polls it
- **THEN** the background sweep records its terminal status and cost, or marks it `unknown` after one hour

#### Scenario: Adding an account from the dashboard
- **WHEN** the administrator starts a sign-in on the Video generation page and approves it at xAI
- **THEN** the page shows xAI's link and code, the next status request after approval stores the account, and the dialog closes
- **AND** closing the dialog first means the code is never exchanged, and no response or log carries the device code or tokens

#### Scenario: The page shows what is left
- **WHEN** an account signed in with `grok-cli:access` has used 25% of its weekly pool
- **THEN** the Video page shows 75% left and the reset time

#### Scenario: An account signed in before this permission
- **WHEN** the pool read answers 403 for an account
- **THEN** the page asks to sign in again, and the account stays active and keeps generating

#### Scenario: An exhausted account is skipped
- **WHEN** an account reads 100% and its period has not ended
- **THEN** no video request goes to it, another account takes the job, and with no other account the client receives 429 `video_pool_exhausted` with `Retry-After` of the period end

#### Scenario: A moderated video does not stay pending

- **WHEN** xAI answers a poll with `status: done` and `respect_moderation: false`
- **THEN** the job becomes `failed` with `video_moderated`, without a URL or cost, on that poll

#### Scenario: An unfinished job leaves a trace

- **WHEN** a job older than two minutes is polled by clients several times within a minute and xAI still answers `pending`
- **THEN** one warning names the job, its age, the HTTP status and `pending`, and it contains no upstream request id, token or URL

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

The wait for the first byte of an upstream answer SHALL last up to three minutes; a longer wait SHALL answer 504 `upstream_timeout`. Every provider failure answer SHALL name its reason in `error.message`.

An upstream HTTP 429 usage-limit refusal that arrives before any output or usage SHALL be settled as a failed, uncharged attempt. That account SHALL then be excluded from image routing for 10 minutes, and the same request SHALL be sent to another eligible account, trying at most four accounts per request. Any other failure, and any response that carried output or usage, SHALL NOT be retried on another account.

When the request finally fails with an upstream 429, the response SHALL carry these upstream headers unchanged:
- `Retry-After`;
- `X-Codex-Active-Limit`;
- every `X-Image-Gen-*` header;
- when `X-Codex-Active-Limit` names an image limit, that limit's own window headers (`X-<limit id with dashes>-*`, for example `X-Imagegen-Premium-*` for `imagegen_premium`) and the `X-Codex-Primary-*` and `X-Codex-Secondary-*` used-percent, window and reset headers.

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
- **WHEN** every tried account answers 429 `usage_limit_reached` with `Retry-After`, `X-Codex-Active-Limit: imagegen_premium`, `X-Image-Gen-Primary-Reset-At` and `X-Imagegen-Premium-Primary-Reset-At`
- **THEN** the client receives the last 429 body together with those headers unchanged
- **AND** the account's plan and credit headers are not passed

#### Scenario: A file-pinned edit stays on its owner
- **WHEN** an edit references a `file_id` created through codex-lb on one account and that account refuses with 429
- **THEN** the refusal is returned to the client and no other account receives the edit

#### Scenario: An image edit gets no answer in time

- **WHEN** the upstream sends no first byte within the bound
- **THEN** the client gets 504 `upstream_timeout` with a message that says so, not 502 `upstream_unavailable`
