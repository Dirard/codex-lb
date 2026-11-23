## Why

The owner wants GPT-Live for third-party apps: the full-duplex voice model behind ChatGPT Voice and Codex voice, which listens and speaks at once, rather than the turn-based GPT-Realtime that go-v1.12 serves («мне нужен именно live»).

Owner-approved probes on 2026-11-03 from prod showed what a ChatGPT subscription token can reach:
- WebSocket-only Live sessions are refused with `forbidden` ("Voice session access denied."), both the public `/v1/live/sessions` and the alpha `/v1/live?model=`;
- a WebRTC call created the way Codex creates it works with real speech: `POST /backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas` with `OpenAI-Alpha: quicksilver=v2`, JSON `{sdp, session}`, models `gpt-live-1-codex` and `gpt-live-1.1-codex`, client delegation;
- the model hands work to the app (`delegation.created` with the user's words), takes the answer (`delegation.context.append`) on the data channel and speaks it;
- usage arrives on any sideband as `session.usage.updated` and `session.closed` with `audio_duration_ms`; several sidebands can attach to one call, and closing one leaves the call running.

## What Changes

- `POST /v1/live/calls` accepts raw SDP, multipart and JSON offers with an API key or a client secret, completes the session (default model `gpt-live-1.1-codex`, client delegation), creates the call on a subscription account with the quicksilver alpha and returns the SDP answer with `Location: /v1/live/<id>`. Browsers on any origin may call it.
- Each call reserves 10 minutes at $0.05 per minute on the key before dispatch. codex-lb attaches its own read-only sideband, follows `audio_duration_ms` and settles once as a `live_session` event when the call ends.
- `POST /v1/live/client_secrets` issues the same `ek_` secrets as `/v1/realtime/client_secrets`, and secrets work on the Live call route.
- A v3 sideband without `OpenAI-Alpha` gets `quicksilver=v2` upstream.

## Capabilities

### Modified Capabilities

- `go-runtime`: two added requirements (Live calls, Live metering) and two modified ones (Realtime protocol negotiation, client secrets).

## Impact

- No schema change.
- Provider traffic: GPT-Live calls on subscription accounts, plus one long-lived sideband per call from codex-lb. Their effect on the subscription's voice limits is unmeasured; calls do not change account health.
- A call that outlives its worker during an update is charged up to the last known usage.
