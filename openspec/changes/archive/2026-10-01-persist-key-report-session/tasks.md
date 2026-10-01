## 1. Session and reports

- [x] 1.1 Add restricted encrypted report grants, live key validation and HTTP session/report routes; verify expiry, revocation, isolation, cookies, CSRF, firewall and restart with Go regression tests.

## 2. Browser lifecycle

- [x] 2.1 Restore the report session on page load, remove retained API-key state, await logout and keep transient failures retryable; verify frontend login/reload/logout tests and translations.

## 3. Integration and specification

- [x] 3.1 Run Go tests/race/vet, UI suite/typecheck/build and actual browser reload/logout against a temporary embedded runtime with synthetic data.
- [x] 3.2 Sync the go-runtime requirement/context, record results and archive the change after strict OpenSpec and SDD validation.
