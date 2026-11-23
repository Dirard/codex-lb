## 1. Scoped limit data

- [x] 1.1 Specify the existing-ledger and same-group read contract; validate OpenSpec and SDD artifacts.
- [x] 1.2 Add safe report DTOs and a single authorized read-only SQLite snapshot; verify HTTP isolation, stale membership/auth and non-mutating expired-window tests.
- [x] 1.3 Add the group's aggregate account-quota percentages within the same authorized snapshot; verify capacity weighting, scope, missing windows and upstream-visibility policy.

## 2. Shared presentation

- [x] 2.1 Extract the existing key-page limit/status widgets and use them in personal/group report sections; verify admin and report-login UI tests.
- [x] 2.2 Add localized explanations for current-window and held-budget values; verify typecheck, locale checks and offline browser rendering.

## 3. Integration

- [x] 3.1 Run full Go tests/race/vet and frontend checks; sync the verified spec/context and archive without updating the live service.
