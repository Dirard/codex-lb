## 1. Contracts

- [x] 1.1 Descriptor `handoff` and `schemaFloor`, identity comparison, seamless switch decision, widened rollback and boot recovery compatibility.

## 2. Shared database

- [x] 2.1 `IMMEDIATE` write transactions; concurrent two-store test.
- [x] 2.2 Migration 41, reservation owner instance, owner-scoped interrupted marking.
- [x] 2.3 Schema floor recording and newer additive schema opening.
- [x] 2.4 Explicit reprice resumption on the active worker; token refresh race recovery.

## 3. Admission

- [x] 3.1 Worker load snapshot and peer load (global, per account, per key).
- [x] 3.2 Retirement signal: idle WebSockets close, active turns finish, admission stays open.

## 4. Launcher and worker

- [x] 4.1 Launcher-owned listener and lock, inherited descriptors, lifeline, instance ids; release and re-acquire around older workers.
- [x] 4.2 Worker handoff endpoints, deferred background, retirement drain, memory limit.
- [x] 4.3 Seamless update flow with failure paths; multi-worker host bookkeeping and load relay.
- [x] 4.4 Boot delegation with crash guard.

## 5. Interface and verification

- [x] 5.1 Status flag, phases, confirmation text and locales.
- [x] 5.2 Real-binary integration tests: switch under load with a long stream, classic fallback, delegation.
- [x] 5.3 Gates: vet, lint, tests, race, web checks, strict OpenSpec validation.
