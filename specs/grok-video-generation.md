# Grok video generation: a separate account pool and REST behind the same API key

- Last edited with skill pack: `0.2.2`

## Title and scope

Video generation is a separate function of codex-lb: several of the owner's SuperGrok accounts form their own pool on a dashboard page, and allowed API keys use it through a separate REST surface `/video/v1/*`, with text, first frame, last frame, keyframes and references as input. The only thing it shares with the ChatGPT pool is the API key, the body budget and the admin dashboard; accounts, routing, accounting and reports stay apart.

## Planning anchor

- Entrypoints (all planned): `POST /video/v1/generations`, `GET /video/v1/generations/{id}`, `GET /video/v1/generations`, the dashboard page `/video` and `/api/video/*`.
- Changed assumption: every upstream call so far finishes within its client request and runs on a row of `accounts`. A video job outlives the request, and a Grok account is not a Codex account.
- Evidence: the probe of 2026-10-31 with one `grok-imagine-video` job through the OAuth token of the official CLI (202 `pending`, 200 `done` after about 20 s, 848×480, `usage.cost_in_usd_ticks: 500000000`, a `vidgen.x.ai` URL readable without auth), xAI's [reference-to-video](https://docs.x.ai/developers/model-capabilities/video/reference-to-video) and [videos API](https://docs.x.ai/developers/rest-api-reference/inference/videos) pages, and the OIDC discovery document of `https://auth.x.ai`.
- Non-local: migration, new xAI adapter, new pool service, proxy mount and firewall prefix, admin routes, composition root, dashboard navigation and page, OpenSpec `go-runtime`.
- Impacted specs in `specs/`: `specs/request-body-budget.md` — action `reuse` (the new create route uses the same lease). `specs/image-generation-routes.md` — action `reuse` (unrelated subscription; its deferred OpenAI API source is not touched). OpenSpec change `add-grok-video-pool` adds «Grok video generation is a separate pool and REST surface».

## Connected groups or observed existing logic

### Observed Existing Logic

1. Mount and auth: `internal/adapters/httpapi/server.go#Handler` mounts the proxy under `/v1/`, `/backend-api/` and `/api/codex/`; `firewall.go:79` lists the same prefixes; `authenticateProxyKey` resolves the key and may admit keyless local clients as `domain.LocalProxyKeyID`. CSRF protection wraps the whole mux, and bearer clients without browser headers pass it as they do on `/v1/`.
2. Body budget: handlers take `h.bodies.lease(keyID)` and read through `readOperationJSON` up to `domain.MaxRequestBodyBytes` (32 MiB).
3. Why not `accounts`: `eligibleAccounts` admits every `kind!='chatgpt'` row, and `ModelSourceService.Price` returns a not-found error (not `ErrUnpriced`) for an external row without a model source, which would fail all text routing; older code would do the same after a rollback. `accounts.kind` has a CHECK limited to `chatgpt` and `external`, and about 60 code sites branch on it.
4. Why not the usage ledger: reservations, settlement, rollups and repricing assume token or per-minute events on `accounts`, and a reservation must be settled within its request or reconciled within an hour. A separate job table with one guarded transition gives exactly-once cost with less risk, and it matches the owner's decision that video is separate.
5. Background: `application.RunPeriodic` with `BackgroundSchedule` runs recoverable periodic tasks that the runtime cancels and joins; two workers can overlap during a seamless update, so background writes must be guarded in SQL.
6. Vault: `application.SecretCipher` encrypts credentials; the existing account import reads an `auth_json` multipart part bounded to 1 MiB (`internal/adapters/httpapi/accounts.go#importAccount`).
7. Official CLI `auth.json`: a map with one entry whose fields include `key`, `refresh_token`, `expires_at`, `user_id`, `email`, `oidc_issuer` and `oidc_client_id`.
8. Probe of 2026-10-31 with the owner's sign-in and `User-Agent: codex-lb/probe`:
   - `grok-imagine-video-1.5` with `image`, `last_frame`, `prompt`, 2 s, 480p: 200 with `request_id`, 202 `pending`, then 200 `done` after 29 s, `usage.cost_in_usd_ticks: 1800000000`. The clip's first and last frames matched the given images (SSIM 0.99 and 0.96).
   - `grok-imagine-video` with `last_frame`: 400 `{"code":"invalid-argument","error":"`last_frame` is not supported for this model."}`. An unknown job id: 404 `{"code":"not-found","error":…}`.
   - Refresh at `https://auth.x.ai/oauth2/token` with form fields `grant_type`, `client_id`, `refresh_token` works outside the CLI. Access tokens last 6 hours (`expires_in: 21600`, scope includes `api:access`), and the refresh token rotates on every refresh.
   - `grok logout` after a refresh left the rotated tokens working. Logout before any refresh was not tested.
   - `POST https://auth.x.ai/oauth2/revoke` with `client_id`, `token` and `token_type_hint=refresh_token` gives 200, after which refresh gives 400 `invalid_grant`. An already issued access token keeps working until it expires.
   - Not observed: the refusal at an exhausted limit, and the pool cost.

