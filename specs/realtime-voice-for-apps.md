# Voice for third-party apps: the OpenAI Realtime API on the subscription pool

- Last edited with skill pack: `0.2.2`

## Title and scope

Third-party apps reach voice through codex-lb with the standard OpenAI Realtime API: a WebSocket session at `/v1/realtime?model=…` and WebRTC calls at `/v1/realtime/calls`, served by the ChatGPT subscription pool. Browser apps get short-lived keys from `/v1/realtime/client_secrets`, and WebSocket sessions are metered against the API key at realtime list prices.

## Planning anchor

- Entrypoints:
  - existing `internal/adapters/httpapi/codex_operations.go#CodexOperationsHandler.realtimeLive` (`GET /v1/realtime`, today only with `call_id`);
  - existing `internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall`;
  - existing `internal/adapters/provider/operations_realtime.go#Adapter.Realtime` (sideband relay).
- Changed assumption: realtime in codex-lb served only Codex's own shapes, that is call creation on `/backend-api/codex/realtime/calls` and a sideband by call id. The owner deferred the Codex Desktop 409 fix on 2026-11-02 and on 2026-11-03 asked for voice for third-party apps over both WebSocket and WebRTC.
- Evidence: four owner-approved probes on 2026-11-03 from prod with pool account `9f6ee091`'s ChatGPT token, Codex client headers and `ChatGPT-Account-ID`:
  - `wss://api.openai.com/v1/realtime?model=gpt-realtime` returned 101 and `session.created` (type `realtime`, model `gpt-realtime`). A `session.update` with own instructions and `output_modalities: ["text"]` was applied, and a text answer `Pong.` came back with `response.done` usage (17 input and 5 output text tokens, with `input_token_details` and `output_token_details` split into text and audio);
  - `POST https://chatgpt.com/backend-api/codex/realtime/calls` with JSON `{sdp, session}` and a standard session (`model: gpt-realtime`, voice `marin`) returned 201, an SDP answer and `Location: /v1/realtime/calls/rtc_…`. Its legacy sideband opened (101) and closed at once, because the hand-made offer never connected media;
  - the same backend refused raw `application/sdp` and `multipart/form-data` with 400 `Unsupported content type`.
- Non-local: domain pricing, SQLite migration, application operations, provider relay, HTTP routes and authentication, runtime wiring, OpenSpec `go-runtime`, release notes.
- OpenSpec change `add-realtime-voice-for-apps`, archived as `openspec/changes/archive/2026-11-03-add-realtime-voice-for-apps/`: `go-runtime` ADDED «Third-party apps open Realtime WebSocket sessions», «Third-party apps create WebRTC calls» and «Realtime client secrets for browser apps».
- Impacted specs in `specs/`: none describes realtime; `specs/request-body-budget.md` (`reuse`, call bodies keep using the shared budget).

## Connected groups or observed existing logic

### Connected groups

1. Entry and authentication
   - `registerRoutes` maps `GET /v1/realtime`, `GET /v1/realtime/{$}`, `GET /v1/live/{call_id}` and `GET /backend-api/codex/{call_id}` to `realtimeLive`, which requires exactly one call selector. `POST /backend-api/codex/realtime/calls` maps to `createRealtimeCall`.
   - `authenticateBearerKey` accepts one `Authorization: Bearer` value of up to 4096 bytes that hashes to an active, unexpired key. Realtime never admits keyless local callers.
2. Application
   - `CreateRealtimeCall` validates the media type (JSON, SDP or multipart) and size (1 MiB), resolves an owner account with `resolveConversationOwner` and `accountForOwner`, calls `provider.Control` with path `realtime/calls`, and binds the call id from `Location` with `SaveCodexResourceOwner`. All of this runs inside `contentFree`.
   - `contentFree` takes an operation admission slot and records one usage event with zero usage. `billedAdmitted` reserves a budget with `ReserveUsage`, runs the call, prices known usage and settles exactly once, keeping uncertain usage reserved.
