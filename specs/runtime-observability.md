# Runtime observability: request logs, background-task reporting, metrics and safer deployment defaults

- Last edited with skill pack: `0.2.2`

## Title and scope

Operators must see what the service does in production: each request gets an ID and a log line on failure, background polls report and survive panics, and an opt-in private listener exposes metrics and pprof. Deployment defaults (systemd sandboxing and restart backoff, Docker without self-update) and bootstrap-replacement semantics make recovery predictable.

## Planning anchor

- Entrypoints: `cmd/codex-lb/runtime.go#openRuntime` and `#serveListener`, `cmd/codex-lb/lifecycle.go#track`, `internal/application/account_usage.go#RunUsagePoller`, `automations.go#RunAutomationsPoller`, `model_catalog.go#Run`, `cmd/codex-lb/maintenance.go#maintainReports`, `internal/adapters/updatefiles/store.go#Initialize`, `deploy/codex-lb.service`, `Dockerfile`.
- Changed assumption: failures were visible only to clients; logs and metrics are now part of the operator contract.
- Audit sources: `specs/audits/2026-10-28/05-ops-runtime.md` OPS-1, OPS-2, OPS-5, OPS-10, OPS-11, OPS-12, OPS-14, OPS-15.
- Impacted specs in `specs/`: `adaptive-capacity.md` (`reuse`: gauges report its limits). OpenSpec change `observe-runtime-operations` amends `go-runtime` and `runtime-updates`.

## Connected groups or observed existing logic

### Observed Existing Logic

1. The server handler is `requests.track(mux)`; health endpoints bypass the admin mux. `http.Server.ErrorLog` was unset.
2. The three service loops and maintenance discarded errors (`_ = s.RefreshAll(ctx)`) and had no panic recovery; they started immediately.
3. Recovered panics elsewhere (`proxy.go`, SSE/WebSocket workers, writer) are outside this slice and owned by the hot-path and transport slices.
4. `updatefiles.Store.Initialize` adopted only a newer bootstrap that differed from the recorded one.

## Use cases

### 1. Log every request with an ID
request --assign request ID and wrap the writer--> served request --classify status and extract our error code--> access log line

Requirements:

  R1:
Each response SHALL carry `X-Request-Id`; one line per request or WebSocket SHALL be logged at DEBUG for success, INFO for 4xx and WARN for 5xx, with method, path without query, group, status, bytes, duration, resolved client IP and `error_code`.

  R2:
The wrapper SHALL preserve flushing, hijacking and `http.ResponseController` access; bodies, queries and messages MUST NOT be logged.

Files And Functions:
 - planned: cmd/codex-lb/access_log.go#accessLog
 - planned: cmd/codex-lb/access_log.go#routeGroup

Tests:
 - description: 503 with error envelope logs WARN with the code and request ID and no body or query
 - description: hijack and flush pass through the wrapper

### 2. Run background loops through one reporting helper
periodic task --run after jitter, every interval or trigger--> run outcome --recover panic and report--> deduplicated log line and metrics

Requirements:

  R3:
`application.RunPeriodic` SHALL recover panics into `PanicError` with the stack, report every run while the context is live, honor an initial delay and stop on cancellation.

  R4:
The reporter SHALL log a new failure class immediately, a persisting one at most every ten minutes, recovery once, and panics with their stack; classes are stable codes or Go type names.

Files And Functions:
 - planned: internal/application/background.go#RunPeriodic
 - planned: cmd/codex-lb/background.go#backgroundReporter

Tests:
 - description: panic, failure and success are reported in order and the loop continues
 - description: duplicate failures are suppressed until ten minutes pass; error text is never logged

### 3. Serve metrics on an opt-in private listener
configured metrics address --register counters and gauges--> metrics listener --scrape--> Prometheus text

Requirements:

  R5:
The listener SHALL be off by default, expose `/metrics` and `/debug/pprof/`, warn when not on loopback, and stop during drain; `/metrics` and `/debug` on the public listener SHALL return 404.

Files And Functions:
 - planned: internal/adapters/metrics/metrics.go#Registry
 - planned: cmd/codex-lb/metrics.go#registerGauges
 - existing: internal/adapters/webui/webui.go#reservedPath

Tests:
 - description: registry text format, histogram buckets and label escaping
 - description: runtime serves metrics only on the metrics listener and logs drain completion

### 4. Make deployment and recovery predictable
operator replaces binary or deploys template --adopt replaced bootstrap and log selection--> expected version runs

Requirements:

  R6:
A bootstrap whose digest differs from the recorded bootstrap SHALL be adopted in either direction; the same bootstrap keeps the current selection.

  R7:
The systemd template SHALL restart with backoff, set `LimitNOFILE=65536` and `TimeoutStopSec=120s`, and sandbox without hiding `/proc/meminfo`; the Docker image SHALL run with `--self-update=false`.

Files And Functions:
 - existing: internal/adapters/updatefiles/store.go#Initialize
 - existing: cmd/codex-lb/update_runtime.go#serveManagedRuntime
 - existing: deploy/codex-lb.service
 - existing: Dockerfile

Tests:
 - description: older replaced bootstrap is adopted and survives restart with the same binary

## Implementation checklist

1. [x] Access log middleware, `--log-level`, JSON error log and final error line (R1, R2).
2. [x] `RunPeriodic`, reporter and jitter; loops migrated (R3, R4).
3. [x] Metrics registry, gauges, opt-in listener, reserved paths (R5).
4. [x] Bootstrap adoption and selection log; systemd and Docker defaults (R6, R7).
5. [x] OpenSpec change `observe-runtime-operations`.
6. [-] `make check`; record results.

## Open questions

None blocking.

## Decision log

- 2026-10-28: Successful requests log at DEBUG so default INFO logs stay proportional to failures on a busy proxy; metrics carry the success counts.
- 2026-10-28: Metrics use a dependency-free registry: the needed families are few and the project avoids new runtime dependencies.
- 2026-10-28: Startup jitter: usage poll 10–30 s, automations 5–15 s, catalog 30–60 s (the catalog is loaded synchronously at startup). Routing uses persisted quota meanwhile.
- 2026-10-28: `systemd-analyze security --offline` rates the template 1.4 OK (was 8.3 EXPOSED). `ProcSubset=pid` and `PrivateUsers=true` from the audit draft were left out: the first hides `/proc/meminfo` from capacity detection, the second can break data-directory ownership checks.