## Use cases

### 1. Build and operate the video pool
official CLI auth.json --validate the issuer, encrypt the tokens and upsert by xAI user--> [pool account, 400 invalid_xai_auth] --pause, resume, rename or delete on the Video generation page--> operated pool account

Requirements:

  R1:
`POST /api/video/accounts/import` SHALL accept one `auth_json` file of at most 1 MiB holding exactly one entry, SHALL accept it only when `oidc_issuer` is exactly `https://auth.x.ai` and `key`, `refresh_token`, `oidc_client_id` and `user_id` are present, SHALL store the tokens encrypted, and SHALL replace the tokens of an account with the same `user_id` instead of adding another. It SHALL refresh the token once before saving, which proves the credential and detaches the stored session from the CLI's copy; a refused refresh SHALL give 400 `invalid_xai_auth` and store nothing. Error messages MUST NOT echo the file.

  R2:
Video accounts SHALL be stored only in `video_accounts`; no row SHALL be written to `accounts`, `account_credentials`, groups or usage tables.

  R3:
The administrator SHALL be able to list accounts with label, email, status, cooldown end, last refusal code, job counts and reported cost, and to pause, resume, rename and delete them. Deleting an account SHALL revoke its refresh token on a best-effort basis and mark its `pending` jobs `unknown`; responses SHALL NOT include tokens.

Tables:
video_accounts
 - id: TEXT primary key # vacc_ + random
 - xai_user_id: TEXT unique
 - email: TEXT
 - label: TEXT
 - status: TEXT # active, paused, reauth_required, access_denied
 - access_token_encrypted: BLOB
 - refresh_token_encrypted: BLOB
 - oidc_client_id: TEXT
 - access_expires_at: INTEGER
 - token_version: INTEGER # guards refresh rotation
 - refresh_claimed_until: INTEGER
 - cooldown_until: INTEGER
 - last_refusal_code: TEXT
 - last_used_at: INTEGER
 - created_at: INTEGER
 - updated_at: INTEGER

Files And Functions:
 - planned: internal/domain/video.go#VideoAccount, #VideoJob, #VideoJobStatus
 - planned: internal/adapters/sqlite/video_schema.go#schemaV44 - additive; add 44 to `additiveMigrations`
 - planned: internal/adapters/sqlite/video_pool.go
 - planned: internal/application/video_pool.go#ImportAccount, #parseCLIAuth
 - planned: internal/adapters/httpapi/video_admin.go#registerVideoAdminRoutes

Tests:
 - description: a valid file creates one account; importing the same user replaces tokens; a wrong issuer, two entries, missing fields and an oversized file give 400 without echoing their content
   requirements: R1
 - description: after an import, `accounts`, `EligibleAccounts`, the overview and `/api/accounts` are unchanged
   requirements: R2
 - description: list hides tokens; pause removes an account from selection; delete marks its pending jobs unknown
   requirements: R3
 - description: migrating every earlier version ends at user_version 44, and code of schema 43 keeps working on the result
   requirements: R2

### 2. Keep account tokens valid
stored account token --refresh when it expires within a minute or after a 401, guarded across workers--> [fresh access token, account marked reauth_required]

Requirements:

  R4:
An access token expiring within 60 seconds, or rejected with 401 once, SHALL be refreshed at `https://auth.x.ai/oauth2/token` with the account's `oidc_client_id` and refresh token before the upstream call is repeated once. A rotated refresh token SHALL be stored in the same write as the access token.

  R5:
At most one worker SHALL refresh one account at a time, and a stale refresh result MUST NOT overwrite newer tokens. A definitive refusal (`invalid_grant` or another 4xx) SHALL set `reauth_required`; a network failure SHALL leave the status unchanged and fail only this attempt. No token SHALL be logged, and no lock or transaction SHALL be held across the refresh call.

Implementation Logic:
1. Per-process single flight by account id joins concurrent callers.
2. A short write transaction sets `refresh_claimed_until = now + 30 s` only when the claim is free; a worker that cannot claim waits briefly and rereads the tokens.
3. The result is saved with `WHERE token_version = ?` and increments the version; a lost compare discards the result and rereads.

