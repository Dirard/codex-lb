# Tasks

## 1. Contracts and release validation
- [x] 1.1 Add typed update status/version/descriptor contracts; verify numeric stable-tag parsing and compatible platform/schema/protocol tests.
- [x] 1.2 Implement fixed-origin bounded release discovery/download, checksum and archive validation; verify corrupt, oversized, linked/path-traversal and incomplete releases stay rejected before execution.

## 2. Managed runtime and recovery
- [x] 2.1 Add private version storage and durable transition state; verify permissions, atomic writes, retained previous version and interrupted-transition recovery.
- [x] 2.2 Add process-manager-independent parent/worker startup and private control channel; verify the same binary works directly without external service commands.
- [x] 2.3 Add race-free idle admission and consistent pre-switch backup; verify active HTTP/SSE/WS/realtime work is not force-cancelled and only one worker owns SQLite.
- [x] 2.4 Add readiness-gated activation and startup-failure rollback; verify preserved data/key/settings/usage and failed candidate cleanup with actual subprocess fixtures.

## 3. Administrator controls
- [x] 3.1 Wire administrator-only status/check/apply/rollback endpoints; verify CSRF, key-report denial, unknown targets, duplicate operations and request cancellation safety.
- [x] 3.2 Add Settings update controls with confirmation, progress and reconnect handling; verify frontend tests, accessibility labels, no key-report exposure and disabled/unavailable/error states.

## 4. Integration and documentation
- [x] 4.1 Verify end-to-end update and rollback using local release fixtures and built binaries; no production update or paid provider traffic.
- [x] 4.2 Run Go tests/race/vet, frontend tests/lint/typecheck/build and spec validators; record actual limitations and synchronize runtime-updates spec/context without publishing or deploying.
