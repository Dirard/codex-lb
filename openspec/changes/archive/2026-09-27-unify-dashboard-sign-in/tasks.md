# Shared sign-in tasks

## 1. Frontend

- [x] 1.1 Move key authentication into the common sign-in gate and preserve scoped report-session cleanup; verify auth and portal regressions including mode switching.
- [x] 1.2 Replace the old key-login route with a shared-entry redirect and update all existing locales; verify route recovery and typecheck.

## 2. Verification and specification

- [x] 2.1 Run focused and frontend integration tests, lint, build and a synthetic browser check without replacing the running service.
- [x] 2.2 Synchronize go-runtime spec/context, pass strict OpenSpec/layered validation and archive the verified change.
