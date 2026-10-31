## 1. Before coding

- [x] 1.1 Owner decides whether codex-lb may use imported CLI credentials and refresh them with the CLI's client id.
- [x] 1.2 Probe with the owner's sign-in: refresh outside the CLI, effect of `grok logout` on a copied refresh token, one `grok-imagine-video-1.5` job with `image`, `last_frame` and `prompt`, pool cost, the refusal returned at a limit.

## 2. Implementation

- [x] 2.1 Migration 44 and store.
- [x] 2.2 xAI adapter: token refresh, create and status calls.
- [x] 2.3 Pool service: import, allow list, selection, creation with failover, status transitions, sweep.
- [x] 2.4 REST routes and admin routes.
- [x] 2.5 Dashboard page.

## 3. Verification

- [x] 3.1 Route, migration and dashboard tests with a loopback xAI stub.
- [x] 3.2 Gates, strict OpenSpec validation, release notes.
