## 1. Contracts

- [ ] 1.1 Descriptor `handoff` and `schemaFloor`, identity comparison, seamless switch decision, widened rollback and boot recovery compatibility.

## 2. Shared database

- [ ] 2.1 `IMMEDIATE` write transactions; concurrent two-store test.
- [ ] 2.2 Migration 41, reservation owner instance, owner-scoped interrupted marking.
- [ ] 2.3 Schema floor recording and newer additive schema opening.
- [ ] 2.4 Explicit reprice resumption on the active worker; token refresh race recovery.

## 3. Admission

- [ ] 3.1 Worker load snapshot and peer load (global, per account, per key).
- [ ] 3.2 Retirement signal: idle WebSockets close, active turns finish, admission stays open.

## 4. Launcher and worker

- [ ] 4.1 Launcher-owned listener and lock, inherited descriptors, lifeline, instance ids; release and re-acquire around older workers.
- [ ] 4.2 Worker handoff endpoints, deferred background, retirement drain, memory limit.
- [ ] 4.3 Seamless update flow with failure paths; multi-worker host bookkeeping and load relay.
- [ ] 4.4 Boot delegation with crash guard.

## 5. Interface and verification

- [ ] 5.1 Status flag, phases, confirmation text and locales.
- [ ] 5.2 Real-binary integration tests: switch under load with a long stream, classic fallback, delegation.
- [ ] 5.3 Gates: vet, lint, tests, race, web checks, strict OpenSpec validation.
