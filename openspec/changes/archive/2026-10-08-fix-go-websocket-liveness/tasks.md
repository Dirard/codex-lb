# Tasks

## 1. Stream liveness
- [x] 1.1 Restore native WebSocket application heartbeats with attempt-scoped real IDs and single-writer cancellation/terminal cleanup.
- [x] 1.2 Bound upstream transport liveness probes without treating pong as progress or replaying uncertain requests.
- [x] 1.3 Cover public route compatibility, safe quota failover, accounting and slow/broken peers with local regressions.

## 2. Verification and release preparation
- [x] 2.1 Run full Go/race/vet and specification validation; synchronize go-runtime context and release notes.
- [x] 2.2 Build Linux amd64/arm64 release assets and checksums; retain live services unchanged.
