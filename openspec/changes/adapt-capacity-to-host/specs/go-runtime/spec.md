## ADDED Requirements

### Requirement: Capacity defaults follow the detected host

At startup the runtime SHALL detect the CPUs (effective GOMAXPROCS), the memory available to the process (the smallest of the cgroup v2/v1 limit along its cgroup path and physical memory, assuming 1 GiB when unknown) and the descriptor limit, and SHALL derive its bounds from that profile. Memory tiers SHALL be: below 768 MiB — 256 generations and 2048 client WebSockets; below 1.75 GiB — 512 and 4096; below 3.5 GiB — 1024 and 8192; below 7 GiB — 2048 and 16384; otherwise 4096 and 32768. The queue and concurrently read HTTP bodies SHALL be a quarter of the generation bound (queue 64…1024, readers at least 64); upstream WebSocket sessions and connections per upstream host SHALL equal the generation bound; SQLite readers SHALL be 2×CPU+4 within 8…32. Client WebSockets SHALL be reduced so that sockets, twice the generation bound and 256 reserved descriptors fit the descriptor limit, never below 64.

The Go soft memory limit SHALL default to 75% of available memory. An exported GOMEMLIMIT SHALL be kept and used for sizing; `--memory-limit off` SHALL leave the runtime limit unset and `--memory-limit <size>` SHALL set it and size from it. `--max-generations` and `--max-client-websockets` SHALL override the derived values; invalid values MUST fail startup naming the flag. The runtime SHALL log the detected profile and effective limits before serving, and `GET /api/runtime/capacity` (administrator only) SHALL return them with the overridden limits and current usage (active and queued generations, client WebSockets, Go heap, process RSS). Host-derived bounds MUST NOT bypass per-account, per-key or source caps.

#### Scenario: The service moves from a 1 GB to a 4 GB host
- **WHEN** the same binary starts on a 6-CPU host whose process may use 3.8 GiB
- **THEN** it admits up to 2048 generations with a 512-request queue, 16384 client WebSockets and 16 SQLite readers, and sets a 75% Go soft memory limit
- **AND** on a 1 GB host it keeps 512 generations, a 128-request queue and 4096 client WebSockets

#### Scenario: A container or systemd unit limits memory
- **WHEN** the process runs under `MemoryMax=900M` on a larger machine
- **THEN** sizing uses the 900 MiB cgroup limit and reports `cgroup_v2` as its source

#### Scenario: The operator overrides capacity
- **WHEN** the operator starts with `--max-generations 300 --memory-limit 2GiB`
- **THEN** 300 generations, a 75-request queue and a 2 GiB Go soft limit are in effect and the capacity endpoint lists both overrides

## MODIFIED Requirements

### Requirement: Idle client WebSockets do not consume HTTP body-reader capacity
The runtime SHALL admit up to the host-sized client WebSocket budget (4096 on a 1 GB-class host) without adding a lower per-key socket ceiling, independently of HTTP body-reader, active-generation and upstream-socket admission. Empty client sockets MUST NOT dispatch upstream or reserve generation/key usage. The global connection slot MUST be acquired after authentication and released on every failed upgrade, disconnect and drain. Actual socket exhaustion SHALL return the existing 503/local_capacity_exceeded envelope and Retry-After. The existing 120-second idle timeout and per-request authorization SHALL remain in force.

#### Scenario: HTTP remains usable beside idle subagent connections
- **WHEN** more than 256 authenticated client WebSockets are idle within the global budget
- **THEN** an eligible HTTP request and a response.create on an admitted WebSocket SHALL still execute under independent generation admission
- **AND** opening those idle sockets SHALL NOT create upstream work or usage reservations

#### Scenario: One key uses the available global pool
- **WHEN** a valid key opens connections while the global socket budget has room
- **THEN** its connections SHALL NOT be refused by a lower per-key socket ceiling

#### Scenario: Socket admission is released
- **WHEN** an upgrade fails, a client disconnects or the runtime drains
- **THEN** its global connection slot SHALL be released without changing active generation or continuation ownership

