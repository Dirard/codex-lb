# Key reports and group picker tasks

## 1. Key report self-service

- [x] 1.1 Add strict Bearer-bound report reads and a safe response projection; verify two-key isolation, invalid credentials, local bypass rejection and read-only behavior through HTTP.
- [x] 1.2 Add `/key-reports` login, scoped report display and logout with private cache lifecycle; verify frontend success/failure/logout and no admin calls or credential persistence.

## 2. Account memberships

- [x] 2.1 Add an atomic account-group membership update and account-card picker; verify multi-group assignment, rollback, unchanged unrelated memberships/limits and UI loading/read-only behavior.

## 3. Integration

- [x] 3.1 Run focused and integrated Go/race/frontend tests, build and synthetic browser/real-binary checks; record limits of real-provider verification.
- [x] 3.2 Synchronize go-runtime spec/context, validate specifications and archive the verified change.
