## Why

Operators could not see failures: the whole runtime had eight log calls, background poll errors were discarded, recovered panics left no stack, rejected requests (for example `503 no_available_accounts`) left no trace, and there were no metrics. `/metrics` and `/debug/pprof` returned the dashboard shell with 200. A manual emergency downgrade by replacing the bootstrap binary was silently ignored, and the systemd template scored 8.3 EXPOSED with no restart backoff or descriptor limit.

## What Changes

- Every request gets an `X-Request-Id`; one JSON access line is logged per request or WebSocket (failures at INFO/WARN, successes at DEBUG) with method, path without query, status, size, duration, resolved client IP and our own error code — never bodies, queries or provider messages. `--log-level` selects verbosity; net/http errors go through the same JSON logger; the final startup error is one JSON line.
- Background loops (usage poll, automations, model catalog, maintenance) run through one helper that recovers panics with their stack, reports outcomes, logs failures by stable class with deduplication and recovery, and starts after a short jitter so restart loops do not burst upstream.
- Optional `--metrics-listen` serves Prometheus text metrics (requests by route group and status class, durations, in-flight work, generations/queue/sockets against limits, memory, goroutines, accounts by status, database/WAL size, background runs) and pprof. It is off by default and separate from the public listener, where `/metrics` and `/debug` now return 404.
- Startup and drain log their configuration summary, durations and whether the grace deadline cancelled work.
- A replaced bootstrap executable (newer or older) is adopted by the managed runtime, and the launcher logs the selected version.
- Hardened systemd template and Docker defaults.

## Capabilities

### Modified Capabilities

- `go-runtime`: operational logging, metrics, health and deployment defaults.
- `runtime-updates`: bootstrap replacement semantics.

## Impact

`cmd/codex-lb`, new `internal/adapters/metrics`, `internal/application` background loops, `internal/adapters/updatefiles`, `internal/adapters/webui` reserved paths, `deploy/codex-lb.service`, `Dockerfile`. No schema or protocol change; no new runtime dependency.
