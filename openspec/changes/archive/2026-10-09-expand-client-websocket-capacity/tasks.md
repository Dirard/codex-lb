# Tasks

## 1. Client admission and input ownership
- [x] 1.1 Raise the existing global socket gate from 256 to 4096 without a new per-key ceiling; cover independent request admission and teardown.
- [x] 1.2 Bound large message lifetime from first substantial read through queue/response cleanup, preserving small cancellation frames.

## 2. Verification
- [x] 2.1 Measure idle pools and active streams with the existing offline benchmark; preserve accounting and report server-only resources.
- [x] 2.2 Synchronize Go-runtime specs/context, run Go/race/vet and validators, and archive completed work without publication or deployment.
- [x] 2.3 At the user's request, test 512 simultaneous generations in an isolated build with test-only admission/transport ceilings. Measure native client WebSocket workloads at several input sizes, preserve content/accounting and report whether 500–600 MB is sufficient; do not change production defaults or deploy.
