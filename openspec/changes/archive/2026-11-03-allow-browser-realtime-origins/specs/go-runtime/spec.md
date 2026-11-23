## MODIFIED Requirements

### Requirement: Third-party apps open Realtime WebSocket sessions

A WebSocket upgrade of `GET /v1/realtime` or `/v1/realtime/` without `call_id` SHALL open a Realtime session for the `model` query value (1 to 128 characters from letters, digits, `.`, `_`, `-`), and other query values SHALL follow the existing realtime bounds. A request without `model` SHALL fail with 400 before the upgrade, and a request with `call_id` SHALL keep the existing sideband behavior.

The session SHALL authenticate an active API key or an unexpired client secret of an active key. The credential comes as `Authorization: Bearer` or, for browsers, as the WebSocket subprotocol `openai-insecure-api-key.<credential>` offered with `realtime`, in which case the server SHALL select `realtime`. Keyless local access MUST NOT apply.

Before dialing, the runtime SHALL pick one ChatGPT subscription account eligible for the key, as Realtime call creation does, and SHALL reserve on the key a session budget of 20000 input and 10000 output tokens at the model's audio rates. It SHALL then dial `wss://api.openai.com/v1/realtime?model=…` with that account's access token, `ChatGPT-Account-ID`, the Codex client identity and the bounded negotiation headers. The session MUST stay on that account, and model sources MUST NOT serve it. With a client secret that carries a session, the first upstream message SHALL be a `session.update` with that session, sent before any client message.

Frames up to 4 MiB SHALL be relayed unchanged in both directions with the existing pings, bounded writes and cleanup. A bad request, credential, key or the lack of an account SHALL fail with an HTTP error before the client's upgrade. An upstream refusal SHALL close the client socket with status 1011 and the fixed reason `realtime_session_unavailable`, without the upstream body, and SHALL charge nothing. At the end the reservation SHALL be settled exactly once with the summed `usage` of every upstream `response.done`: input, cached-input and output tokens, and the cost at the model's realtime rates, which price text and audio apart. A response unfinished at the close is not counted. The session SHALL NOT change account health. Message content, audio and upstream bodies MUST NOT be logged or archived.

Realtime cost SHALL be computed per million tokens from list prices. The `gpt-realtime` family, and any other realtime model, takes text input $4, cached input $0.40, text output $16, audio input $32 and audio output $64. Models starting with `gpt-realtime-mini` take text input $0.60, cached text $0.06, text output $2.40, audio input $10, cached audio $0.30 and audio output $20. Cached tokens are charged at the cached rate and the rest of each kind at its own rate. Image input is charged at the text input rate.


The app session route SHALL accept a WebSocket upgrade from any `Origin`, because its credential is always explicit and never a cookie. The call sideband and Codex realtime routes SHALL keep accepting only their own origin.
#### Scenario: A server app talks to gpt-realtime
- **WHEN** an app with an active key upgrades `/v1/realtime?model=gpt-realtime` and exchanges events
- **THEN** a subscription account's Realtime session relays them unchanged
- **AND** after the close the key is charged once for the summed `response.done` usage

#### Scenario: A browser authenticates through the subprotocol
- **WHEN** a browser offers `realtime` and `openai-insecure-api-key.<client secret>`
- **THEN** the server selects `realtime`, opens the session for the secret's key and sends the secret's session first

#### Scenario: The upstream refuses the session
- **WHEN** the upstream refuses the session dial
- **THEN** the client socket closes with status 1011 and reason `realtime_session_unavailable`, without the upstream body, and nothing is charged

#### Scenario: A web page on another origin opens a session
- **WHEN** a page on `https://app.example` opens `/v1/realtime?model=gpt-realtime` with `realtime` and `openai-insecure-api-key.<client secret>`
- **THEN** the upgrade succeeds and the session runs for the secret's key
- **AND** the same page is still refused on the `call_id` sideband

### Requirement: Third-party apps create WebRTC calls

`POST /v1/realtime/calls` and `/v1/realtime/calls/` SHALL accept `application/sdp`, `multipart/form-data` with an `sdp` part and an optional `session` part, and `application/json` `{sdp, session}`, up to 1 MiB, and SHALL refuse other media types with 415 before dispatch. The session SHALL be taken from the request, else from the client secret used, else `{type: "realtime", model: <query model or gpt-realtime>}`. It SHALL be sent to the subscription backend as JSON `{sdp, session}`. The client SHALL receive the SDP answer as `application/sdp` and `Location: /v1/realtime/calls/<call id>` once the key-scoped owner is bound. The call SHALL be reachable through the `/v1/realtime?call_id=` sideband and SHALL be recorded without usage, since its media bypasses the runtime.


`POST /v1/realtime/calls` and `/v1/realtime/calls/` SHALL be exempt from cross-origin request protection. Their answers, refusals included, SHALL carry `Access-Control-Allow-Origin: *` and `Access-Control-Expose-Headers: Location`. `OPTIONS` on these paths SHALL answer a CORS preflight with 204, `POST` as the allowed method, `Authorization, Content-Type` as the allowed headers and a 600-second max age, without authentication. Every other proxy route, `/v1/realtime/client_secrets` included, SHALL keep its cross-origin protection.
#### Scenario: A browser posts a raw offer with a client secret
- **WHEN** a browser posts `application/sdp` with `Authorization: Bearer ek_…`
- **THEN** the backend receives JSON with that SDP and the secret's session, and the browser receives the SDP answer and the call location

#### Scenario: An unsupported offer
- **WHEN** the offer uses another media type
- **THEN** it is refused with 415 before any upstream request

#### Scenario: A web page on another origin creates a call
- **WHEN** a page on `https://app.example` sends a preflight and then posts its SDP offer with `Authorization: Bearer ek_…`
- **THEN** the preflight is allowed, the offer creates the call, and the page can read the SDP answer and `Location`
- **AND** the same page is still refused by `/v1/realtime/client_secrets` and `/v1/responses`
