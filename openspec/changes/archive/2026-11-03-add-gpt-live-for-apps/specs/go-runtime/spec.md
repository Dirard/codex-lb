## ADDED Requirements

### Requirement: Third-party apps create GPT-Live calls

`POST /v1/live/calls` and `/v1/live/calls/` SHALL accept `application/sdp`, `multipart/form-data` with an `sdp` part and an optional `session` part, and `application/json` `{sdp, session}`, up to 1 MiB, with an active API key or a client secret, and SHALL refuse other media types with 415 and bad bodies with 400 before any reservation or dispatch. The session SHALL be taken from the request, else from the client secret, else `{}`; a missing `model` SHALL become the `model` query value or `gpt-live-1.1-codex`, a missing `delegation` SHALL become `{"type": "client"}`, and present fields SHALL pass unchanged. A model outside 1 to 128 letters, digits, `.`, `_` and `-` SHALL be refused with 400.

Before dispatch the runtime SHALL pick one ChatGPT subscription account eligible for the key, as Realtime call creation does, and SHALL reserve on the key a call budget of 10 minutes at the Live price, with one input and one output token so token limits hold the call. It SHALL then create the call on that account with `POST …/codex/realtime/calls?intent=quicksilver&architecture=avas`, `OpenAI-Alpha: quicksilver=v2` and JSON `{sdp, session}`, and bind the key-scoped owner as for other Realtime calls. Model sources MUST NOT serve it. The client SHALL receive the upstream status with the SDP answer as `application/sdp` and `Location: /v1/live/<call id>`. A refused or failed creation SHALL release the reservation without charge and expose only a fixed failure code.

These routes SHALL be exempt from cross-origin request protection, and their answers, refusals included, SHALL carry `Access-Control-Allow-Origin: *` and `Access-Control-Expose-Headers: Location`. `OPTIONS` on them SHALL answer a CORS preflight with 204, `POST` as the allowed method, `Authorization, Content-Type` as the allowed headers and a 600-second max age, without authentication.

#### Scenario: A browser page starts a Live call
- **WHEN** a page on another origin posts `application/sdp` to `/v1/live/calls` with `Authorization: Bearer ek_…`
- **THEN** the subscription backend receives JSON with that SDP and the secret's session completed with the default model and client delegation, together with the quicksilver query and alpha
- **AND** the page reads the SDP answer and `Location: /v1/live/<call id>`

#### Scenario: The backend refuses the call
- **WHEN** the subscription backend refuses the call
- **THEN** the client receives a fixed failure code and the key's reservation is released without charge

### Requirement: GPT-Live calls are metered through an observer sideband

After creating a Live call the runtime SHALL attach its own read-only sideband to `wss://api.openai.com/v1/live/<id>` on the creating account with `OpenAI-Alpha: quicksilver=v2` and SHALL send no client events. Until the first attachment a 404 or 410 SHALL be retried for up to 30 seconds; a dropped sideband SHALL be reattached with a backoff from 200 ms to 5 s. Observation SHALL end at `session.closed`, at a 404 or 410 after attachment, at `session.started.session.expires_at` plus 5 minutes (else creation plus 2 hours 5 minutes), or when the worker stops.

The runtime SHALL keep the largest `usage.audio_duration_ms` from `session.usage.updated` and `session.closed` and, when observation ends, SHALL settle the reservation exactly once with a `live_session` usage event priced at $0.05 per minute of that duration, rounded to the nearest microdollar. The event SHALL be a success after `session.closed`, `cancelled` with code `live_usage_incomplete` when the worker stops first, an error with code `live_usage_incomplete` when the call ends otherwise, and an error with code `live_call_unobserved` with nothing charged when the sideband never attached. Known usage SHALL be charged in every case, and `live_session` costs SHALL survive settlement pricing and repricing.

Each worker SHALL observe at most as many calls as its upstream WebSocket capacity, and a call beyond that SHALL be refused with 503 `live_calls_busy` before any reservation or dispatch. Observers SHALL NOT hold an operation admission slot, change account health, or log content, audio or upstream bodies. Stopping the runtime SHALL cancel, settle and join every observer before storage closes; observers SHALL NOT delay a seamless update.