Files And Functions:
 - planned: internal/adapters/xai/client.go#Refresh, #CreateVideo, #GetVideo
 - planned: internal/application/video_tokens.go#accessToken

Tests:
 - description: an expiring token is refreshed once for concurrent callers and the rotated refresh token is stored encrypted
   requirements: R4, R5
 - description: a 401 refreshes and repeats once; a second 401 refuses the attempt
   requirements: R4
 - description: invalid_grant sets reauth_required; a connection failure does not
   requirements: R5
 - description: a result with an old token_version does not overwrite newer tokens
   requirements: R5

### 3. Allow an API key to use video
existing API key --the administrator adds it to the video allow list--> key allowed for /video/v1/*

Requirements:

  R6:
A key SHALL get 403 `video_access_denied` until it is listed in `video_allowed_keys`. Listing SHALL change nothing on other routes. An inactive, expired or deleted key and a request without a bearer key, including keyless local access, SHALL get 401 `invalid_api_key`, and internal principals MUST NOT be listable.

Tables:
video_allowed_keys
 - api_key_id: TEXT primary key references api_keys
 - created_at: INTEGER

Events And Endpoints:
 - GET /api/video/keys and PUT /api/video/keys/{id} and DELETE /api/video/keys/{id}

Tests:
 - description: the same key gets 403 video_access_denied on video before listing and passes after it, while /v1/responses behaves the same in both cases
   requirements: R6
 - description: an inactive listed key and a request without a key get 401 with no job row; the internal principal cannot be listed
   requirements: R6

### 4. Create a generation
allowed key request --charge the body and validate--> [validated generation, 400 with parameter, 413, 503 body budget] --write a dispatching job and select an account--> [dispatching job, 503 video_no_accounts] --post to xAI--> [pending job answered 202, refusal moved to another account, 429 video_pool_exhausted, failed attempt answered 502]

Requirements:

  R7:
`POST /video/v1/generations` SHALL accept JSON with `model`, `prompt`, `image`, `last_frame`, `keyframes`, `reference_images`, `reference_audios`, `duration`, `aspect_ratio` and `resolution`, charged to the shared body budget with the 32 MiB maximum, and SHALL forward those fields unchanged.

  R8:
Before any job row or upstream call it SHALL reject with 400 `invalid_request` and the parameter name: any other field (which also excludes `output`, `storage_options` and `user`), any `file_id`, a model outside the table below, a duration outside 1–15 seconds, more than four keyframes, a keyframe `timestamp_s` not strictly inside the duration, `last_frame`, `keyframes` or references on a model other than `grok-imagine-video-1.5`, and a request with neither `prompt` nor a pinned frame.

  R9:
Selection SHALL take accounts that are `active`, past `cooldown_until` and below four unfinished jobs, ordered by fewest unfinished jobs and then least recently used. A `dispatching` row SHALL be written first, so concurrent requests and overlapping workers count it.

  R10:
An upstream 401 after one refresh, 403 or 429 SHALL mark the attempt `refused` and move the same body to another eligible account, at most four accounts per request. A 429 SHALL set `cooldown_until` from `Retry-After`, or one hour without it; a 403 SHALL set `access_denied`. Other 4xx SHALL mark the job `rejected` and return the client error without failover. A 5xx, a transport failure or an unreadable success MAY have executed: the job SHALL become `unknown`, the client SHALL get 502, and it MUST NOT be sent elsewhere.

  R11:
A success SHALL store the upstream request id on the job, make it `pending` and answer 202 with `{id, status, model, created_at}`. When every tried account refused, the client SHALL get 429 `video_pool_exhausted` with `Retry-After` of the nearest cooldown; with no eligible account at all and no cooldown, 503 `video_no_accounts`.

Data:
supported_models:

| model | text | first frame | last frame, keyframes | references |
|---|---|---|---|---|
| `grok-imagine-video` | yes | yes | no (xAI answers 400) | no |
| `grok-imagine-video-1.5` | yes | yes | yes | yes |
| `grok-imagine-video-1.5-lite` | yes | yes | no | no |

Tables:
video_jobs
 - id: TEXT primary key # vid_ + random
 - api_key_id: TEXT
 - account_id: TEXT
 - upstream_request_id: TEXT nullable unique
 - model: TEXT
 - mode: TEXT # text, first_frame, first_last_frame, keyframes, references
 - requested_seconds: INTEGER
 - status: TEXT # dispatching, pending, done, failed, expired, refused, rejected, unknown
 - video_url: TEXT nullable # kept only on the row and returned only to the job's key
 - duration_seconds: REAL nullable
 - cost_microdollars: INTEGER nullable
 - error_code: TEXT nullable
 - next_poll_at: INTEGER nullable
 - created_at: INTEGER
 - finished_at: INTEGER nullable

Files And Functions:
 - planned: internal/application/video_generate.go#Generate, #validateGeneration, #selectAccount
 - planned: internal/adapters/httpapi/video.go#RegisterVideoRoutes, #createGeneration
 - existing: internal/adapters/httpapi/server.go#Handler - mount `/video/v1/` with `proxyIngress`
 - existing: internal/adapters/httpapi/firewall.go - add the `/video/v1/` prefix
 - existing: cmd/codex-lb/runtime.go - wire the service, share `BodyBudget` and the operation admission
 - existing: cmd/codex-lb/access_log.go - bounded route group for video

Tests:
internal/adapters/httpapi/video_test.go
 - description: first frame, last frame and prompt reach the stub unchanged with the account token; the client gets 202 with a vid_ id and neither the account nor the upstream id
   requirements: R7, R11
 - description: each invalid body gets 400 with its parameter and creates no job
   requirements: R8
 - description: three concurrent requests over two accounts spread by unfinished jobs; a fifth job on a full single account gets 503 video_no_accounts without an upstream call
   requirements: R9
 - description: a 429 moves the body to the second account, sets the cooldown and keeps the first account out of the next request; a 403 sets access_denied
   requirements: R10
 - description: a 500 and a dropped connection give 502, an unknown job and one upstream call; a 400 gives the client error and no failover
   requirements: R10
 - description: all accounts refusing gives 429 video_pool_exhausted with Retry-After
   requirements: R11

### 5. Read and finish a job
key and job id --read the job's account at xAI--> [job status for the client, 404 for another key] --apply one guarded transition--> finished job

Requirements:

  R12:
`GET /video/v1/generations/{id}` SHALL answer 404 unless the job belongs to the calling key, SHALL query only the job's account, and SHALL answer `{id, status, progress, model, created_at, finished_at, video: {url, duration}, cost_usd, error}`. A finished job SHALL be answered from its row without an upstream call. `GET /video/v1/generations` SHALL list the key's own jobs newest first with a limit of at most 100 and a cursor.

  R13:
A job SHALL leave `pending` by one transition guarded by `WHERE status='pending'`. `done` SHALL store the URL, the duration and the cost as `cost_in_usd_ticks / 10000` microdollars; `failed` and `expired` SHALL store no cost. A valid `done` without a cost SHALL store a null cost, which reports show as unknown.

  R14:
A background task SHALL poll `pending` jobs due by `next_poll_at`, at most 20 per run with at most four concurrent calls, every 10 seconds at first and backing off to one minute, and SHALL be cancelled and joined at shutdown. A `pending` job one hour old, or a `dispatching` job 15 minutes old, SHALL become `unknown`. Rows finished more than 90 days ago SHALL be deleted in bounded batches.

  R15:
Logs and diagnostics MUST NOT contain prompts, image or audio payloads, video URLs or tokens. The returned URL is xAI's and readable by anyone holding it; the API documentation SHALL say so.

Files And Functions:
 - planned: internal/application/video_jobs.go#Status, #List, #Sweep, #finishJob
 - existing: internal/application/background.go#RunPeriodic
 - existing: cmd/codex-lb/runtime.go - add the task to the background group

Tests:
 - description: another key's job id gives 404 and no upstream call; a finished job makes no upstream call
   requirements: R12
 - description: list returns only the key's jobs and pages with a cursor
   requirements: R12
 - description: a concurrent poll and sweep on done apply one transition, and the summary counts the cost once
   requirements: R13
 - description: failed and expired store no cost; done without usage stores a null cost
   requirements: R13
 - description: a job never polled by the client finishes through the sweep; a stale pending and a stale dispatching job become unknown; a partial failure in a batch keeps the other results; shutdown joins the task
   requirements: R14
 - description: a captured log of a full job contains no prompt, image, URL or token
   requirements: R15

### 6. Show the function on its own dashboard page
administrator --open Video generation--> page with pool, allowed keys and recent jobs

Requirements:

  R16:
A navigation entry SHALL open `/video` with three sections: pool accounts with import and actions, allowed keys with add and remove, and recent jobs with key, account, mode, status, seconds and cost, plus totals for today and for 7 and 30 days. Reading the page MUST NOT call xAI. The page SHALL state that the remaining weekly pool is not available through the API and is shown only on xAI's site.

  R17:
Other pages, reports and the overview SHALL NOT change.

Events And Endpoints:
 - GET /api/video/accounts, POST /api/video/accounts/import, PATCH /api/video/accounts/{id}, DELETE /api/video/accounts/{id}
 - GET /api/video/jobs, GET /api/video/summary

Files And Functions:
 - planned: web/src/features/video/ - page, schemas, hooks and tests
 - existing: web/src/App.tsx - route
 - existing: web/src/components/layout/app-header.tsx - `nav.video`
 - existing: web/src/components/layout/shell-status.ts - known paths
 - existing: web/src/i18n/locales/ - strings
 - existing: web/src/test/mocks/handlers.ts - mocks

Tests:
 - description: the page lists accounts, imports a file, adds a key and shows recent jobs and totals; no token field is rendered
   requirements: R16
 - description: contract tests against the built binary cover /api/video/*
   requirements: R16
 - description: existing overview, accounts and reports tests pass unchanged with pool data present
   requirements: R17

## Implementation checklist

1. [x] Solution spec and OpenSpec change `add-grok-video-pool`.
2. [x] Owner's decision on imported CLI credentials: approved.
3. [x] Probe with the owner's sign-in; results recorded under Observed Existing Logic.
4. [x] Migration 44, domain types and store (R1–R3, R6, R9, R13).
5. [x] xAI adapter with bounded responses and token refresh (R4, R5).
6. [x] Pool service: import, allow list, validation, selection, creation with failover (R1, R6–R11).
7. [x] Job status, list, guarded finish and background sweep with retention (R12–R15).
8. [x] REST and admin routes, mount, firewall prefix, access-log group, composition (R1, R3, R6, R7, R12, R16).
9. [x] Dashboard page, navigation, locales, mocks and tests (R16, R17).
10. [x] `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`; sync the delta, archive the change, release notes with REST examples. No production-copy rehearsal: migration 44 only creates empty tables.

Deferred:
- Sign-in from the dashboard: needs a registered OAuth client, and no way to register one was found.
- Video extension and editing endpoints, xAI Files API inputs, and `storage_options` uploads.
- Copying finished videos to codex-lb storage; the client receives xAI's URL.
- Per-key video limits and budgets: the allow list is the only control for now, because the function is for the owner's keys.
- Choosing an account by its remaining pool: the API exposes no such value.

## Open questions

1. Non-blocking: the refusal at an exhausted limit and the pool cost of a job were not observed. The refusal mapping (R10) follows HTTP status and may need adjusting after the first real refusal.
2. Non-blocking: xAI's terms were seen only through a secondary source and bar making an account available to others. The owner's several accounts with the owner's keys fit "only for myself", but using several subscriptions to get around limits was not checked against the current text; the owner accepts the account risk by choosing this design.

## Decision log

- 2026-10-31: The owner chose "only for myself", then several Grok accounts, then a separate pool and function, then the same API key with a separate REST surface. This replaced a first draft that stored Grok accounts as `kind='external'` rows with key reservations.
- 2026-10-31: Separate tables instead of `accounts`. A row in `accounts` would have required exclusions in every selection and broken text routing after a rollback; separate tables make the migration additive and rollback safe.
- 2026-10-31: Separate job accounting instead of the usage ledger. A job outlives its request and has no tokens; one guarded status transition gives exactly-once cost, and video consumes no key limits. The AGENTS.md money invariant covers API-key reservations, and this path takes none.
- 2026-10-31: `/video/v1/*` with a local job id and a normalized response instead of mirroring xAI's `/v1/videos/*`. The surface is visibly separate, and the account and upstream id stay hidden.
- 2026-10-31: Import from `auth.json` instead of a device flow in the dashboard, so no client id is embedded in the product; the id comes from the owner's own file. Pending open question 1.
- 2026-10-31: An allow list of keys instead of groups: it is independent of the ChatGPT pool's group model and denies by default.
- 2026-10-31: The owner approved holding imported CLI tokens and refreshing them with the CLI's client id, and approved the probe. xAI may stop or forbid this; the risk is the owner's.
- 2026-10-31: `grok-imagine-video-1.5-lite` is treated like the classic model for pinned frames and references: xAI's reference page says only `grok-imagine-video-1.5` supports them. Not probed on the subscription.
- 2026-10-31: Import refreshes at once, because the probe showed that a rotated session survives `grok logout`, and logout before any refresh was not tested. After import the CLI's copy no longer matters.
