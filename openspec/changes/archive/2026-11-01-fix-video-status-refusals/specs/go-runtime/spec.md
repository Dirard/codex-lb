## MODIFIED Requirements

### Requirement: Grok video generation is a separate pool and REST surface

The runtime SHALL keep Grok accounts in a video pool separate from `accounts`, added only through a device sign-in started from the dashboard at `https://auth.x.ai`, with tokens encrypted at rest, refreshed through that issuer's token endpoint, and revoked on a best-effort basis when the account is deleted. The device code MUST stay in process memory. Signing in to the same xAI user SHALL replace its tokens instead of adding an account. Video accounts MUST NOT be visible to any other proxy route, routing decision or usage report, and video jobs MUST NOT consume API-key limits or appear in usage events.

The runtime SHALL serve `POST /video/v1/generations`, `GET /video/v1/generations/{id}` and `GET /video/v1/generations` to an active API key on the video allow list. Any other active key SHALL receive 403 `video_access_denied` before dispatch, and a request without a valid bearer key, including keyless local access, SHALL receive 401. A key SHALL see only its own jobs, and another key's job id SHALL answer 404.

Creation SHALL accept `model`, `prompt`, `image`, `last_frame`, `keyframes`, `reference_images`, `reference_audios`, `duration`, `aspect_ratio` and `resolution`, and SHALL forward them unchanged to `https://api.x.ai/v1/videos/generations`. It SHALL reject with 400 and the parameter name any other field, any `file_id`, an unsupported model, a duration outside 1 to 15 seconds, more than four keyframes, `last_frame`, `keyframes` or references on a model other than `grok-imagine-video-1.5`, and a request with no prompt and no pinned frame. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum.

A job row SHALL be written before dispatch, and a successful creation SHALL answer 202 with the local `id`. An upstream 401 after one token refresh, 403 or 429 SHALL mark that attempt refused, take the account out of selection and move the request to another eligible account, trying at most four accounts. Any other failure, or one that may have executed, MUST NOT be sent to another account. When all tried accounts refused, the client SHALL receive 429 `video_pool_exhausted`.

Status reads and a background sweep SHALL query only the job's account. A job SHALL leave `pending` exactly once. `done` SHALL record `usage.cost_in_usd_ticks` at 1e-10 USD per tick and the duration. `failed` and `expired` SHALL record no cost. A finished job without a video SHALL answer `error` with its code and `error_message` with its reason in words, xAI's own text when xAI gave one, and the dashboard job list SHALL show that reason. A `done` answer with `respect_moderation: false` SHALL finish the job as `failed` with `video_moderated`, and a `done` answer without a video URL SHALL finish it as `failed` with `video_failed`; neither SHALL give the client a URL or record a cost. An HTTP 400 answer to a status read of a job older than two minutes SHALL finish it as `failed` with `video_failed` and xAI's text as the reason; a younger job SHALL keep polling. A job still `pending` one hour after creation SHALL become `unknown`, and a job left in `dispatching` for 15 minutes SHALL become `unknown`. Responses SHALL NOT name the account or the upstream request id. Every due poll that does not finish a job SHALL log the local job id, its age, xAI's HTTP status and its status word, as a warning when the job is older than two minutes and otherwise as information, and a finished poll SHALL log the job's outcome and xAI's HTTP status. The job row SHALL keep xAI's last answer to creation or to a due or finishing poll: its HTTP status, its body up to 8 KiB and when it came, or the transport error of a read that got no answer, and SHALL keep it after the job finishes and until the row is pruned. The dashboard job list SHALL show that answer with any `url` value hidden; the REST surface SHALL NOT return it. The runtime SHALL NOT limit how many unfinished jobs an account holds: selection SHALL prefer the account with the fewest, and xAI's own 429 SHALL be the bound. When no account is active, the 503 `video_no_accounts` message SHALL say so. Logs and diagnostics MUST NOT contain tokens, prompts, image or audio payloads, video URLs or xAI's response bodies.

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

#### Scenario: xAI refuses to read a job

- **WHEN** xAI answers a due status read of a job older than two minutes with HTTP 400 and a body with `error`
- **THEN** the job becomes `failed` with `video_failed` and that text in `error_message`, without a URL or cost, on that poll
- **AND** the dashboard job list shows HTTP 400 and the body

#### Scenario: A young job survives a refused read

- **WHEN** xAI answers a due status read of a job younger than two minutes with HTTP 400
- **THEN** the job stays `pending`, the log records the HTTP status at information level, and the row keeps the answer

#### Scenario: xAI's answers stay on the job and out of the log

- **WHEN** xAI answers creation or a poll with a body that holds text and a `url`
- **THEN** the row keeps the body, the dashboard shows it with the `url` value hidden, and neither the log nor the REST surface contains it

#### Scenario: Hung jobs do not block new ones

- **WHEN** a request arrives while the only active account holds four or more unfinished jobs
- **THEN** the job is sent to xAI on that account

#### Scenario: No account is active

- **WHEN** a request arrives and no video account is active, cooling down or exhausted
- **THEN** the client receives 503 `video_no_accounts` with a message that says no account is active, and xAI is not called
