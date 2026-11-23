## Why

The completed Go rewrite has a measured streaming latency regression under
concurrent requests. The user requires production quality for hundreds of real
users, not a resource reduction bought with worse responsiveness. Offline phase
measurements locate the delay before upstream dispatch: the single SQLite
connection serializes reads behind durable accounting writes.

## What Changes

- Separate committed-snapshot reads from the serialized SQLite writer in WAL
  mode, keeping crash-safe accounting and all ownership/policy checks.
- Reduce redundant durable transactions only where measured contention remains
  and the existing atomicity, idempotency and outcome-order contracts are preserved.
- Extend the existing offline benchmark to compare the unchanged baseline and
  candidate at 8, 32, 128 and 256 concurrent requests, including multiple keys, p50/p95/p99,
  upstream/forwarding phases, cancellation and resource cleanup.
- Keep at least 256 long streams active; align the outbound HTTP connection cap
  with that bound and remove measured redundant context copies. 128 MiB is a
  preferred budget, not a reason to count queued large requests as active.
- Keep tuning in the server implementation, not in weakened test settings or
  new operational switches. No production deployment is authorized.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: concurrent read/write isolation, durable admission/accounting,
  and measured concurrent-streaming behavior.

## Impact

SQLite connection lifecycle and repositories, accounting integration if needed,
offline benchmarking and regression tests. No schema migration is expected.
Public APIs, UI, provider semantics and authentication boundaries remain stable.
