## Why

Production moved from a 1 vCPU / 1 GB VPS to 6 vCPU / 4 GB, but every capacity bound is a constant measured for the small host: 512 generations, a 128-request queue, 128 body readers, 4096 client WebSockets, 512 upstream sessions and connections, 8 SQLite readers, and no Go soft memory limit. The service cannot use a larger host and can exceed a smaller container's memory before GC reacts.

## What Changes

- Detect CPUs, cgroup-aware memory and the descriptor limit at startup.
- Derive generation, queue, body-reader, client-WebSocket, upstream-session/connection and SQLite-reader bounds from memory tiers that keep the measured 1 GB defaults exactly; set a 75% Go soft memory limit unless GOMEMLIMIT is exported.
- Add `--memory-limit`, `--max-generations` and `--max-client-websockets` overrides.
- Log the plan at startup and expose it with live usage at `GET /api/runtime/capacity` and on the dashboard runtime page.

## Capabilities

### Modified Capabilities

- `go-runtime`: capacity sizing replaces fixed bounds; affected requirements keep their 1 GB numbers as the 1 GB-class tier.

## Impact

`cmd/codex-lb`, new `internal/adapters/hostinfo`, `internal/domain` capacity planning, configurable bounds in sqlite, httpapi and upstream adapters, a new admin route and dashboard card. No schema or protocol change. On a 1 GB-class host behavior is unchanged except for the new Go soft memory limit.
