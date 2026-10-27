# Owner audit 2026-10-28 — prioritized backlog

Seven independent audits of `main` @ `0b674e97` (go-v1.0.10). Each report lists findings with evidence (`file:line` at that commit), proposed fix, effort and verification. The security report is kept out of the public tree until its fixes are released; its IDs (SEC-*) are referenced here only by title.

| Report | Area | IDs |
|---|---|---|
| [01-proxy-application.md](01-proxy-application.md) | proxy orchestration, selection, settlement | APP-1…17 |
| [02-sqlite.md](02-sqlite.md) | storage, writer, schema, growth, reports | DB-1…23 |
| [04-transport.md](04-transport.md) | HTTP/SSE/WebSocket transport, upstream pool | NET-1…25 |
| [05-ops-runtime.md](05-ops-runtime.md) | lifecycle, observability, self-update, deploy | OPS-1…23 |
| [06-frontend.md](06-frontend.md) | dashboard | WEB-1…23 |
| [07-quality-hygiene.md](07-quality-hygiene.md) | lint, tests, deps, OpenSpec, process | QA-1…25 |

Production context: the service moved from a 1 vCPU / 1 GB host to 6 vCPU / 4 GB; it must size itself to the host it runs on.

## Slices in delivery order

1. **Engineering baseline** ([solution spec](../../engineering-baseline.md)) — patched toolchain (11 stdlib CVEs, QA-1/SEC-12), lint gate, CI, reproducible tag releases, clean-checkout build (OPS-8), process docs and OpenSpec cleanup (QA-2…5, QA-14, OPS-19/20), transcription MIME bug (QA-11). Release go-v1.0.11.
2. **Adaptive capacity** — derive admission, body-memory budget, WebSocket budgets, SQLite pools and `GOMEMLIMIT` from cgroup-aware CPU/memory detection with operator overrides; byte-weighted body budget on every proxy route (SEC-4, NET-14, NET-17).
3. **Hot path latency** — one write job before dispatch (APP-1) and one after the terminal (APP-2, NET-4), all writes through the batch writer and no 5 s writer starvation (DB-1, DB-2), per-selection price memo and selection snapshot (APP-7, DB-9), byte prefilters and fewer JSON passes (APP-3, NET-5, NET-7), lazy history decrypt (APP-4), separate analytics pool (DB-7).
4. **Correctness** — conversation stripped on quota failover (APP-5), compact Lite flag (APP-11), dashboard overview failure at ≥139 accounts (DB-3), upstream WebSocket pool: stale frames, blocked pong reader, dead idle sockets (NET-1, NET-2, NET-3), HTTP/2 ping and connection reuse (NET-9, NET-10), quadratic tool-call accumulation (NET-11), atomic automation delete (DB-20).
5. **Security** — implicit loopback locality behind a same-host proxy, trusted proxy defaults, auth endpoint body limits and bcrypt outside the global mutex, account-wide attempt limits, TOTP enforcement once configured, session revocation, model-source SSRF guard, CSP/HSTS, release signature verification.
6. **Observability and operations** — access log with request IDs, panic stacks and background-loop errors (OPS-1), opt-in metrics listener and 404 for `/metrics` on the main port (OPS-2), status endpoint (OPS-3), config/env validation with named errors (OPS-4), downgrade adoption and runtime-selection log (OPS-5), scheduled online backups (OPS-6), background-loop jitter and recovery (OPS-12), drain summary (OPS-14), hardened systemd unit and Docker defaults (OPS-10, OPS-11).
7. **Data growth** — retention that keeps up (DB-4), reservation pruning (DB-5), WAL size limit and checkpointing (DB-8), deduplicated continuation aliases and O(1) bounds accounting (APP-6, DB-11), `PRAGMA optimize` (DB-22).
8. **Dashboard** — precompressed static assets and API compression (WEB-1), lazy locales and charts (WEB-2, WEB-17), parallel tests (WEB-3/4), dead fields and UI removal (WEB-6, 9–11), tokens/s and duplicate-email bugs (WEB-7, WEB-8), polling reductions (WEB-12, WEB-13), dependency cleanup (WEB-20…22).
9. **Maintainability** — split `Proxy.Respond` into phases (APP-16, QA-7), `t.Parallel` and configurable timers in tests (QA-8, QA-9), route tests for auth handlers (QA-6), remove dead ledger APIs (DB-13), decide on legacy import removal (DB-14, OPS-21) and the managed self-update design (OPS-7).
