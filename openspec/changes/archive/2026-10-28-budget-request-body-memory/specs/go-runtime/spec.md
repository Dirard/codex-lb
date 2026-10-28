## ADDED Requirements

### Requirement: Request bodies share a byte-weighted memory budget

Every proxy route that reads a request body — Responses over HTTP and WebSocket, Chat Completions, embeddings, compact, Codex control, file creation, transcription, realtime call creation and public warmup — SHALL charge that body to one request-body budget counted in bytes and shared by all routes, under the authenticated key, before retaining it. A declared Content-Length above the route maximum SHALL be rejected with 413 before charging; a declared length SHALL be charged in full before any byte is read; a body without a declared length, a gzip body whose decoded size exceeds its declared length, and a WebSocket message SHALL be charged as bytes are read, so that no retained byte is uncharged. The charge SHALL be held until the request finishes using the body and MUST be released exactly once on success, validation failure, upstream failure, client cancellation, disconnect and mid-read rejection.

The budget SHALL default to one eighth of the sizing memory and never less than 64 MiB (128 MiB on a 1 GB-class host). One API key SHALL hold at most its share of the budget (50% by default, never less than the smaller of the budget and the 32 MiB maximum body); keyless local traffic is one internal principal bounded only by the whole budget. `--body-memory-budget <size>` (at least 32 MiB) and `--body-memory-key-share <percent>` (1–100, where 100 disables the per-key bound) SHALL override the defaults; invalid values MUST fail startup naming the flag.

When the budget or the key's share cannot hold a body, the runtime SHALL return the existing 503 `local_capacity_exceeded` envelope with Retry-After without dispatching upstream, reserving key usage or changing account health. This local pressure MUST NOT be classified as provider quota or authorize failover. The budget counts request-body bytes; parsing, prepared payloads, history and outputs have additional workload-dependent costs, so it MUST NOT be presented as a hard process-memory limit. `GET /api/runtime/capacity` SHALL report the budget, the per-key bound, the retained bytes and the rejection count, and the dashboard capacity card SHALL show retained bytes against the budget.

#### Scenario: A burst of large bodies exceeds the budget
- **WHEN** clients send more concurrent large compact or Responses bodies than the budget can hold
- **THEN** the bodies that fit are processed normally
- **AND** each excess request receives 503 `local_capacity_exceeded` with Retry-After before any upstream dispatch
- **AND** once the requests finish the retained body bytes return to zero

#### Scenario: A chunked or compressed body grows past the free budget
- **WHEN** a body without a declared length, or a gzip body, decodes to more bytes than the budget has free
- **THEN** reading stops at the budget, the request receives 503 `local_capacity_exceeded` and its partial charge is released

#### Scenario: Admitted requests fail independently
- **WHEN** one admitted request fails upstream, another is cancelled by its client and a third fails validation while a fourth is still streaming
- **THEN** the first three release their charges exactly once and the fourth keeps its own charge until it ends

#### Scenario: One key reaches its share
- **WHEN** one API key holds its share of the budget and sends another large body
- **THEN** that request receives 503 `local_capacity_exceeded`
- **AND** a request from another key that fits the remaining budget is served

#### Scenario: WebSocket and HTTP bodies share the budget
- **WHEN** HTTP bodies hold the budget and a client sends a large response.create on an admitted WebSocket
- **THEN** only that message receives local capacity and the socket stays usable

## MODIFIED Requirements

### Requirement: Capacity defaults follow the detected host

At startup the runtime SHALL detect the CPUs (effective GOMAXPROCS), the memory available to the process (the smallest of the cgroup v2/v1 limit along its cgroup path and physical memory, assuming 1 GiB when unknown) and the descriptor limit, and SHALL derive its bounds from that profile. Memory tiers SHALL be: below 768 MiB — 256 generations and 2048 client WebSockets; below 1.75 GiB — 512 and 4096; below 3.5 GiB — 1024 and 8192; below 7 GiB — 2048 and 16384; otherwise 4096 and 32768. The queue SHALL be a quarter of the generation bound (64…1024); the request-body budget SHALL be one eighth of the sizing memory (at least 64 MiB) with a 50% per-key share; upstream WebSocket sessions and connections per upstream host SHALL equal the generation bound; SQLite readers SHALL be 2×CPU+4 within 8…32. Client WebSockets SHALL be reduced so that sockets, twice the generation bound and 256 reserved descriptors fit the descriptor limit, never below 64.

