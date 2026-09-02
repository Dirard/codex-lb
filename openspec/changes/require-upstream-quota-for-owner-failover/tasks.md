## 1. Regression Coverage

- [x] 1.1 Add direct-WebSocket tests proving only upstream quota errors can authorize owner replay and verify the focused tests fail before implementation.
- [x] 1.2 Add HTTP-bridge and HTTP/SSE tests proving local state, refresh/connect, model, overload, and generic rate-limit failures do not move a hard owner while verified quota replay remains supported; verify the focused tests fail before implementation.
- [x] 1.3 Add a product-path HTTP-bridge test proving safe quota replay replaces the hard-owner bridge and dispatches through an eligible account without old account continuity; verify it fails before implementation.
- [x] 1.4 Add a product-path control proving a failure after replacement reconnect retires the B generation without partially restoring account A.

## 2. Implementation

- [x] 2.1 Add one narrow upstream-quota transfer predicate and make direct WebSocket, HTTP bridge, and HTTP/SSE owner-move paths use it; verify focused regression tests pass.
- [x] 2.2 Preserve the normalized upstream terminal and request-log metadata whenever transfer is not authorized, safe, or possible; verify non-quota and unsafe-quota tests pass.
- [x] 2.3 Make authorized HTTP-bridge quota replay retire the exhausted hard-owner generation and select a replacement account while preserving the canonical thread key; verify the product-path regression passes.
- [x] 2.4 Keep account, socket, lease, headers, and durable ownership consistent by retiring a replacement generation when post-reconnect replay setup fails.

## 3. Verification and Release

- [x] 3.1 Run focused proxy/WebSocket/HTTP-bridge suites, Ruff, format checks, and strict OpenSpec validation with no new failures (change strict PASS; aggregate remains at the pre-existing 50/58).
- [ ] 3.2 Commit and push the fork release, build the Docker image, replace the root `codex-lb` container while preserving its volume, ports, and configuration, then verify `/health/ready` and the running image.
