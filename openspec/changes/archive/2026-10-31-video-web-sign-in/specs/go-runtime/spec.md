## MODIFIED Requirements

### Requirement: Grok video generation is a separate pool and REST surface

The runtime SHALL keep Grok accounts in a video pool separate from `accounts`, added only through a device sign-in started from the dashboard at `https://auth.x.ai`, with tokens encrypted at rest, refreshed through that issuer's token endpoint, and revoked on a best-effort basis when the account is deleted. The device code MUST stay in process memory. Signing in to the same xAI user SHALL replace its tokens instead of adding an account. Video accounts MUST NOT be visible to any other proxy route, routing decision or usage report, and video jobs MUST NOT consume API-key limits or appear in usage events.

The runtime SHALL serve `POST /video/v1/generations`, `GET /video/v1/generations/{id}` and `GET /video/v1/generations` to an active API key on the video allow list. Any other active key SHALL receive 403 `video_access_denied` before dispatch, and a request without a valid bearer key, including keyless local access, SHALL receive 401. A key SHALL see only its own jobs, and another key's job id SHALL answer 404.

Creation SHALL accept `model`, `prompt`, `image`, `last_frame`, `keyframes`, `reference_images`, `reference_audios`, `duration`, `aspect_ratio` and `resolution`, and SHALL forward them unchanged to `https://api.x.ai/v1/videos/generations`. It SHALL reject with 400 and the parameter name any other field, any `file_id`, an unsupported model, a duration outside 1 to 15 seconds, more than four keyframes, `last_frame`, `keyframes` or references on a model other than `grok-imagine-video-1.5`, and a request with no prompt and no pinned frame. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum.

A job row SHALL be written before dispatch, and a successful creation SHALL answer 202 with the local `id`. An upstream 401 after one token refresh, 403 or 429 SHALL mark that attempt refused, take the account out of selection and move the request to another eligible account, trying at most four accounts. Any other failure, or one that may have executed, MUST NOT be sent to another account. When all tried accounts refused, the client SHALL receive 429 `video_pool_exhausted`.

Status reads and a background sweep SHALL query only the job's account. A job SHALL leave `pending` exactly once. `done` SHALL record `usage.cost_in_usd_ticks` at 1e-10 USD per tick and the duration. `failed` and `expired` SHALL record no cost. A job still `pending` one hour after creation SHALL become `unknown`, and a job left in `dispatching` for 15 minutes SHALL become `unknown`. Responses SHALL NOT name the account or the upstream request id. Logs and diagnostics MUST NOT contain tokens, prompts, image or audio payloads, or video URLs.

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