The Go soft memory limit SHALL default to 75% of available memory. An exported GOMEMLIMIT SHALL be kept and used for sizing; `--memory-limit off` SHALL leave the runtime limit unset and `--memory-limit <size>` SHALL set it and size from it. `--max-generations`, `--max-client-websockets`, `--body-memory-budget` and `--body-memory-key-share` SHALL override the derived values; invalid values MUST fail startup naming the flag. The runtime SHALL log the detected profile and effective limits before serving, and `GET /api/runtime/capacity` (administrator only) SHALL return them with the overridden limits and current usage (active and queued generations, client WebSockets, retained request-body bytes and body rejections, Go heap, process RSS). Host-derived bounds MUST NOT bypass per-account, per-key or source caps.

#### Scenario: The service moves from a 1 GB to a 4 GB host
- **WHEN** the same binary starts on a 6-CPU host whose process may use 3.8 GiB
- **THEN** it admits up to 2048 generations with a 512-request queue, 16384 client WebSockets, a ~486 MiB request-body budget and 16 SQLite readers, and sets a 75% Go soft memory limit
- **AND** on a 1 GB host it keeps 512 generations, a 128-request queue and 4096 client WebSockets with a 128 MiB request-body budget

#### Scenario: A container or systemd unit limits memory
- **WHEN** the process runs under `MemoryMax=900M` on a larger machine
- **THEN** sizing uses the 900 MiB cgroup limit and reports `cgroup_v2` as its source

#### Scenario: The operator overrides capacity
- **WHEN** the operator starts with `--max-generations 300 --memory-limit 2GiB --body-memory-budget 1GiB`
- **THEN** 300 generations, a 75-request queue, a 2 GiB Go soft limit and a 1 GiB request-body budget are in effect and the capacity endpoint lists the three overrides

### Requirement: Idle client WebSockets do not consume HTTP body-reader capacity
The runtime SHALL admit up to the host-sized client WebSocket budget (4096 on a 1 GB-class host) without adding a lower per-key socket ceiling, independently of the request-body budget, active-generation and upstream-socket admission. Empty client sockets MUST NOT dispatch upstream, reserve generation/key usage or charge the request-body budget. The global connection slot MUST be acquired after authentication and released on every failed upgrade, disconnect and drain. Actual socket exhaustion SHALL return the existing 503/local_capacity_exceeded envelope and Retry-After. The existing 120-second idle timeout and per-request authorization SHALL remain in force.

#### Scenario: HTTP remains usable beside idle subagent connections
- **WHEN** more than 256 authenticated client WebSockets are idle within the global budget
- **THEN** an eligible HTTP request and a response.create on an admitted WebSocket SHALL still execute under independent generation admission
- **AND** opening those idle sockets SHALL NOT create upstream work, usage reservations or request-body charges

#### Scenario: One key uses the available global pool
- **WHEN** a valid key opens connections while the global socket budget has room
- **THEN** its connections SHALL NOT be refused by a lower per-key socket ceiling

#### Scenario: Socket admission is released
- **WHEN** an upgrade fails, a client disconnects or the runtime drains
- **THEN** its global connection slot SHALL be released without changing active generation or continuation ownership

### Requirement: Standby socket capacity does not multiply retained large input bodies
Large incoming WebSocket messages across reading, queued and executing responses SHALL be bounded by the shared request-body budget in bytes, allowing a current and one staged next message per socket. A message larger than 4096 bytes MUST be charged to that budget, under the socket's key, before retaining data beyond its small prefix and keep the charge until handled or discarded; data read beyond the prefix is charged as it arrives. Waiting on an empty socket and receiving a short control frame (cancel, interrupt) MUST NOT be charged. The existing 32 MiB message limit SHALL remain enforced. Rejection, validation failure, completion, cancellation, disconnect and queue cleanup MUST release owned charges exactly once.

When the budget cannot hold a message, the runtime SHALL discard only the excess message with bounded scratch space under the existing message-size validation, return local capacity for that message, and keep the connection usable. It MUST NOT cancel the connection's active response, replay upstream work, or reclassify pressure as provider quota. The byte budget MUST NOT be presented as a hard process-memory limit.

#### Scenario: Short cancellation remains available at the large-body ceiling
- **WHEN** the request-body budget is full and an active client sends a short response.cancel
- **THEN** cancellation SHALL still be processed and release the active request's charge

#### Scenario: Excess input does not abort admitted work
- **WHEN** an active client sends another large message while the budget cannot hold it
- **THEN** only that excess message SHALL receive local capacity without being retained or dispatched
- **AND** the same connection's active request and subsequent short cancellation SHALL remain usable

#### Scenario: A queued frame outlives the client
- **WHEN** a client disconnects with a queued or partially read large request or an upgrade fails
- **THEN** cleanup SHALL leave no leaked connection slots or body charges
