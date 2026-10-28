## ADDED Requirements

### Requirement: Requests and background work are observable without exposing secrets

Every HTTP request and WebSocket SHALL receive a server-generated `X-Request-Id` and SHALL produce one structured log line when it ends, with method, path without query, status, response size, duration, resolved client IP, route group and the stable `error.code` of our JSON error envelope when present. Failures SHALL be logged at INFO (4xx) or WARN (5xx) and successes at DEBUG; `--log-level` SHALL select the minimum level. Request bodies, query strings, credentials and provider error messages MUST NOT be logged. Background loops (usage polling, automations, model catalog, maintenance) SHALL recover panics with their stack, report every run, log failures by a stable class without message text — immediately on a new class, at most every ten minutes while it persists, and once on recovery — and SHALL start after a bounded jitter. Startup SHALL log the effective configuration summary and drain SHALL log its duration and whether the grace deadline cancelled work. The final startup error SHALL be a single JSON line.

#### Scenario: A request is rejected for lack of accounts
- **WHEN** a client request fails with 503 `no_available_accounts`
- **THEN** one WARN line with the request ID, path, status and `error_code` is logged and the same ID is returned in `X-Request-Id`
- **AND** no request body, query or credential appears in the log

#### Scenario: A background poll keeps failing
- **WHEN** the usage poll fails with the same class on consecutive runs
- **THEN** the failure is logged once, again after ten minutes, and a recovery line is logged after the next success
- **AND** a panic in a run is logged with its stack and the loop continues

### Requirement: Runtime metrics are available on an opt-in private listener

When `--metrics-listen` is set, the runtime SHALL serve Prometheus text metrics and pprof on that separate listener: HTTP requests and durations by fixed route group and status class, in-flight requests, active and queued generations and client WebSockets against their limits, process and Go memory, goroutines, uptime, accounts by status, database and WAL size, background runs by result with their last success time, and build version. Labels MUST NOT contain account, key or request identifiers. The listener SHALL be off by default and SHALL warn when bound beyond loopback. On the public listener `/metrics` and `/debug` SHALL return 404 rather than the dashboard shell.

#### Scenario: An operator scrapes metrics
- **WHEN** the server starts with `--metrics-listen 127.0.0.1:9464`
- **THEN** `GET http://127.0.0.1:9464/metrics` returns the metric families in text format
- **AND** `GET /metrics` on the public address returns 404

### Requirement: Deployment templates restart, bound descriptors and sandbox the service

The systemd template SHALL restart indefinitely with backoff, set a descriptor limit sufficient for the host-sized socket budget, allow at least the drain grace plus accounting cleanup to stop, and apply systemd sandboxing that keeps `/proc/meminfo` and the cgroup filesystem readable for capacity detection. The Docker image SHALL disable the built-in version manager, because containers update by replacing the image, and SHALL document a stop timeout of at least 120 seconds.

#### Scenario: A transient startup failure
- **WHEN** the listen port is briefly busy at boot
- **THEN** systemd keeps retrying with increasing delay instead of leaving the unit failed
