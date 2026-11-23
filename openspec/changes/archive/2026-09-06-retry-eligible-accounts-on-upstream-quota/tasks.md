## 1. Quota recovery

- [x] 1.1 Reproduce the client-owned verified full-resend quota failure and preserve negative controls for unsafe replay and non-quota errors.
- [x] 1.2 Apply the minimum quota-specific correction, including the upstream handshake path if the same verified replay is available there.
- [x] 1.3 Verify account A to B recovery through the public WebSocket path without exposing the recovered quota error or stale account state.

## 2. Verification

- [x] 2.1 Run relevant WebSocket, HTTP-bridge, and HTTP/SSE regressions plus focused lint/type checks and strict change validation.
- [x] 2.2 Synchronize and archive verified requirements after the requested rebase and fork review.