### Requirement: Standby socket capacity does not multiply retained large input bodies
The runtime SHALL bound large incoming WebSocket messages across reading, queued and executing responses at twice the configured active-plus-queued response capacity (1280 on a 1 GB-class host), allowing a current and one staged next message per admitted request. A message larger than 4096 bytes MUST acquire a body permit before retaining data beyond its small prefix and retain it until handled or discarded. Waiting on an empty socket and receiving a short control frame MUST NOT acquire such a permit. The existing 32 MiB message limit SHALL remain enforced. Rejection, validation failure, completion, cancellation, disconnect and queue cleanup MUST release owned body permits exactly once. Socket expansion MUST NOT add a smaller shared payload-byte ceiling that rejects otherwise admitted requests.

When every large-body permit is occupied, the runtime SHALL discard only the excess message with bounded scratch space under the existing message-size validation, return local capacity for that message, and keep the connection usable. It MUST NOT cancel the connection's active response, replay upstream work, or reclassify pressure as provider quota. The input-count bound MUST NOT be presented as a hard process-memory limit.

#### Scenario: Short cancellation remains available at the large-body ceiling
- **WHEN** all large-body permits are in use and an active client sends a short response.cancel
- **THEN** cancellation SHALL still be processed and release the active request's owned body permit

#### Scenario: Excess input does not abort admitted work
- **WHEN** an active client sends another large message while the current-plus-staged body capacity is full
- **THEN** only that excess message SHALL receive local capacity without being retained or dispatched
- **AND** the same connection's active request and subsequent short cancellation SHALL remain usable

#### Scenario: A queued frame outlives the client
- **WHEN** a client disconnects with a queued large request or an upgrade fails
- **THEN** cleanup SHALL leave no leaked connection slots or queued-body permits

### Requirement: Streaming optimization is verified under concurrent user load

Performance changes SHALL be compared against the preserved pre-change Go binary
on identical offline disk-backed workloads and durability settings, including
8, 32, 128 and 256 concurrent requests and multiple keys. Results SHALL report
first-token and completion p50/p95/p99, errors, resource consumption and
cancellation cleanup; failed requests MUST NOT be counted as fast successes.
Both native streaming Chat and Codex Responses SHALL preserve their current
output, usage, ownership and terminal contracts. Results for a synthetic local
provider MUST NOT be presented as a real-provider or production capacity SLA.
The target deployment is 1vCPU/1GB with a 500–600 MB service memory budget
and predominantly long-lived streams; measured memory use and workload-dependent
limitations SHALL be reported. Verification SHALL include single-Go-processor
runs, peakRSS and larger request context rather than only tiny short requests.
At least512 active streams SHALL be tested without counting queued requests as
active. Additional concurrency MUST NOT weaken accounting/authentication.
Configuration/data files MAY be reorganized only
where measurement supports the change and recovery/data-safety contracts remain.

The global active-stream bound SHALL follow the host capacity plan, supporting
512 concurrent requests with a bounded 128-request queue on a 1 GB-class host,
retain the 15-second queue timeout, and preserve all per-account
recovery/create/stream and configured source caps. Raising the
global bound MUST NOT authorize scope bypass or weaken cancellation cleanup.
The production HTTP transport MUST allow as many concurrent connections to one
upstream host as the active-stream bound, so its HTTP/1.1 connection queue does
not reduce this bound. The upstream WebSocket session store SHALL support as
many independent live sessions as the active-stream bound
without evicting active peers; its bounded-capacity refusal and idle-session
eviction SHALL retain owner, key, credential and capability isolation.

#### Scenario: The candidate is accepted after load testing
- **WHEN** the optimized runtime completes repeated concurrent streaming trials
- **THEN** measured TTFT and tail latency improvements SHALL be stated against the matching durable baseline
- **AND** accounting, cancellation, resource bounds and existing protocol regression tests SHALL remain passing

#### Scenario: At least256 independent subscription streams overlap
- **WHEN** 256 authorized clients have sufficient capacity across 40 eligible subscription accounts and the stub holds terminal responses after the first event
- **THEN** all 256 streams SHALL become active before release without bypassing per-account caps
- **AND** successful completion or cancellation SHALL release their owned resources and preserve accounting

#### Scenario: 512 mixed native generations complete without bypassing policy
- **WHEN** 512 permitted native WebSocket requests have sufficient provider capacity and a declared mixed-input workload is started progressively on the single-CPU offline runtime
- **THEN** all 512 SHALL overlap without counting queued work as active, preserve content and usage, and complete without local-capacity errors
- **AND** measured memory, CPU, latency and workload limitations SHALL be reported without claiming production capacity for arbitrary contexts
