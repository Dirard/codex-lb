## 1. Persistence and group management

- [x] 1.1 Add group, membership, limit-template models and optional key association; verify a single migration head and upgrade/downgrade/re-upgrade data retention in temporary databases.
- [x] 1.2 Implement authenticated group CRUD with validation, atomic updates and safe deletion; verify API tests cover successful mutations, invalid input, conflicting membership, and denied writes.

## 2. Key inheritance and enforcement

- [x] 2.1 Add optional group association to key create/update/response and live account resolution; verify ungrouped compatibility, grouped overrides rejected, regeneration, and safe detach through API tests.
- [x] 2.2 Reuse per-key accounting for synchronized group limit templates; verify independent counters, amount edits without usage reset, limit exhaustion, and settlement of an outstanding reservation across an edit.
- [x] 2.3 Invalidate affected cached policy and enforce group scope on new requests and reused connections; verify HTTP/WebSocket group updates and removed-owner fail-closed behavior without regressing ungrouped zero-quota continuity.

## 3. Dashboard

- [x] 3.1 Add group management using the existing account picker and limit editor; verify create/edit/delete and validation behavior with frontend tests.
- [x] 3.2 Add optional group selection to key dialogs and display inherited settings with per-key usage; verify legacy forms, group switching, and related query invalidation with frontend tests and TypeScript checks.

## 4. Verification and handoff

- [x] 4.1 Run relevant backend/frontend regression suites, formatting/type/architecture checks, and strict OpenSpec validation; fix regressions within this feature's scope.
- [x] 4.2 Inspect the finished UI locally using fixtures, verify the implemented behavior against the change, and archive the verified change with stable context; do not commit, push, release, or deploy.