3. Provider
   - `Adapter.Realtime` dials `liveBaseURL` (`https://api.openai.com/v1`, `/live/<id>` or `/realtime?call_id=`) with the account's access token, `ChatGPT-Account-ID`, the Codex identity headers and bounded `OpenAI-Alpha`/`OpenAI-Beta`. It relays both directions with a 4 MiB read limit, upstream pings and bounded writes.
4. Persistence
   - Migrations are append-only; schema 48 is current, and additive migrations run while the previous worker still serves.
5. Pricing
   - `pricing.Price` has one input, cached and output rate, with no audio split; transcription already settles a cost computed outside it.
6. Validation
   - HTTP tests use `wireFixtureWithProxy`, loopback stubs and `github.com/coder/websocket` peers.

## Use cases

### 1. Open a Realtime WebSocket session

app WebSocket upgrade without call_id --authenticate the key or client secret--> authorized app --reserve the session budget and pick a subscription account--> reserved session --dial the account's Realtime session--> live session --relay frames and add up response usage--> closed session --settle the summed usage once--> metered session

Requirements:

  R1:
A WebSocket upgrade of `GET /v1/realtime` or `/v1/realtime/` without `call_id` SHALL open a Realtime session for the `model` query value (1 to 128 characters from letters, digits, `.`, `_`, `-`). Other query values SHALL follow the existing realtime bounds. A request without `model` SHALL fail with 400 before the upgrade, and a request with `call_id` SHALL keep the existing sideband behavior.

  R2:
The session SHALL authenticate an active API key or an unexpired client secret of an active key (use case 3), given as `Authorization: Bearer` or, for browsers, as the WebSocket subprotocol `openai-insecure-api-key.<credential>` offered with `realtime`. In that case the server SHALL select `realtime`. Keyless local access MUST NOT apply.

  R3:
Before dialing, the runtime SHALL pick one ChatGPT subscription account eligible for the key, as Realtime call creation does, and reserve on the key a session budget of 20000 input and 10000 output tokens at the model's audio rates. It SHALL then dial `wss://api.openai.com/v1/realtime?model=…` with that account's access token, `ChatGPT-Account-ID`, the Codex client identity and the bounded negotiation headers. The session MUST stay on that account, and model sources MUST NOT serve it.

  R4:
With a client secret that carries a session, the first upstream message SHALL be a `session.update` with that session, sent before any client message.

  R5:
Frames up to 4 MiB SHALL be relayed unchanged in both directions, with the existing pings, bounded writes and cleanup. A bad request, credential, key or the lack of an account SHALL fail with an HTTP error before the client's upgrade. An upstream refusal SHALL close the client socket with status 1011 and the fixed reason `realtime_session_unavailable`, without the upstream body, and SHALL charge nothing.

  R6:
At the end of the session the reservation SHALL be settled exactly once with the sum of the `usage` of every upstream `response.done`: input, cached-input and output tokens, and the cost at the model's realtime rates with text and audio priced apart. A response unfinished at the close is not counted. The session SHALL NOT change account health, and message content, audio and upstream bodies MUST NOT be logged or archived.

Logic Details:
- Codex already reaches the same upstream with the same identity for its sidebands; only the path (`/realtime?model=`) and the absence of a call differ, so the relay pump is shared and gains an upstream observer that reads `response.done` usage and an optional first message.
- A WebSocket session has no request that could be reserved per response (server VAD starts responses by itself), so one reservation covers the session and the settlement takes the actual sum, which may exceed it.

Files And Functions:
 - planned: internal/application/realtime_apps.go#CodexOperations.RealtimeSession - workflow steps "reserve the session budget and pick a subscription account", "settle the summed usage once"
 - existing: internal/adapters/provider/operations_realtime.go#Adapter.Realtime - shares its relay with sessions
 - planned: internal/adapters/provider/operations_realtime.go#Adapter.RealtimeSession - workflow step "dial the account's Realtime session"
 - existing: internal/adapters/httpapi/codex_operations.go#CodexOperationsHandler.realtimeLive - session branch and subprotocol credential
 - existing: internal/application/codex_operations_accounting.go#CodexOperations.billedAdmitted - realtime cost branch

