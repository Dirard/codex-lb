## ADDED Requirements

### Requirement: Concurrent policy reads do not queue behind unrelated durable writes

The file-backed Go SQLite runtime SHALL permit bounded concurrent read-only
committed snapshots while retaining a serialized durable writer. Read-check-write
admission, key-limit reservations, settlement, ownership fences and repricing
SHALL retain atomicity and exactly-once local accounting. Every opened connection
SHALL apply its required isolation and integrity settings, including reconnects.
Writer durability MUST remain WAL/FULL; no response or admission may be reported
durably recorded before its required commit. The change MUST NOT introduce stale
authorization caches, unbounded connection growth, hidden write retries or
fire-and-forget financial mutations.

#### Scenario: A writer is busy while another request checks committed policy
- **WHEN** an unrelated writer transaction has not committed
- **THEN** a pure read SHALL complete from a consistent committed snapshot without waiting for the writer connection
- **AND** it SHALL NOT expose uncommitted policy or bypass subsequent transactional reservation checks

#### Scenario: Connections are reopened or startup fails
- **WHEN** the read pool opens a new physical connection or any startup stage fails
- **THEN** each connection SHALL retain its required settings and partial resources SHALL be closed
- **AND** foreign databases and literal-path safety SHALL retain their previous refusal behavior

### Requirement: Grouped short writes retain independent financial outcomes

The runtime SHALL group only already waiting short SQLite mutations into bounded
transactions without delaying an idle write to collect more work. Each job
SHALL retain independent rollback isolation, while successful jobs MUST NOT be
acknowledged before the shared FULL commit succeeds. A caller cancellation MUST
NOT interrupt unrelated jobs or return an indeterminate reservation while its
callback continues mutating state. Failed savepoint control, callback panic or
outer commit failure SHALL fail the affected pending batch without hidden retry
or subsequent accidental autocommit. Closing the store SHALL stop new admissions
and finish accepted jobs before closing the database. Existing financial and
account/key/incarnation/route/outcome-sequence fences MUST remain unchanged.

#### Scenario: A batch contains a failed or cancelled job
- **WHEN** an isolated job fails before release and its savepoint can be rolled back
- **THEN** its mutation SHALL be discarded without undoing successful sibling jobs
- **AND** successful siblings SHALL wait for the shared durable commit before returning

#### Scenario: The batch cannot commit
- **WHEN** savepoint integrity is lost or the outer transaction commit fails
- **THEN** no pending successful job SHALL be acknowledged or retried automatically
- **AND** callers SHALL retain the existing fail-closed accounting/reconciliation behavior

### Requirement: Streaming optimization is verified under concurrent user load

Performance changes SHALL be compared against the preserved pre-change Go binary
on identical offline disk-backed workloads and durability settings, including
8, 32, 128 and 256 concurrent requests and multiple keys. Results SHALL report
first-token and completion p50/p95/p99, errors, resource consumption and
cancellation cleanup; failed requests MUST NOT be counted as fast successes.
Both native streaming Chat and Codex Responses SHALL preserve their current
output, usage, ownership and terminal contracts. Results for a synthetic local
provider MUST NOT be presented as a real-provider or production capacity SLA.
The target deployment is 1vCPU/1GB with a preferred128MiB service memory budget
and predominantly long-lived streams; higher memory use is authorized when needed
and SHALL be measured and reported. Verification SHALL include single-Go-processor
runs, peakRSS and larger request context rather than only tiny short requests.
At least256 active streams SHALL be tested without counting queued requests as
active. Additional concurrency MUST NOT weaken accounting/authentication.
Configuration/data files MAY be reorganized only
where measurement supports the change and recovery/data-safety contracts remain.

The default global active-stream bound SHALL support 256 concurrent requests,
retain a bounded 128-request queue and its 15-second timeout, and preserve all
per-account recovery/create/stream and configured source caps. Raising the
global bound MUST NOT authorize scope bypass or weaken cancellation cleanup.
The production HTTP transport MUST allow at least 256 concurrent connections to
one upstream host, so its HTTP/1.1 connection queue does not reduce this bound.
The upstream WebSocket session store SHALL support 256 independent live sessions
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
