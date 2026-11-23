## 1. Native interruption

- [x] 1.1 Wire request-scoped interruption readiness through ingress, application and ChatGPT provider; validate current response IDs and preserve authorization, verified by route tests.
- [x] 1.2 Relay interruption on the leased upstream socket, forward actual ACK and preserve terminal usage across races; verify upstream loopback regressions and unchanged legacy cancellation.

## 2. Verification

- [x] 2.1 Exercise canonical/trailing-slash routes, active/late/foreign controls, follow-up continuation, usage settlement and cancellation using deterministic existing fixtures.
- [x] 2.2 Run focused and complete Go tests, race tests and vet; sync main specification/context and archive only after verification. Do not commit, publish or deploy.
