# Operational observability: context

## Reading the logs

```sh
journalctl -u codex-lb -o cat | jq -c 'select(.msg=="http request" and .status>=500)'
```

A rejected request looks like:

```json
{"level":"WARN","msg":"http request","request_id":"req_exqk6v6mzk4x3e3i","method":"POST","path":"/v1/responses","group":"responses","status":503,"bytes":96,"duration_ms":4,"client_ip":"198.51.100.7","error_code":"no_available_accounts"}
```

The same `request_id` is returned to the client in `X-Request-Id`. Successful requests are logged only with `--log-level debug`. Bodies, query strings and provider error messages are never logged; background failures are logged by class (`provider:upstream_timeout`, `timeout`, a Go type name), a persisting failure at most every ten minutes, and its recovery once.

## Metrics

Start with `--metrics-listen 127.0.0.1:9464` and scrape `http://127.0.0.1:9464/metrics`. The listener has no authentication; keep it on loopback or a private network. pprof is under `/debug/pprof/` on the same listener. Labels are fixed route groups, status classes, task names and account statuses — never account or key identifiers.

## Updates by binary replacement

With the managed runtime, replacing `/usr/local/bin/codex-lb` now takes effect on the next start whether the new binary is newer or older; restarting with the same binary keeps an administrator's dashboard rollback. The launcher logs `managed runtime selected` with the bootstrap and selected versions. An older binary still refuses a newer database schema, so a downgrade across a schema change needs the pre-upgrade backup.

## systemd

The template restarts indefinitely with backoff (`StartLimitIntervalSec=0`, `RestartSteps`, `RestartMaxDelaySec` on systemd ≥254; older versions ignore the keys), sets `LimitNOFILE=65536`, `TimeoutStopSec=120s` and strict sandboxing. `/proc/meminfo` stays visible because capacity detection reads it. Set `MemoryMax=` to reserve memory for other services; capacity sizing honors it. Verify the template on a staging host before replacing a production unit.
