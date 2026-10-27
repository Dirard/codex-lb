## 1. Logging

- [x] 1.1 Access log middleware with request IDs, error-code extraction and status-based levels; `--log-level`; JSON net/http error log and final error.
- [x] 1.2 `application.RunPeriodic` with panic recovery and outcome reports; deduplicating reporter with error classes; startup jitter.
- [x] 1.3 Startup and drain summaries.

## 2. Metrics

- [x] 2.1 Dependency-free Prometheus registry and gauges; optional `--metrics-listen` with pprof; `/metrics` and `/debug` return 404 on the public listener.

## 3. Deployment and updates

- [x] 3.1 Adopt a replaced bootstrap executable in either direction and log the selected runtime.
- [x] 3.2 Hardened systemd template (1.4 in `systemd-analyze security`), Docker `--self-update=false` and stop-timeout guidance.

## 4. Verification

- [ ] 4.1 `make check`; sync deltas into the main specs and archive after merge.
