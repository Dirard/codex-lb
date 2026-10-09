## 1. Capacity promotion

- [x] 1.1 Promote application, HTTP and upstream WS limits to 512, synchronize derived input defaults and confirm the focused capacity tests pass.

## 2. Verification and release preparation

- [x] 2.1 Run Go/race/vet, UI build, binary integration, native mixed-load regression and strict spec validators; retain measured limits in context.
- [x] 2.2 Prepare go-v1.0.8 notes and static Linux amd64/arm64 packages; verify version/update metadata, contents and SHA-256 checksums, then archive the verified change. Publication follows this implementation checklist under the administrator's explicit authorization.
