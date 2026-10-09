## MODIFIED Requirements

### Requirement: Standby socket capacity does not multiply retained large input bodies
The runtime SHALL bound large incoming WebSocket messages across reading, queued and executing responses at twice the configured active-plus-queued response capacity (1280 by default), allowing a current and one staged next message per admitted request. A message larger than 4096 bytes MUST acquire a body permit before retaining data beyond its small prefix and retain it until handled or discarded. Waiting on an empty socket and receiving a short control frame MUST NOT acquire such a permit. The existing 32 MiB message limit SHALL remain enforced. Rejection, validation failure, completion, cancellation, disconnect and queue cleanup MUST release owned body permits exactly once. Socket expansion MUST NOT add a smaller shared payload-byte ceiling that rejects otherwise admitted requests.

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

The default global active-stream bound SHALL support 512 concurrent requests,
retain a bounded 128-request queue and its 15-second timeout, and preserve all
per-account recovery/create/stream and configured source caps. Raising the
global bound MUST NOT authorize scope bypass or weaken cancellation cleanup.
The production HTTP transport MUST allow at least 512 concurrent connections to
one upstream host, so its HTTP/1.1 connection queue does not reduce this bound.
The upstream WebSocket session store SHALL support 512 independent live sessions
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