Tests:
internal/adapters/httpapi/realtime_apps_test.go
 - description: an app session reaches the stub with the account token, model and identity headers; frames relay both ways; `response.done` usage settles once at realtime rates
   requirements: R1, R3, R5, R6
 - description: a browser credential in the subprotocol is accepted and `realtime` is selected; no `model`, no key or another key's secret fail before the upgrade
   requirements: R1, R2
 - description: a client secret's session reaches the stub as the first message; an upstream 403 refusal releases the reservation without charge
   requirements: R4, R5

### 2. Create a WebRTC call for an app

app SDP offer --authenticate the key or client secret--> authorized offer --build the backend's JSON call--> backend call request --create the call and bind its owner--> SDP answer and call location

Requirements:

  R7:
`POST /v1/realtime/calls` and `/v1/realtime/calls/` SHALL accept `application/sdp`, `multipart/form-data` with an `sdp` part and an optional `session` part, and `application/json` `{sdp, session}`, up to the existing 1 MiB. Other media types SHALL be refused with 415 before dispatch. The session SHALL be taken from the request, else from the client secret, else `{type: "realtime", model: <query model or gpt-realtime>}`. It SHALL be sent to the subscription backend as JSON `{sdp, session}`, because the backend accepts nothing else.

  R8:
The client SHALL receive the SDP answer as `application/sdp` and `Location: /v1/realtime/calls/<call id>` once the key-scoped owner is bound. The call SHALL be reachable through the existing `/v1/realtime?call_id=` sideband and recorded content-free, since media bypasses codex-lb.

Files And Functions:
 - planned: internal/application/realtime_apps.go#CodexOperations.CreateAppRealtimeCall - workflow step "build the backend's JSON call"
 - existing: internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall - shares owner binding
 - existing: internal/adapters/httpapi/codex_operations.go - routes

Tests:
internal/adapters/httpapi/realtime_apps_test.go
 - description: raw SDP, multipart and JSON offers reach the stub as JSON with the right session; the client gets the SDP answer, the location and a sideband that only its key can open
   requirements: R7, R8
 - description: other media types are refused with 415 and nothing is dispatched
   requirements: R7

### 3. Issue client secrets for browser apps

server request with an API key --check the expiry and session--> valid request --store the secret's hash with its key, session and expiry--> client secret answer

Requirements:

  R9:
`POST /v1/realtime/client_secrets` with an active API key and an optional JSON body of up to 64 KiB, `{expires_after: {anchor: "created_at", seconds: 10..7200}, session: {...}}`, SHALL return `{value: "ek_<random>", expires_at, session}`. The default lifetime is 600 seconds. The runtime SHALL store only the secret's SHA-256, the key id, the encrypted session and the expiry, and SHALL delete expired secrets.

  R10:
A client secret SHALL be accepted only by the session and call routes of use cases 1 and 2, only before it expires and only while its key is active. It SHALL count as its key for authorization, scopes and metering. Any other route, a secret used to mint secrets, or an expired or unknown secret SHALL get 401.

Tables:
realtime_client_secrets
 - secret_hash: TEXT PRIMARY KEY # SHA-256 hex of the secret
 - api_key_id: TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE
 - session_encrypted: BLOB # vault ciphertext of the session JSON, NULL without one
 - expires_at: INTEGER NOT NULL # Unix milliseconds
 - created_at: INTEGER NOT NULL

Logic Details:
- Migration 49 only adds this table, which older code never reads, so it is additive. Storing secrets in SQLite keeps them valid across restarts and in both workers during a seamless update; the secret stays short, under the 4096-byte bearer bound.

Files And Functions:
 - planned: internal/adapters/sqlite/realtime_secrets.go#Store.SaveRealtimeClientSecret
 - planned: internal/adapters/sqlite/realtime_secrets.go#Store.RealtimeClientSecret
 - planned: internal/application/realtime_apps.go#CodexOperations.IssueRealtimeClientSecret - workflow step "store the secret's hash with its key, session and expiry"

Tests:
internal/adapters/httpapi/realtime_apps_test.go
 - description: a secret is issued with its expiry, opens a session and a call for its key, and is refused after expiry, after its key is disabled, on Responses and on the secret route
   requirements: R9, R10
