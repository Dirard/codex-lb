## 1. Implementation

- [x] 1.1 Domain rule for image-limit identifiers and the reset headers to pass.
- [x] 1.2 Image routes: accept `stream: true`, relay an upstream event stream, settle from the completed event, return JSON answers as JSON.
- [x] 1.3 Image routes: pass reset headers on a final 429.
- [x] 1.4 Responses: classify image-limit 429 (HTTP stream and WebSocket handshake), settle uncharged without account outcome or replay, return 429 `usage_limit_reached` with reset headers.

## 2. Verification

- [x] 2.1 Route tests: streamed generation relayed and settled once; streaming request answered with JSON; stream without a completed image; final 429 carries reset headers; Responses image-limit 429 keeps the account active and is not replayed, while a plain quota 429 still marks the account.
- [ ] 2.2 Gates and strict OpenSpec validation.
