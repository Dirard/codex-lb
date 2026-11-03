# GPT-Live for third-party apps on the subscription pool

- Last edited with skill pack: `0.2.2`

## Title and scope

Third-party apps reach GPT-Live, OpenAI's full-duplex voice model, through codex-lb: an app posts a WebRTC offer to `/v1/live/calls`, codex-lb creates the call on a ChatGPT subscription account the way Codex does, and the app's peer talks to the model directly. codex-lb meters each call on the API key through its own read-only sideband. Browser pages use short-lived client secrets.

## Planning anchor

- Entrypoints:
  - existing `internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall` (owner binding of subscription calls);
  - existing `internal/application/realtime_apps.go#CodexOperations.CreateAppRealtimeCall` (app offers, client secrets);
  - existing `internal/adapters/provider/operations_realtime.go#Adapter.Realtime` (sideband relay to `wss://api.openai.com/v1/live/<id>`).
- Changed assumption: go-v1.12 serves GPT-Realtime, a turn-based model with barge-in. On 2026-11-03 the owner asked for GPT-Live instead («мне не нужен realtime, это прошлый век, мне нужен именно live»), the model behind ChatGPT Voice and Codex voice that listens and speaks at once.
- Evidence (research notes in `.local/research/`):
  - The public GPT-Live API (`gpt-live-1`, `/v1/live/sessions`, GA events, no client secrets) is documented for API projects.
  - Codex reaches GPT-Live with a ChatGPT login only through WebRTC calls: `POST https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas` with `OpenAI-Alpha: quicksilver=v2`, JSON `{sdp, session}`, model `gpt-live-1-codex` (Desktop: `gpt-live-1.1-codex`), delegation `client`, sideband `wss://api.openai.com/v1/live/<id>`. Events are the alpha dialect (`delegation.created`, `delegation.context.append`, `turn.*`, `*_transcript.added`).
  - Eight owner-approved probes on 2026-11-03 from prod, pool account `9f6ee091`:
    - GA WebSocket `/v1/live/sessions` (`gpt-live-1` and `gpt-live-1.1-codex`) and alpha WebSocket `/v1/live?model=` opened, then answered `error forbidden "Voice session access denied."`;
    - a WebRTC call on the ChatGPT backend with `gpt-live-1.1-codex` and real synthetic speech connected media. The model delegated the request (`delegation.created` with the user's words), accepted `delegation.context.append` sent on the **data channel** and spoke the answer;
    - with `gpt-live-1-codex`, two sidebands attached at once, closing one left the call running, and `session.close` on the data channel ended it;
    - usage arrives as `session.usage.updated {usage:{audio_duration_ms, backend_model_usage:[]}, usage_limit}` about every 15 s and finally in `session.closed {reason, usage}`; `session.started.session.expires_at` is creation + 2 h.
- Non-local: domain pricing, application call creation and metering, provider observer, HTTP routes and CORS, runtime lifecycle, repricing exclusions, OpenSpec `go-runtime`, release notes.
- OpenSpec change `add-gpt-live-for-apps`, archived as `openspec/changes/archive/2026-11-03-add-gpt-live-for-apps/`: `go-runtime` ADDED «Third-party apps create GPT-Live calls» and «GPT-Live calls are metered through an observer sideband», MODIFIED «Realtime protocol negotiation preserves native client variants» and «Realtime client secrets for browser apps».
- Impacted specs in `specs/`: `specs/realtime-voice-for-apps.md` (`reuse`: offers, client secrets and CORS are shared; its WebSocket sessions and calls stay as they are).

## Connected groups or observed existing logic

### Connected groups

1. Entry and authentication
   - `CodexOperationsHandler.realtimeCredential` accepts a Bearer API key or an `ek_` client secret; secrets work only on routes that call it.
   - `POST /v1/realtime/calls` is exempt from cross-origin protection (`server.go`) and answers CORS; `realtimeCallPreflight` serves `OPTIONS`.
   - `GET /v1/live/{call_id}` is the key-scoped sideband (`realtimeLive` → `AuthorizeRealtime` → `Adapter.Realtime`), forwarding the client's bounded `OpenAI-Alpha`/`OpenAI-Beta`.
2. Application
   - `CreateRealtimeCall` resolves the owner account, sends `provider.Control` (`POST realtime/calls` with the request query and negotiation headers), binds the key-scoped owner row from `Location` and records a content-free `realtime_call` event.
   - `realtimeOffer` reads raw SDP, multipart and JSON offers. `RealtimeSession` shows the metered pattern: `ReserveUsage` before dispatch, one `settle` after, account-neutral.
   - Reservations of a running worker are never reconciled; those of a gone worker are marked and released without charge after an hour (`ReleaseUnreconciledReservations`).
3. Provider
   - `dialRealtime` opens an upstream socket with the account token, `ChatGPT-Account-ID`, the Codex identity and negotiation headers; `probeRealtimeUpstream` pings a silent upstream.
4. Runtime
   - Background loops run on `backgroundCtx`; `runtime.close` stops them, joins them and then closes storage. A retiring worker keeps serving accepted requests up to 30 minutes and then drains.
5. Pricing
   - `priceUsageEventTx` keeps a token-free cost; repricing (`model_pricing_reprice.go`, `model_pricing_folded.go`) and settlement pricing skip `transcription`, `reconciled` and `realtime_session` events by kind.

## Use cases

### 1. Create a GPT-Live call for an app

app offer --authenticate the key or client secret--> authorized offer --complete the Live session--> backend Live call request --reserve the call budget on a subscription account--> reserved call --create the call with the quicksilver alpha and bind its owner--> SDP answer and Live location

Requirements:

  R1:
`POST /v1/live/calls` and `/v1/live/calls/` SHALL accept `application/sdp`, `multipart/form-data` with an `sdp` part and an optional `session` part, and `application/json` `{sdp, session}`, up to 1 MiB, with an API key or a client secret. Other media types SHALL be refused with 415 and bad bodies with 400, before any reservation or dispatch.

  R2:
The session SHALL be taken from the request, else from the client secret, else `{}`. Missing fields SHALL be completed: `model` from the `model` query value or `gpt-live-1.1-codex`, and `delegation` as `{"type": "client"}`. Present fields SHALL pass unchanged. A model outside 1 to 128 letters, digits, `.`, `_` and `-` SHALL be refused with 400.

  R3:
Before dispatch the runtime SHALL pick one ChatGPT subscription account eligible for the key, as Realtime call creation does, and reserve on the key a call budget of 10 minutes at the Live price, with one input and one output token so token limits hold the call too. The call SHALL then be created on that account with `POST …/codex/realtime/calls?intent=quicksilver&architecture=avas`, `OpenAI-Alpha: quicksilver=v2` and JSON `{sdp, session}`, and its key-scoped owner bound as for other Realtime calls. Model sources MUST NOT serve it.

  R4:
The client SHALL receive the upstream status with the SDP answer as `application/sdp`, `Location: /v1/live/<call id>` and the CORS headers of app calls. A refused or failed creation SHALL release the reservation without charge and expose only a fixed failure code.

Logic Details:
- The subscription refuses WebSocket-only Live sessions, so WebRTC calls are the only GPT-Live transport; the model's events travel on the app's data channel `oai-events`, which also accepts the app's control events.
- `Location` names the existing sideband path, so a server app can attach with the same key.

Files And Functions:
 - planned: internal/application/live_calls.go#CodexOperations.CreateAppLiveCall - workflow steps "complete the Live session", "reserve the call budget on a subscription account", "create the call with the quicksilver alpha and bind its owner"
 - existing: internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall - shares owner binding through an extracted dispatch
 - existing: internal/application/realtime_apps.go#realtimeOffer - shared offer parsing
 - planned: internal/adapters/httpapi/realtime_apps.go#CodexOperationsHandler.createAppLiveCall
 - existing: internal/adapters/httpapi/codex_operations.go#CodexOperationsHandler.RegisterRoutes - routes

Tests:
internal/adapters/httpapi/live_calls_test.go
 - description: a raw offer with a client secret from another origin reaches the backend as JSON with the alpha, the query and the completed session; the page reads the SDP answer and `Location: /v1/live/<id>`
   requirements: R1, R2, R3, R4
 - description: a JSON offer keeps its own model and delegation; a bad model, a bad body and another media type are refused before any reservation or dispatch
   requirements: R1, R2
 - description: an upstream refusal releases the reservation without charge and exposes a fixed code
   requirements: R3, R4

### 2. Meter a GPT-Live call

created call --attach a read-only sideband on the creating account--> observed call --track the cumulative audio duration--> ended call --settle the reservation once at the Live price--> metered call

Requirements:

  R5:
After creation the runtime SHALL attach its own read-only sideband to `wss://api.openai.com/v1/live/<id>` on the creating account with `OpenAI-Alpha: quicksilver=v2`. It SHALL send no client events. While the call has never been attached, a 404 or 410 SHALL be retried for up to 30 seconds. A dropped sideband SHALL be reattached with a backoff from 200 ms to 5 s, and observation SHALL stop at `session.closed`, at a 404 or 410 after attachment, at `session.started.session.expires_at` plus 5 minutes (else creation plus 2 hours 5 minutes), or when the worker stops.

  R6:
The runtime SHALL keep the largest `usage.audio_duration_ms` from `session.usage.updated` and `session.closed` and, when observation ends, settle the reservation exactly once with a `live_session` usage event priced at $0.05 per minute of that duration, rounded to the nearest microdollar. The event SHALL be a success after `session.closed`; `cancelled` with code `live_usage_incomplete` when the worker stops first; an error with code `live_usage_incomplete` when the call ends otherwise; and an error with code `live_call_unobserved` and nothing charged when the sideband never attached. Known usage SHALL be charged in every case.

  R7:
Each worker SHALL observe at most as many calls as its upstream WebSocket capacity. A call beyond that SHALL be refused with 503 `live_calls_busy` before any reservation or dispatch. Observation SHALL NOT hold an operation admission slot, change account health or log content, audio or upstream bodies.

  R8:
Stopping the runtime SHALL cancel every observer, settle each one and join them before storage closes. Observers SHALL NOT delay a seamless update: a call that outlives its worker is charged up to the last known usage. A worker that exits without settling leaves its reservations to the existing reconciliation.

  R9:
`live_session` events SHALL keep their recorded cost through settlement pricing and repricing.

Logic Details:
- Media flows between the app and OpenAI, so the sideband is the only place codex-lb can see usage. Probes showed a second sideband coexists with the app's own and that closing one leaves the call running.
- The sideband also carries audio reflections (about 26 KB/s); only frames that mention a usage, close or start event are decoded.
- `usage_limit` (`status: approaching`, `reset_seconds`) is ignored for now.
- The ledger refuses a reservation without a positive part for each token limit of the key, so the budget carries one input and one output token; settlement returns them, since Live usage has no tokens. Transcription reserves the same way.
- R9 needs no new pricing code: settlement pricing and repricing never recompute a cost recorded without tokens (`priceUsageEventTx`, `applyPriceEventTx`), and `live_session` events carry no tokens. An explicit `live_session` exclusion was written and removed, because a mutation showed it changed nothing.

Data:
live_price:
| model | price |
|---|---|
| gpt-live-* | $0.05 per minute of `audio_duration_ms` |

Files And Functions:
 - planned: internal/domain/live.go#LiveUsage - Live price
 - planned: internal/domain/live.go#LiveCallBudget
 - planned: internal/application/live_calls.go#CodexOperations.observeLiveCall - workflow steps "attach a read-only sideband on the creating account", "track the cumulative audio duration", "settle the reservation once at the Live price"
 - planned: internal/application/live_calls.go#CodexOperations.ConfigureLiveCalls
 - planned: internal/application/live_calls.go#CodexOperations.CloseLiveCalls
 - planned: internal/adapters/provider/operations_live.go#Adapter.ObserveLiveCall
 - existing: cmd/codex-lb/runtime.go#runtime.close - closes observers before storage
 - existing: internal/adapters/sqlite/model_pricing.go#priceUsageEventTx - keeps a token-free cost
 - existing: internal/adapters/sqlite/model_pricing_reprice.go#applyPriceEventTx - skips a token-free cost

Tests:
internal/domain/live_test.go
 - description: duration pricing rounds to the nearest microdollar and refuses negative durations; the budget is 10 minutes
   requirements: R6
internal/adapters/httpapi/live_calls_test.go
 - description: the observer attaches with the alpha after an early 404, ignores audio frames and settles once at the Live price after `session.closed`
   requirements: R5, R6
 - description: a dropped sideband is reattached and the final usage is charged; closing the runtime charges the last known usage as `cancelled`
   requirements: R5, R6, R8
 - description: a sideband that never attaches settles `live_call_unobserved` with nothing charged
   requirements: R5, R6
 - description: with every observer slot taken, a new call is refused with 503 before any reservation or dispatch
   requirements: R7
internal/adapters/sqlite/model_pricing_limits_test.go
 - description: repricing leaves `live_session` and `realtime_session` costs unchanged
   requirements: R9

### 3. Serve browser apps

browser request from another origin --answer the CORS preflight of a Live call--> allowed offer --accept a client secret minted on either secret route--> served browser call

Requirements:

  R10:
`POST /v1/live/calls` and `/v1/live/calls/` SHALL be exempt from cross-origin request protection, and their answers, refusals included, SHALL carry `Access-Control-Allow-Origin: *` and `Access-Control-Expose-Headers: Location`. `OPTIONS` on them SHALL answer the same preflight as app Realtime calls.

  R11:
`POST /v1/live/client_secrets` and `/v1/live/client_secrets/` SHALL issue client secrets exactly as `/v1/realtime/client_secrets` does and keep cross-origin protection. A secret from either route SHALL be accepted by the Realtime session and call routes and by the Live call route.

Files And Functions:
 - existing: internal/adapters/httpapi/server.go#Server.Handler - cross-origin bypass for Live calls
 - existing: internal/adapters/httpapi/codex_operations.go#CodexOperationsHandler.RegisterRoutes - preflight and secret alias

Tests:
internal/adapters/httpapi/live_calls_test.go
 - description: through the full server handler a foreign page passes the Live call preflight and creates a call, while `/v1/live/client_secrets` stays refused for it
   requirements: R10, R11

### 4. Attach an app's own sideband

app sideband upgrade on /v1/live/<id> --authorize the key-scoped owner--> owned call --complete the frameless alpha--> app sideband relay

Requirements:

  R12:
A sideband on `/v1/live/<id>` or `/backend-api/codex/<id>` without `OpenAI-Alpha` SHALL reach the upstream with `OpenAI-Alpha: quicksilver=v2`; a client value SHALL pass unchanged.

Files And Functions:
 - existing: internal/adapters/provider/operations_realtime.go#Adapter.Realtime

Tests:
internal/adapters/httpapi/live_calls_test.go
 - description: an app sideband without the alpha reaches the stub with `quicksilver=v2` and relays frames
   requirements: R12

## Implementation checklist

1. [x] Write this spec and the OpenSpec change `add-gpt-live-for-apps`; validate both.
2. [x] Domain Live price and budget with tests (R6).
3. [x] Provider: `ObserveLiveCall` and the default alpha on v3 sidebands (R5, R12).
4. [x] Application: extract call dispatch from `CreateRealtimeCall`; `CreateAppLiveCall` with session completion, reservation and release; observers with attach, reattach, settlement, capacity and close (R1–R8). Docstrings name the workflow steps.
5. [x] SQLite: a repricing test for `live_session` costs (R9).
6. [x] HTTP: routes, preflight, secret alias, cross-origin bypass; runtime wiring and close (R1, R4, R7, R8, R10, R11).
7. [x] End-to-end tests against loopback stubs (R1–R12).
8. [x] Gates, release notes `go-v1.13.0`, context notes, sync and archive the OpenSpec change.
9. [ ] Publish go-v1.13.0 and stop before the rollout; after the owner's update, check one browser-style call with approval.

Deferred:
- GA-compatible `/v1/live/sessions`: the subscription refuses it, and the app's data channel speaks the alpha dialect, which codex-lb cannot translate because media and events bypass it.
- WebSocket-only Live sessions for server apps: refused for ChatGPT tokens.
- Using `usage_limit` (`approaching`, `reset_seconds`) for account selection or warnings.
- Resuming observers in the replacement worker after a seamless update.
- Fixing Codex Desktop's own voice (409 on its sideband).

## Open questions

- non-blocking: How Live calls count against the subscription's voice limits was not measured; `usage_limit` stayed `null` in the probes.
- non-blocking: The $0.05 per minute rate is the public `gpt-live-1` list price, used for key budgets only; the subscription itself is not billed per minute.

## Decision log

- 2026-11-03: GPT-Live through WebRTC calls on the ChatGPT backend, the only transport the subscription serves to a ChatGPT token. Proven by the eight probes listed in the planning anchor.
- 2026-11-03: A separate route `/v1/live/calls` instead of model detection on `/v1/realtime/calls`: the session shape, the upstream alpha and the metering differ, and the GA Live API shape cannot be served.
- 2026-11-03: Calls are metered through codex-lb's own sideband, priced at the public $0.05 per minute, so key limits apply to voice. Observers live outside request lifetimes, are bounded by the upstream WebSocket capacity and settle once when they end. They never block an update; the part of a call after its worker stops is not charged.
- 2026-11-03: Default model `gpt-live-1.1-codex`, the one Codex Desktop uses; `gpt-live-1-codex` also works.
- 2026-11-03: The ledger refused the first reservation: a key with token limits needs a positive token part. The call budget carries one input and one output token, which settlement returns, as transcription does.
- 2026-11-03: Validation: domain pricing tests, a repricing test, and end-to-end tests against a loopback GPT-Live that cover creation from a foreign page with a secret, offer checks, refusal release, early 404 retry, reattachment, close with known usage, an unobserved call, observer capacity and the app sideband alpha. Twelve mutations (default alpha, attach retry, release on refusal, reattachment, join on close, capacity, usage tracking, unobserved code, session completion, quicksilver query, CORS bypass, budget tokens) each fail their test. A thirteenth showed the planned `live_session` repricing exclusion was redundant with the token-free rule, so it was removed.
- 2026-11-03: All twelve local gates green on the branch: build and contract tests, web checks, vet, lint, `go test`, four race shards, govulncheck and spec validation.