internal/adapters/sqlite/migrations_test.go
 - description: every earlier schema upgrades to 49 and migration 49 is additive
   requirements: R9

### 4. Price realtime usage

response.done usage --split text, audio and cached tokens--> priced usage

Requirements:

  R11:
Realtime cost SHALL be computed per million tokens from list prices. The `gpt-realtime` family (and any other realtime model) takes text input $4, cached input $0.40, text output $16, audio input $32 and audio output $64. Models starting with `gpt-realtime-mini` take text input $0.60, cached input $0.06, text output $2.40, audio input $10, cached audio $0.30 and audio output $20. Cached tokens are charged at the cached rate and the rest of each kind at its own rate. Image input is charged at the text input rate.

Data:
realtime_rates:
| model | text in | cached text | text out | audio in | cached audio | audio out |
|---|---|---|---|---|---|---|
| gpt-realtime* | 4.00 | 0.40 | 16.00 | 32.00 | 0.40 | 64.00 |
| gpt-realtime-mini* | 0.60 | 0.06 | 2.40 | 10.00 | 0.30 | 20.00 |

Files And Functions:
 - planned: internal/domain/realtime.go#RealtimeCost

Tests:
internal/domain/realtime_test.go
 - description: text, audio and cached tokens are priced apart for both families; malformed counts are refused
   requirements: R11

## Implementation checklist

1. [x] Write this spec and the OpenSpec change `add-realtime-voice-for-apps`; validate both.
2. [x] Domain realtime usage and prices with tests (R11).
3. [x] Migration 49 and the client secret store, additive, with migration tests (R9).
4. [x] Provider: share the relay, add the session dial with a first message and a usage observer (R3–R6).
5. [x] Application: sessions with reservation and settlement, app calls with JSON conversion, client secrets (R1–R10). Docstrings name the workflow steps.
6. [x] HTTP: routes, subprotocol and client-secret authentication, wiring (R1, R2, R7–R10).
7. [x] End-to-end tests against loopback stubs (R1–R10).
8. [x] Gates, release notes `go-v1.12.0`, context notes, sync and archive the OpenSpec change.
9. [ ] Publish go-v1.12.0 and stop before the rollout; after the owner's update, check one app session with approval.

Deferred:
- Metering WebRTC media, which flows directly between the app and OpenAI; codex-lb would have to keep its own sideband open for every call.
- Server-side tools and SIP endpoints (`accept`, `refer`, `hangup`) of the Realtime API.
- Fixing Codex Desktop's own voice, which creates its call outside codex-lb (see the Desktop 409 investigation).

## Open questions

- non-blocking: Whether realtime use draws on the Codex usage limits of the account was not measured; sessions do not change account health.
- non-blocking: A live WebRTC call with real media through `/v1/realtime/calls` was not run; the backend call shape and the sideband were verified separately.
- non-blocking: Prices for models newer than `gpt-realtime` and `gpt-realtime-mini` are assumed equal to their family.

## Decision log

- 2026-11-03: Both protocols, as the owner chose. WebSocket sessions go to `api.openai.com/v1/realtime?model=` with the subscription token, and calls go to the ChatGPT backend as JSON. Both shapes were proven by probes.
- 2026-11-03: WebSocket sessions are metered with one reservation per session and one settlement of the summed `response.done` usage, so keys with limits pay for voice. WebRTC calls stay content-free because their media bypasses codex-lb.
- 2026-11-03: Client secrets live in SQLite (additive migration 49) rather than in memory or in a sealed token. This keeps them short, survives restarts and works in both workers during an update.
- 2026-11-03: Sessions are account-neutral, as they are for Codex's own realtime, because voice limits are not known to match Codex limits.
- 2026-11-03: Validation: realtime pricing and secret store tests, and end-to-end tests against a loopback Realtime. Those cover the relay with settlement at realtime rates, refusals before the upgrade, a refused dial closing with a fixed reason and no charge, browser secrets through the subprotocol and calls, and offer conversion. All local gates are green. A mutation that ignores `response.done` fails `TestRealtimeAppSessionRelaysAndSettlesOnce`. The first gate run caught a regression of the relay split, which reported a credential failure as `realtime_live_unavailable`; it was fixed before release.