#### Scenario: A call ends normally
- **WHEN** the call reports `session.closed` with 60000 ms of audio
- **THEN** the key is charged once 50000 microdollars as a successful `live_session`

#### Scenario: The worker stops during a call
- **WHEN** the runtime stops while a call is observed
- **THEN** the observer settles the last known duration as `cancelled` with code `live_usage_incomplete` before storage closes

#### Scenario: The call never connects
- **WHEN** the sideband is refused with 404 for the whole attach window
- **THEN** the reservation settles with code `live_call_unobserved` and nothing is charged

## MODIFIED Requirements

### Requirement: Realtime protocol negotiation preserves native client variants

Realtime call creation and sideband requests SHALL forward bounded `OpenAI-Alpha` and `OpenAI-Beta` negotiation values without accepting arbitrary client headers or replacing provider-owned authentication/account identity. Responses-only Beta tokens (`responses=experimental` and `responses_websockets` declarations) MUST NOT enter Realtime upstream requests. Invalid or oversized negotiation headers SHALL be rejected before provider dispatch or WebSocket upgrade.

A v3 attachment on `/v1/live/<id>` or `/backend-api/codex/<id>` without `OpenAI-Alpha` SHALL reach the upstream with `OpenAI-Alpha: quicksilver=v2`, the frameless protocol of that path; a client value SHALL pass unchanged.

The runtime SHALL support `/v1/realtime?call_id=<id>` and its trailing-slash equivalent for v1/v2 sidebands, forwarding to the upstream query-based `/realtime` endpoint. Exactly one valid call ID is required and SHALL retain the same active Bearer key/account-generation owner checks as v3 `/live/<id>` and backend aliases. V3 path-based requests MUST reject query-supplied call IDs. All variants SHALL retain bounded frames, private failures, cancellation cleanup and sanitized accounting.

#### Scenario: Native Codex selects frameless Realtime
- **WHEN** call creation or v3 attachment carries `OpenAI-Alpha: quicksilver=v2`
- **THEN** the subscription transport preserves that value with the selected account's own authentication

#### Scenario: A v1/v2 client attaches by query
- **WHEN** an authenticated client supplies one owned `call_id` on the legacy route
- **THEN** the upstream request uses `/realtime` with that single call ID and other bounded query values, not the v3 live path

#### Scenario: A caller mixes ownership selectors
- **WHEN** a legacy route has zero/multiple call-ID values or a path-based route also supplies a call-ID query value
- **THEN** the request fails before WebSocket upgrade and upstream dispatch without changing ownership

#### Scenario: An app attaches without the alpha
- **WHEN** an app's v3 sideband carries no `OpenAI-Alpha`
- **THEN** the upstream attachment carries `OpenAI-Alpha: quicksilver=v2`

### Requirement: Realtime client secrets for browser apps

`POST /v1/realtime/client_secrets` and `POST /v1/live/client_secrets` (each with its single trailing-slash equivalent), with an active API key and an optional JSON body of up to 64 KiB, `{expires_after: {anchor: "created_at", seconds: 10..7200}, session: {...}}`, SHALL return `{value: "ek_<random>", expires_at, session}`. The default lifetime SHALL be 600 seconds. The runtime SHALL store only the secret's SHA-256, the key id, the encrypted session and the expiry, and SHALL delete expired secrets. A secret from either route SHALL be accepted only by the app Realtime session and call routes and by the GPT-Live call route, only before it expires and only while its key is active. It SHALL count as its key for authorization, scopes and metering. Any other route, a secret presented to mint secrets, and an expired or unknown secret SHALL receive 401. Migration 49 adds the table and is additive.

#### Scenario: A server mints a secret for its browser
- **WHEN** a server posts a session and a 300-second lifetime with its API key
- **THEN** it receives an `ek_` value that opens sessions and calls for that key for 300 seconds

#### Scenario: A secret is used elsewhere
- **WHEN** a client secret is presented to `/v1/responses` or to mint another secret
- **THEN** the request receives 401

#### Scenario: A browser creates a Live call with a secret
- **WHEN** a server mints a secret on `/v1/live/client_secrets` and its page posts an offer to `/v1/live/calls` with that secret
- **THEN** the call is created for the secret's key and the secret's session is used when the offer has none
