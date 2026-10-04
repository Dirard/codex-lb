# Tasks

## 1. Provider-confirmed recovery
- [x] 1.1 Preserve optional permission flags in usage snapshots and verify explicit/missing/negative parser payloads.
- [x] 1.2 Extend ordinary refresh recovery with backend permission, complete-window checks and explicit-denial veto; verify same-deadline, already-zero, legacy and guarded negative regressions.
- [x] 1.3 Verify refresh through SQLite into visible account status/selection with deterministic provider fixtures, including concurrent refusal/operator changes.

## 2. Contract synchronization
- [x] 2.1 Synchronize go-runtime spec/context and validate both OpenSpec and layered design; run required Go tests/race/vet without touching live services.
