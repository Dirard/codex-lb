# Request-body memory budget: one byte-weighted budget for every proxy route

- Last edited with skill pack: `0.2.2`

## Title and scope

Request bodies on every proxy route (Responses HTTP and WebSocket, Chat Completions, embeddings, compact, control, files, transcription, realtime call creation, public warmup) are admitted against one shared memory budget counted in bytes and sized from the host, instead of the count-based HTTP body readers and WebSocket large-body permits that left compact, native and transcription bodies unbounded (audit NET-14, SEC-4). An optional per-API-key share keeps one key from taking the whole budget, and the budget with its live usage is shown in `GET /api/runtime/capacity` and the dashboard capacity card.

## Planning anchor

- Entrypoints: `internal/adapters/httpapi/proxy.go#ProxyHandler.responses` (`reading` count semaphore around `readProxyBody`), `internal/adapters/httpapi/proxy_websocket_frame.go#readWebSocketFrame` (`websocketBodies` count permits), `internal/adapters/httpapi/native_api.go#chat`/`#embeddings`, `internal/adapters/httpapi/codex_operations.go#compact`/`#control`/`#createFile`/`#transcribeBackend`/`#transcribeV1`/`#createRealtimeCall`, `internal/adapters/httpapi/public_warmup.go#publicWarmup` (no budget at all).
- Changed assumption: memory, not request count, is what large contexts exhaust. A count of 128 readers admits 128 × 32 MiB; the count is released right after reading although the body stays in memory until the request ends; compact (32 MiB), Chat (32 MiB) and transcription (25 MB audio) read without any bound. The go-runtime requirement «Standby socket capacity does not multiply retained large input bodies» explicitly forbids a shared payload-byte ceiling; this slice amends it deliberately.
- Non-local: domain capacity plan, composition root and flags, three HTTP handlers and the WebSocket frame reader, admin capacity API, Prometheus gauges, dashboard card and locales, OpenSpec `go-runtime`.
- Impacted specs in `specs/`: `specs/adaptive-capacity.md` — action `amend` (R5 body readers replaced by the body budget; decision log points here). `specs/upstream-transport-reliability.md` and `specs/security-hardening.md` list NET-14/SEC-4 as deferred — action `amend` (decision log entry). OpenSpec change `budget-request-body-memory` amends «Capacity defaults follow the detected host», «Idle client WebSockets do not consume HTTP body-reader capacity», «Standby socket capacity does not multiply retained large input bodies» and adds «Request bodies share a byte-weighted memory budget».

## Connected groups or observed existing logic

### Observed Existing Logic

1. Entry and orchestration: every proxy route authenticates the key first (`authenticateProxyKey` / `authenticateBearerKey`), then reads the whole body into memory, then calls the application service; the body slice stays referenced until the handler returns (streams keep it for failover and compaction).
2. HTTP Responses: `reading` (`BodyReaders`, a quarter of the generation bound, ≥64) is acquired with a non-blocking send, released immediately after `readProxyBody`; exhaustion returns 503 `local_capacity_exceeded` with `Retry-After: 2` through `writeProxyError`. `readProxyBody` supports identity and gzip, limits to 32 MiB (413 `request_too_large`) and reads with `io.ReadAll` (≈2× allocation, NET-24).
3. WebSocket: `readWebSocketFrame` reads a 4096-byte prefix free of charge; a larger message takes one of `2 × AdmissionCapacity()` permits (1280 on 1 GB), otherwise discards only that message and reports local capacity on the open socket; the permit moves with the frame into the queue/active turn and is released after the turn, on validation failure, cancellation, disconnect and queue cleanup.
4. Native API: `chat` and `embeddings` call `readProxyBody` without any admission.
5. Operations: `readOperationJSON`/`readOperationBody` (compact 32 MiB, control 2 MiB, file create 64 KiB, realtime call 1 MiB, public warmup 1 KiB) and `parseTranscription` (multipart up to 32 MiB, audio up to 25 MB) read without admission; operations ignore Content-Encoding.
6. Realtime live WebSocket messages (`websocketConnection.Read`, 4 MiB each) are relayed one at a time; realtime sockets themselves are outside the client socket budget (NET-22, deferred).
7. Capacity plan: `domain.PlanCapacity` sizes generations, queue, `BodyReaders`, sockets and pools from memory tiers; `cmd/codex-lb/capacity.go` logs it; `GET /api/runtime/capacity` returns limits including `bodyReaders`; the dashboard card does not show body readers.
8. Tests: `proxy_websocket_frame_test.go`, `websocket_admission_test.go`, `runtime_capacity_test.go` and `capacity_test.go` reference the count semaphores directly.

