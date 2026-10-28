# Adaptive capacity: size admission, sockets, pools and the Go memory limit from the host

- Last edited with skill pack: `0.2.2`

## Title and scope

codex-lb must run efficiently on servers of different sizes (production moved from 1 vCPU / 1 GB to 6 vCPU / 4 GB), so the generation, queue, body-reader, WebSocket, upstream-connection and SQLite-reader limits and the Go soft memory limit are derived from the detected CPU and memory instead of fixed constants. Operators can override the derived values, and the dashboard shows what was detected, what is in effect and current usage.

## Planning anchor

- Entrypoints: `cmd/codex-lb/runtime.go#openRuntime` (`application.ProxyConfig{}`, `httpapi.NewProxyHandler`, `http.Transport{MaxConnsPerHost: 512}`), `cmd/codex-lb/storage.go#openData` (`sqlite.Open`), `internal/adapters/sqlite/store.go#readPoolSize` (8), `internal/adapters/httpapi/proxy.go#NewProxyHandler` (128 body readers, 4096 client WebSockets), `internal/adapters/upstream/websocket_sessions.go#acquire` (512 lanes), `internal/application/proxy.go#NewProxy` (512 / 128 defaults).
- Changed assumption: the fixed defaults were measured and approved for a 1 vCPU / 1 GB host. On 4 GB they leave memory and cores unused; on smaller hosts they can exceed memory. No `GOMEMLIMIT` is set, so the Go heap can grow past a container or VPS budget before GC reacts.
- Non-local: composition root, storage, HTTP ingress, upstream adapter, admin API and dashboard.
- Impacted specs in `specs/`: none. OpenSpec `go-runtime` requirements «Idle client WebSockets do not consume HTTP body-reader capacity», «Standby socket capacity does not multiply retained large input bodies» and «Streaming optimization is verified under concurrent user load» name fixed numbers — action `amend` through change `adapt-capacity-to-host`.

## Connected groups or observed existing logic

### Observed Existing Logic

1. Entry and orchestration: `NewProxy` applies `MaxStreams=512`, `MaxQueued=128` when zero; `AdmissionCapacity()` returns `MaxStreams+MaxQueued` and `NewProxyHandler` sizes WebSocket large-body permits as twice that (1280).
2. HTTP ingress: `reading` (128) bounds concurrently read HTTP bodies; `websockets` (4096) bounds authenticated client sockets.
3. Upstream: the ChatGPT WebSocket pool refuses or evicts idle sessions at 512 lanes; the HTTP transport allows 512 connections per host.
4. Persistence: one writer connection and a fixed 8-connection read pool.
5. Go runtime: GOMAXPROCS already follows cgroup CPU quotas (Go ≥1.25); the soft memory limit is unset unless `GOMEMLIMIT` is exported; the process raises the soft `RLIMIT_NOFILE` to the hard limit at start.
6. Measurements in `openspec/specs/go-runtime/context.md`: 512 mixed native generations plus 3584 idle sockets peaked at ~466 MiB RSS on one CPU; 512 × 256 KiB contexts peaked at ~750 MiB.

## Use cases

### 1. Detect the host resources available to the process
process start --read cgroup v2/v1 limits, physical memory, CPU and descriptor limit--> host resources

Requirements:

  R1:
The runtime SHALL use the smallest of the cgroup memory limit (v2 `memory.max`/`memory.high` along the process cgroup path, or v1 `memory.limit_in_bytes`) and physical memory (`MemTotal`), treat `max` and unrealistically large v1 values as unlimited, and record which source determined the value.

  R2:
CPU count SHALL be the effective `GOMAXPROCS`, which already reflects cgroup CPU quotas and an explicit `GOMAXPROCS` environment variable.

  R3:
When memory cannot be determined, sizing SHALL assume a 1 GiB host and say so.

Files And Functions:
 - planned: internal/adapters/hostinfo/hostinfo.go#Detect - reads an `fs.FS` rooted at `/` so tests use `fstest.MapFS`
 - planned: internal/adapters/hostinfo/rlimit_unix.go#fileDescriptorLimit

Tests:
 - description: cgroup v2 nested limit is the minimum along the path and below physical memory
 - description: cgroup v2 `max` falls back to MemTotal
 - description: cgroup v1 limit and the unlimited sentinel
 - description: missing files yield an explicit default source

### 2. Plan capacity from host resources and operator overrides
host resources and overrides --select the memory tier and derive limits--> capacity plan

Requirements:

  R4:
Limits SHALL follow memory tiers of usable memory: below 768 MiB — 256 generations and 2048 client WebSockets; below 1.75 GiB — 512 and 4096 (the measured 1 GB defaults); below 3.5 GiB — 1024 and 8192; below 7 GiB — 2048 and 16384; otherwise 4096 and 32768.

  R5:
The queue SHALL be a quarter of the generation bound (minimum 64, maximum 1024); upstream WebSocket sessions and upstream connections per host SHALL equal the generation bound; SQLite readers SHALL be `2×CPU+4` within 8…32. Request bodies on every proxy route share a byte budget of one eighth of the sizing memory (see `specs/request-body-budget.md`), which replaced the count-based body readers and WebSocket large-body permits.

  R6:
Client WebSockets SHALL be reduced so that sockets plus twice the generation bound plus 256 reserved descriptors fit the descriptor limit, never below 64.

  R7:
The Go soft memory limit SHALL default to 75% of usable memory. An exported `GOMEMLIMIT` takes precedence and its value is used for sizing; `--memory-limit off` leaves the runtime limit unset; `--memory-limit <size>` sets it explicitly and sizes from it.

  R8:
`--max-generations` and `--max-client-websockets` SHALL override the derived values; queue, upstream sessions and connections are derived from the effective generation bound. Invalid values SHALL fail startup naming the flag.

Data:
tiers:

| usable memory | generations | queue | client WebSockets |
|---|---|---|---|
| < 768 MiB | 256 | 64 | 2048 |
| < 1.75 GiB | 512 | 128 | 4096 |
| < 3.5 GiB | 1024 | 256 | 8192 |
| < 7 GiB | 2048 | 512 | 16384 |
| ≥ 7 GiB | 4096 | 1024 | 32768 |

Files And Functions:
 - planned: internal/domain/capacity.go#PlanCapacity - pure function
 - planned: internal/domain/capacity.go#HostResources, CapacityOverrides, CapacityPlan

Tests:
 - description: each tier boundary, including a 1 GB VPS reporting ~960 MiB MemTotal, keeps 512/128/128/4096
 - description: a 4 GB VPS reporting ~3.8 GiB gets 2048/512/512/16384 and 16 readers on 6 CPUs
 - description: descriptor limit clamps WebSockets; overrides win; GOMEMLIMIT sizing

### 3. Apply the plan at startup and expose it to the operator
capacity plan --set the Go memory limit and wire limits into storage, proxy, ingress and upstream--> sized runtime --log the plan and serve GET /api/runtime/capacity--> visible capacity

Requirements:

  R9:
Startup SHALL log one structured `runtime capacity` line with detected resources, sources and effective limits, and SHALL apply the plan before the listener opens.

  R10:
`GET /api/runtime/capacity` (administrator only) SHALL return detected resources, effective limits, which limits were overridden, and current usage: active and queued generations, client WebSockets, Go heap and process RSS.

  R11:
The dashboard SHALL show this information on the runtime settings page without polling more often than every 15 seconds while visible.

Files And Functions:
 - existing: cmd/codex-lb/config.go#parseConfig - new flags
 - existing: cmd/codex-lb/runtime.go#openRuntime - apply plan
 - existing: cmd/codex-lb/storage.go#openData - reader pool size
 - existing: internal/adapters/sqlite/store.go#Open - `OpenWithOptions`
 - existing: internal/adapters/httpapi/proxy.go#NewProxyHandler - `NewProxyHandlerWithLimits`
 - existing: internal/adapters/upstream/websocket_sessions.go#acquire - configurable lane bound
 - existing: internal/adapters/provider/provider.go#Config - upstream session bound
 - existing: internal/application/proxy.go - `AdmissionUsage`
 - planned: internal/adapters/httpapi/runtime_capacity.go - admin route
 - planned: web/src/features/runtime - capacity card

Tests:
 - description: route returns plan and usage only for administrators
 - description: runtime wires overridden limits into proxy admission and the handler budgets

## Implementation checklist

1. [x] OpenSpec change `adapt-capacity-to-host` amending the fixed-number requirements (R4–R11).
2. [x] `domain.PlanCapacity` with table tests (R4–R8).
3. [x] `hostinfo.Detect` with cgroup v2/v1/physical tests and descriptor limit (R1–R3, R6).
4. [x] Configurable bounds: sqlite readers, proxy handler limits, upstream session lanes, admission usage (R5).
5. [x] Flags, plan application, `GOMEMLIMIT` handling, startup log (R7–R9).
6. [x] Admin capacity route and dashboard card (R10, R11).
7. [x] `make check`, web checks; record measured plans for 1 GB and 4 GB profiles in the decision log.

## Open questions

None blocking.

## Decision log

- 2026-10-28: Memory tiers instead of a continuous formula keep the measured and administrator-approved 1 GB defaults exactly (512 generations, 128 queue, 4096 sockets) and make limits predictable for operators; a 1 GB VPS typically reports 900–990 MiB MemTotal and stays in the 512 tier.
- 2026-10-28: The soft memory limit is 75% of usable memory: below it Go GC runs normally; the remaining quarter covers non-heap memory and co-located services. It is soft, so the process never fails merely by reaching it.
- 2026-10-28: Measured plans: this 32-CPU/92 GiB workstation → 4096 generations, 32768 sockets, 32 readers; `systemd-run --scope -p MemoryMax=900M` → `cgroup_v2`, 943 MiB, 512 generations, 4096 sockets; `GOMEMLIMIT=1GiB` → 512 tier.
- 2026-10-28: Byte-weighted body budgets for every proxy route (audit NET-14 / security SEC-4) are planned as the next slice once the transport changes land; this slice keeps count-based bounds but sizes them from memory.
- 2026-10-28: Amended by `specs/request-body-budget.md`: body readers and WebSocket large-body permits are replaced by one byte budget (sizing memory / 8, 50% per key); R5 and the tier table no longer list body readers. Status: active, updated by that plan.
