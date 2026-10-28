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
 - description: overrides win and are listed; a 10% share is floored at 32 MiB; 100% equals the budget
   requirements: R1, R2
 - description: flags reject a budget below 32 MiB and shares outside 1–100
   requirements: R2

### 2. Admit a request body against the shared budget
authenticated proxy request --charge the declared length or charge bytes while reading--> [admitted body held by a lease, 503 local_capacity_exceeded, 413 request_too_large] --finish using the body--> released lease

Requirements:

  R4:
Every proxy route that reads a request body (Responses HTTP, Chat Completions, embeddings, compact, control, file creation, transcription, realtime call creation, public warmup) SHALL charge it to one budget shared by all routes before retaining it, under the authenticated key.

  R5:
When the body has a declared Content-Length, the route SHALL reject a length above the route maximum with 413 before charging and SHALL charge the full length before reading any byte; a body without a declared length, or whose decoded (gzip) size exceeds the declared length, SHALL be charged as bytes are read, never retaining a byte that is not charged.

  R6:
When the budget (or the key's share) cannot hold the body, the route SHALL respond with the existing 503 `local_capacity_exceeded` envelope and `Retry-After`, without dispatching upstream, reserving key usage or changing account health; this local pressure MUST NOT be classified as provider quota.

  R7:
The charge SHALL be held until the request finishes using the body (the handler returns) and released exactly once on every path: success, validation failure, upstream error, client cancellation, mid-read rejection and panic-free early return.

  R8:
The per-key share SHALL bound the bytes one API key holds; the internal local-proxy principal (keyless local traffic) SHALL be bounded only by the global budget.

  R9:
The budget counts request-body bytes, not total process memory, and MUST NOT be presented as a hard process-memory limit.

Detailed Workflow:
declared length: request with Content-Length --reject above route maximum--> bounded length --charge the whole length at once--> prepaid lease --read into one exactly sized buffer--> body
unknown length or gzip: request --wrap the decoded reader in a charging reader--> charging reader --charge each read before it is returned, refunding unread bytes--> body or local capacity

Implementation Logic:
1. `BodyBudget` (httpapi) holds `limit`, `keyLimit`, `used`, per-key `keys` map (only when `keyLimit < limit`), and a rejection counter under one mutex; it never blocks.
2. `bodyLease` belongs to one request and key: `grow(n, all)` grants up to `n` bytes (all-or-nothing when `all`), `refund(n)` returns unused grant, `release()` returns everything and closes the lease; `release` is idempotent so deferred and explicit releases cannot double-free, and a closed lease grants nothing.
3. `chargedReader` first consumes the prepaid declared length, then grants up to 64 KiB per read; when nothing can be granted it probes one byte: end of body admits the body exactly at the limit, any further byte is refused. Charged bytes therefore always cover the retained body.
4. `readProxyBody` (Responses, Chat, embeddings): encoding check (identity, gzip, else 415), 413 above 32 MiB declared, prepay declared length, identity with declared length reads with `io.ReadFull` into an exactly sized buffer (removes the `io.ReadAll` doubling, NET-24), otherwise `io.ReadAll` through the charging reader over the decoded stream.
5. `readOperationBody` (operations ignore Content-Encoding) and `parseTranscription` (multipart over the charging reader) use the same prepaid/charged rules; capacity errors are recognized before generic decode errors.
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
 - description: partial failure — an upstream error, a client cancellation and a validation failure on admitted requests all release their charge exactly once while a concurrent admitted request keeps its own
   requirements: R7
 - description: Chat Completions and transcription bodies are charged and refused at the limit
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
 - description: runtime wiring shares one budget between Responses, native and operations routes
   requirements: R13
 - description: dashboard card renders the body-memory row and override names
   requirements: R14

## Implementation checklist

1. [-] Solution spec and OpenSpec change `budget-request-body-memory` (proposal, tasks, delta amending the three requirements, new requirement).
2. [ ] Domain plan: `MaxRequestBodyBytes`, `BodyBudgetBytes`, `BodyBudgetKeyBytes`, overrides, remove `BodyReaders`; table tests (R1–R3).
3. [ ] `BodyBudget`, `bodyLease`, `chargedReader` with unit tests (R5, R7–R9).
4. [ ] Responses HTTP and native routes on the budget; identity bodies read into an exact buffer (R4–R7).
5. [ ] Operations and public warmup routes, transcription multipart on the budget (R4–R7).
6. [ ] WebSocket frames charged by bytes; remove `websocketBodies`; rewrite frame tests (R10–R12).
7. [ ] Flags, plan wiring, startup log, capacity API, gauges (R2, R13, R14).
8. [ ] Dashboard schema, card row, mocks, locales and test (R14).
9. [ ] Route-level tests: compact burst, chunked/gzip mid-read refusal, partial failure release, per-key share, shared WebSocket/HTTP budget, Chat and transcription (R4–R8, R10).
10. [ ] Measure before/after peak RSS for a burst of large bodies with the release binary and a loopback source; record in the decision log and OpenSpec context.
11. [ ] `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`; sync delta into `openspec/specs/go-runtime/`, archive the change, amend `specs/adaptive-capacity.md`.

Deferred:
- Realtime live WebSocket messages (4 MiB, relayed one at a time) stay outside the budget until realtime sockets join a shared socket budget (NET-22).

## Open questions

None blocking.

## Decision log

- 2026-10-28: One eighth of sizing memory: the 1 GB host keeps the measured 512 × 256 KiB contexts (128 MiB) inside the budget, and with the observed 4–5× retention per body byte (prepared payload, upstream encoding, transport buffers) a full budget stays under the 75% Go soft limit. The 64 MiB floor keeps two maximum-size bodies admissible on small hosts.
- 2026-10-28: Per-key share defaults to 50% and is a flag, not a runtime setting: it needs no schema change, is visible in the capacity plan, and 100 disables it. Keyless local traffic (one internal principal for all local clients) is exempt, as in stream fair share.
- 2026-10-28: Rejection instead of waiting: the existing `local_capacity_exceeded` + `Retry-After` contract already tells clients to retry; waiting would hold connections and bodies outside the budget.
- 2026-10-28: The charge is held until the handler returns, not until the body is read: the body stays referenced for failover, compaction and accounting until the request ends, and the WebSocket path already held permits until the turn ended.
- 2026-10-28: Charging bytes read, not buffer capacity: the declared-length path allocates exactly; growth slack on chunked/gzip/WebSocket reads is part of the documented per-byte multiplier.
- 2026-10-28: Realtime live messages deferred with NET-22 (see Deferred).