## Use cases

### 1. Size the body budget from the host plan
host resources and overrides --derive the body budget and per-key share--> capacity plan with body budget

Requirements:

  R1:
The plan SHALL set `BodyBudgetBytes` to one eighth of the sizing memory, never below 64 MiB (twice the 32 MiB maximum request body), and `BodyBudgetKeyBytes` to the per-key share of it (default 50%), never below the smaller of the budget and 32 MiB so an idle key can always send one maximum-size body.

  R2:
`--body-memory-budget <size>` (at least 32 MiB) and `--body-memory-key-share <percent>` (1–100; 100 disables the per-key bound) SHALL override the derived values and be listed as `body_memory_budget` and `body_memory_key_share` in the overridden limits; invalid values MUST fail startup naming the flag.

  R3:
The count-based `BodyReaders` limit SHALL be removed from the plan, the startup log and the capacity API.

Data:
budget examples (default 50% share):

| sizing memory | body budget | per-key bound |
|---|---|---|
| 512 MiB | 64 MiB | 32 MiB |
| 1 GiB (assumed) | 128 MiB | 64 MiB |
| 3.8 GiB (4 GB VPS) | 486 MiB | 243 MiB |
| 92 GiB | 11.5 GiB | 5.75 GiB |

Files And Functions:
 - existing: internal/domain/capacity.go#PlanCapacity - derive `BodyBudgetBytes`, `BodyBudgetKeyBytes`; drop `BodyReaders`
 - planned: internal/domain/capacity.go#MaxRequestBodyBytes - 32 MiB, shared by readers and the floors
 - existing: cmd/codex-lb/config.go#parseConfig - new flags
 - existing: cmd/codex-lb/capacity.go#planCapacity, #applyCapacity - pass overrides, log budget

Tests:
 - description: each tier and the assumed 1 GiB host get sizing/8 with the 64 MiB floor and a 50% key share
   requirements: R1
 - description: overrides win and are listed; a 5% share is floored at 32 MiB; 100% equals the budget
   requirements: R1, R2
 - description: flags parse `512MiB` and `25%`, reject a budget below 32 MiB and shares outside 1–100, and the plan lists both overrides
   requirements: R2

### 2. Admit a request body against the shared budget
authenticated proxy request --check the declared length and charge bytes as they arrive--> [admitted body held by a lease, 503 local_capacity_exceeded, 413 request_too_large, 408 request_body_timeout] --finish using the body--> released lease

Requirements:

  R4:
Every proxy route that reads a request body (Responses HTTP, Chat Completions, embeddings, compact, control, file creation, transcription, realtime call creation, public warmup) SHALL charge it to one budget shared by all routes before retaining it, under the authenticated key.

  R5:
When the body has a declared Content-Length, the route SHALL reject a length above the route maximum with 413 and refuse a length the key cannot charge now with 503, both before reading, without reserving it; every body (gzip by decoded bytes) SHALL be charged as its bytes arrive, never retaining a byte that is not charged, so a request that declares a length and sends nothing holds nothing. Past the route maximum the body SHALL get 413 even on a full budget: the byte that proves it is read without charge.

  R6:
When the budget (or the key's share) cannot hold the body, the route SHALL respond with the existing 503 `local_capacity_exceeded` envelope and `Retry-After`, without dispatching upstream, reserving key usage or changing account health; this local pressure MUST NOT be classified as provider quota.

  R7:
The charge SHALL be held until the request finishes using the body (the handler returns) and released exactly once on every path: success, validation failure, upstream error, client cancellation, mid-read rejection and panic-free early return.

  R8:
The per-key share SHALL bound the bytes one API key holds; the internal local-proxy principal (keyless local traffic) SHALL be bounded only by the global budget.

  R9:
The budget counts request-body bytes, not total process memory, and MUST NOT be presented as a hard process-memory limit.

  R15:
A request body SHALL keep arriving: each read that returns data moves the connection read deadline 10 s ahead, never past 2 minutes from the start of the body (the server ReadTimeout); a stalled upload SHALL end with 408 `request_body_timeout` and release its charge, and the deadline MUST NOT outlive the body into the response.

Detailed Workflow:
declared length: request with Content-Length --reject above route maximum or beyond the free budget--> checked length --read through the progress and charging readers--> body
any body: request --watch read progress on the connection--> progress reader --charge each read before it is returned, refunding unread bytes, up to the route maximum--> [body, local capacity, too large, body timeout]

Implementation Logic:
1. `BodyBudget` (httpapi) holds `limit`, `keyLimit`, `used`, per-key `keys` map (only when `keyLimit < limit`), and a rejection counter under one mutex; it never blocks.
2. `bodyLease` belongs to one request and key: `grow(n, all)` grants up to `n` bytes (all-or-nothing when `all`), `refund(n)` returns unused grant, `release()` returns everything and closes the lease; `release` is idempotent so deferred and explicit releases cannot double-free, and a closed lease grants nothing.
3. `chargedReader` grants up to 64 KiB per read and refunds what the read did not return; when nothing can be granted it probes one byte: end of body admits the body exactly at the limit, any further byte is refused. It returns at most the route maximum; one more byte, read without charge, is 413. Its failure is sticky, so one body counts one refusal. Charged bytes therefore always cover the retained body (a blocked read holds at most its pending grant).
4. `bodyLease.admit` checks a declared length (413 above the maximum, 503 beyond what the key may still charge) without reserving; `bodyLease.open` wraps `MaxBytesReader(r.Body)` in `progressReader`, which moves the connection read deadline 10 s ahead after every read that returns data (capped at 2 minutes from the start) and maps the deadline to 408 `request_body_timeout`; the deadline is not touched for empty bodies or after the body ends.
5. `readProxyBody` (Responses, Chat, embeddings: identity or gzip, else 415), `readOperationBody` (operations do not decode Content-Encoding) and `parseTranscription` (multipart over the charging reader) all read through `open` and the charging reader; local errors (refusal, too large, timeout) are recognized before generic decode errors, and an invalid transcription field is not drained.
6. Handlers create the lease after authentication and `defer lease.release()`.

Files And Functions:
 - planned: internal/adapters/httpapi/body_budget.go#BodyBudget, #bodyLease, #chargedReader, #NewBodyBudget, #Usage
 - existing: internal/adapters/httpapi/proxy.go#responses, #readProxyBody - lease instead of `reading`
 - existing: internal/adapters/httpapi/native_api.go#chat, #embeddings
 - existing: internal/adapters/httpapi/codex_operations.go#compact, #control, #createFile, #transcribeBackend, #transcribeV1, #createRealtimeCall, #readOperationJSON, #readOperationBody, #parseTranscription
 - existing: internal/adapters/httpapi/public_warmup.go#publicWarmup

Tests:
internal/adapters/httpapi/body_budget_test.go
 - description: lease grow/refund/release keeps used exact; release twice returns bytes once; per-key share refuses one key while another is admitted; local principal is exempt from the share
   requirements: R7, R8
 - description: compact burst on the route — bodies that fit are processed, the excess gets 503 local_capacity_exceeded with Retry-After before upstream; budget returns to zero (NET-14 regression)
   requirements: R4, R5, R6, R7
 - description: chunked and gzip Responses bodies charged while reading; a body larger than the free budget is refused mid-read and its partial charge released
   requirements: R5, R6, R7
 - description: review finding 1 — four connections from two keys that declare 32 MiB and send nothing hold no budget, and requests from both keys are served
   requirements: R5
 - description: a stalled upload ends with 408 and releases its charge at the idle deadline; a slow but progressing upload is served and its response outlives the deadline (internal route test with a 200 ms idle)
   requirements: R7, R15
 - description: review finding 2 — on the 64 MiB/32 MiB-per-key plan, oversized gzip (declared and chunked), identity chunked and chunked transcription audio get 413 without Retry-After and no refusal count
   requirements: R5, R6
 - description: unit — exact fit, sticky refusal counted once, the route maximum reports 413 before the budget, a declared length is checked but not reserved
   requirements: R5, R7
 - description: partial failure — an upstream error, a client cancellation and a validation failure on admitted requests all release their charge exactly once while a concurrent admitted request keeps its own
   requirements: R7
 - description: Chat Completions and transcription (declared and chunked multipart) bodies are charged and refused at the limit
   requirements: R4, R6
 - description: per-key share on the route — one key at its share gets 503 while another key's request is served
   requirements: R6, R8

### 3. Charge large WebSocket messages by bytes
large client WebSocket message --charge the prefix and then each read to the shared budget--> [queued or active turn holding its charge, discarded excess message with local capacity] --turn ends, frame rejected or socket closes--> released charge

Requirements:

  R10:
A Responses WebSocket message larger than 4096 bytes SHALL be charged to the shared byte budget (under the socket's key) before retaining data beyond its small prefix and SHALL keep the charge until handled or discarded; waiting on an empty socket and short control frames (cancel, interrupt) MUST NOT be charged.

  R11:
When the budget cannot hold the message, the runtime SHALL discard only that message under the 32 MiB message limit, report local capacity on the open socket and keep the connection and its active turn usable; release on rejection, validation failure, completion, cancellation, disconnect and queue cleanup MUST happen exactly once.

  R12:
The count-based `websocketBodies` permits (twice the admission capacity) SHALL be removed; staged and current messages are bounded by bytes, and per-socket staging (one queued frame) is unchanged.

Files And Functions:
 - existing: internal/adapters/httpapi/proxy_websocket_frame.go#readWebSocketFrame - lease and charging reader; key parameter
 - existing: internal/adapters/httpapi/proxy_websocket.go#websocket - pass key ID

Tests:
internal/adapters/httpapi/proxy_websocket_frame_test.go
 - description: existing permit tests rewritten for bytes: active body charged, cancel releases, invalid frames release, queued frames released on overflow and disconnect, current plus staged complete, discard keeps control readable, partial frame disconnect releases, input pressure keeps active turn, discard keeps message size limit
   requirements: R10, R11, R12
 - description: a WebSocket message and an HTTP body share one budget — a held HTTP body makes a large WebSocket message receive local capacity
   requirements: R10

### 4. Show the budget to the operator
capacity plan and live budget usage --serve limits and usage--> capacity API and dashboard card

Requirements:

  R13:
`GET /api/runtime/capacity` SHALL return `limits.bodyBudgetBytes`, `limits.bodyBudgetKeyBytes`, `usage.bodyBytes` and `usage.bodyRejections`; `bodyReaders` is removed.

  R14:
The dashboard capacity card SHALL show retained request-body memory against the budget, and the Prometheus endpoint SHALL export the retained bytes, the budget and the rejection count.

Files And Functions:
 - existing: internal/adapters/httpapi/runtime_capacity.go - fields
 - existing: cmd/codex-lb/runtime.go#openRuntime - one shared `BodyBudget` wired into the Responses, native and operations handlers and the usage callback
 - existing: cmd/codex-lb/metrics.go#registerGauges - `codex_lb_request_body_bytes`, `codex_lb_request_body_budget_bytes`, `codex_lb_request_body_rejections`
 - existing: web/src/features/runtime/schemas.ts, runtime-capacity-settings.tsx, web/src/test/mocks/handlers.ts, web/src/i18n/locales/*.json

Tests:
 - description: capacity route returns the budget and live usage for administrators
   requirements: R13
 - description: runtime wiring shares one budget — a Responses body that has arrived except its last byte refuses compact, Chat Completions and transcription bodies until its client disconnects (cmd/codex-lb/capacity_test.go)
   requirements: R13
 - description: dashboard card renders the body-memory row and override names
   requirements: R14

## Implementation checklist

1. [x] Solution spec and OpenSpec change `budget-request-body-memory` (proposal, tasks, delta amending the three requirements, new requirement).
2. [x] Domain plan: `MaxRequestBodyBytes`, `BodyBudgetBytes`, `BodyBudgetKeyBytes`, overrides, remove `BodyReaders`; table tests (R1–R3).
3. [x] `BodyBudget`, `bodyLease`, `chargedReader` with unit tests (R5, R7–R9).
4. [x] Responses HTTP and native routes on the budget; identity bodies read into an exact buffer (R4–R7).
5. [x] Operations and public warmup routes, transcription multipart on the budget (R4–R7).
6. [x] WebSocket frames charged by bytes; remove `websocketBodies`; rewrite frame tests (R10–R12).
7. [x] Flags, plan wiring, startup log, capacity API, gauges (R2, R13, R14).
8. [x] Dashboard schema, card row, mocks, locales and test (R14).
9. [x] Route-level tests: compact burst, chunked/gzip mid-read refusal, partial failure release, per-key share, shared WebSocket/HTTP budget, Chat and transcription (R4–R8, R10).
10. [x] Measure before/after peak RSS for a burst of large bodies with the release binary and a loopback source; record in the decision log and OpenSpec context.
11. [x] `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`; sync delta into `openspec/specs/go-runtime/`, archive the change, amend `specs/adaptive-capacity.md`.

12. [x] Review fixes (OpenSpec change `bound-stalled-request-bodies`): declared lengths checked, not reserved; progress deadline with 408; 413 before 503 past the maximum; overflow-safe key share; requirement rename and 1 GB wording; regression tests; re-measured bursts.

Deferred:
- Realtime live WebSocket messages (4 MiB, relayed one at a time) stay outside the budget until realtime sockets join a shared socket budget (NET-22).
- WebSocket message buffers may have up to 1.25× their length in capacity; the charge counts length (part of the per-byte multiplier).
- The budget is per process; overlapping workers during a seamless update would each have a full budget (shared tally or halving when that mechanism lands).
- Handlers constructed without `ConfigureBodyBudget` keep a private 1 GB-host budget; making the budget a required argument is a follow-up.

## Open questions

None blocking.

## Decision log

- 2026-10-28: One eighth of sizing memory: the 1 GB host keeps the measured 512 × 256 KiB contexts (128 MiB) inside the budget, and the measured peak RSS is about 6× the retained body bytes (roughly three live copies — body, prepared payload, upstream encoding — doubled by the Go GC goal), so a full budget drives RSS to about the 75% Go soft limit and not past host memory. The 64 MiB floor keeps two maximum-size bodies admissible on small hosts.
- 2026-10-28: Per-key share defaults to 50% and is a flag, not a runtime setting: it needs no schema change, is visible in the capacity plan, and 100 disables it. Keyless local traffic (one internal principal for all local clients) is exempt, as in stream fair share.
- 2026-10-28: Rejection instead of waiting: the existing `local_capacity_exceeded` + `Retry-After` contract already tells clients to retry; waiting would hold connections and bodies outside the budget.
- 2026-10-28: The charge is held until the handler returns, not until the body is read: the body stays referenced for failover, compaction and accounting until the request ends, and the WebSocket path already held permits until the turn ended.
- 2026-10-28: Charging bytes read, not buffer capacity: growth slack on WebSocket reads is part of the documented per-byte multiplier.
- 2026-10-28: Realtime live messages deferred with NET-22 (see Deferred).
- 2026-10-28: Measured with the real binaries (before = go-v1.1.1 `d18a3257`, after = this branch), a loopback Z.AI source whose stub reads each body and holds it 5 s, 4 keys, non-stream `/v1/responses`, burst started at once, idle RSS 24 MiB, peak = VmHWM. 1 GiB profile (`--memory-limit 768MiB`), 200 × 4 MiB: before 200 admitted, 2624 MiB; after 32 admitted, 168 × 503 `local_capacity_exceeded`, 816 MiB. 4 GiB profile (`--memory-limit 3GiB`), 400 × 4 MiB: before 303 OK and 97 × 503 `usage_settlement_failed`, 5244 MiB; after 128 admitted, 272 refused, 3064 MiB. The approved 512 × 256 KiB profile on 1 GiB: 512/512 both, 769 → 786 MiB (noise). Synthetic workload, not a production SLA.
- 2026-10-28: Validation: `make check` (vet, golangci-lint, test, race, govulncheck, openspec), `make web-check`, `make web-deps build`, `make test-web-contract`. `specs/adaptive-capacity.md` R5 amended; NET-14/SEC-4 deferral notes in `specs/upstream-transport-reliability.md` and `specs/security-hardening.md` point here.
- 2026-10-28: Review (PR #6) fixes. (1) Charging a declared length at the headers let clients that send no body hold the budget until ReadTimeout (four connections on 1 GB); an idle-only deadline would still let reconnecting clients hold it, so declared lengths are now checked but not reserved and every body is charged as it arrives, with a 10 s no-progress deadline (2 min total, 408 `request_body_timeout`) bounding what a stalled client holds. Declared bodies use `io.ReadAll` instead of an exact buffer allocated at the headers, which would be uncharged memory. (2) The charged stream stops at the route maximum and probes one byte without charge, so oversized bodies get 413 rather than a retryable 503 on small shares. (3) The "approved 512 × 256 KiB profile fully admitted" claim holds only at exactly 1 GiB (as measured with `--memory-limit 768MiB`); a real 1 GB VPS (960–990 MiB) gives a 120–123 MiB budget and admits about 480–491 of the 512 at once — wording corrected rather than adding a tier floor, which would push a full budget past the soft limit on small hosts. Re-measured after the fix: 1 GiB 200 × 4 MiB — 31 admitted, 799 MiB; 4 GiB 400 × 4 MiB — 128 admitted, 3104 MiB; 512 × 256 KiB at 1 GiB — 512 admitted, 768 MiB.
- 2026-10-28: The requirement «Idle client WebSockets do not consume HTTP body-reader capacity» is renamed to «…request-body capacity». The budget is per process; seamless-update worker overlap would double the host worst case and is recorded as deferred.
