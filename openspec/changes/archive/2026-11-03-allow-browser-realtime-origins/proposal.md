## Why

go-v1.12.0 issues client secrets for browsers, but a web page on another origin cannot use them:
- the proxy's cross-origin protection refuses its `POST /v1/realtime/calls`, and there are no CORS headers;
- the WebSocket accept admits only the same origin.

The owner asked to let browsers on other origins reach voice directly.

## What Changes

- The app Realtime WebSocket session accepts any `Origin`. Its credential is always explicit (bearer or subprotocol) and never a cookie, so a foreign page gains nothing without a key or a client secret.
- `POST /v1/realtime/calls` is exempt from cross-origin protection and answers CORS: a preflight on `OPTIONS`, `Access-Control-Allow-Origin: *` and an exposed `Location`.
- The call sideband, Codex realtime routes, client secret issuance and all other proxy routes keep their protection.

## Capabilities

### Modified Capabilities

- `go-runtime`: the app session and app call requirements gain their browser origin rules.

## Impact

- No schema, API shape or provider change.
