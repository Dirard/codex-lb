## Why

The owner wants voice for third-party apps through codex-lb, over both WebSocket and WebRTC. Today realtime in codex-lb serves only Codex's own shapes: call creation on `/backend-api/codex/realtime/calls` and a sideband by call id. Standard Realtime clients open `wss://…/v1/realtime?model=…` or post an offer to `/v1/realtime/calls`, and browsers use short-lived client secrets.

Owner-approved probes on 2026-11-03 from prod showed what the subscription serves:
- with a pool account's ChatGPT token, `wss://api.openai.com/v1/realtime?model=gpt-realtime` opens a standard session, applies a `session.update` and answers with `response.done` usage;
- the ChatGPT backend creates a call from JSON `{sdp, session}` with a standard session, but refuses raw SDP and multipart offers.

## What Changes

- `GET /v1/realtime?model=…` without `call_id` opens a Realtime WebSocket session on one subscription account.
  - A browser can authenticate through the `openai-insecure-api-key.` subprotocol.
  - Each session reserves a budget on the key and settles once with the summed `response.done` usage at realtime list prices.
- `POST /v1/realtime/calls` accepts raw SDP, multipart and JSON offers, sends the backend JSON `{sdp, session}`, and returns the SDP answer with `Location: /v1/realtime/calls/<id>`. The existing sideband serves the call.
- `POST /v1/realtime/client_secrets` issues `ek_…` secrets bound to an API key and an optional session. They are accepted only by the session and call routes, until they expire.
- Migration 49 (additive) adds `realtime_client_secrets`.

## Capabilities

### Modified Capabilities

- `go-runtime`: three added requirements for app sessions, app calls and client secrets.

## Impact

- Schema 49, additive: the previous version keeps working during the update.
- Provider traffic: realtime sessions and calls on subscription accounts. Their effect on Codex usage limits is unmeasured, and sessions do not change account health.
- WebRTC media bypasses codex-lb, so calls are recorded without usage.
