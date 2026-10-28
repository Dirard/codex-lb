# Request-body memory budget: context

## Why bytes, not counts

The count-based guards bounded how many bodies were *being read*, not how many bytes stayed in memory: 128 HTTP readers were released right after reading while the body lived until the response ended, and 1280 WebSocket permits each allowed up to 32 MiB. Compact (32 MiB), Chat Completions, embeddings and transcription (25 MB audio) had no guard at all (audit NET-14, SEC-4). A burst of auto-compactions or large contexts therefore grew the heap until the host ran out of memory.

## How it works

- One `BodyBudget` is shared by the Responses (HTTP and WebSocket), native and operations handlers. A request's body is charged under its API key after authentication.
- A declared `Content-Length` is checked against the route maximum (413) and charged in full before any byte is read; the body is then read into one exactly sized buffer. Chunked bodies, gzip bodies (by decoded size) and WebSocket messages larger than 4 KiB are charged as bytes arrive, at most 64 KiB ahead of what was read. When nothing is free a one-byte probe distinguishes "the body has ended" (admitted exactly at the limit) from "more is coming" (refused).
- The charge is released when the handler returns (the body is referenced until the response ends, for failover, compaction and accounting); for WebSocket frames, when the turn ends or the frame is rejected. Release is idempotent.
- Refusal: 503 `local_capacity_exceeded` with `Retry-After: 2` on HTTP; an error frame for that message on a WebSocket, which stays open. Nothing is dispatched and account health is untouched.
- Sizing: one eighth of the sizing memory, at least 64 MiB (1 GiB → 128 MiB, 4 GB VPS → ~486 MiB). One key may hold 50% (at least one 32 MiB body). Keyless local traffic is one internal principal and is bounded only by the whole budget.

## Why one eighth

Measured peak RSS is about 6× the retained body bytes: roughly three live copies (request body, prepared payload, upstream encoding) doubled by the Go GC goal. A full budget of sizing/8 therefore drives RSS to about the 75% Go soft memory limit — the limit that already defines the process's memory envelope — and not past the host's memory. The budget counts body bytes; it is not a hard process-memory limit.

## Measurements (2026-10-28)

Synthetic and offline: real `codex-lb serve` binaries (before = go-v1.1.1 `d18a3257`, after = this change), a loopback Z.AI model source whose stub reads each body and holds it 5 s, 4 API keys, non-stream `POST /v1/responses`, the whole burst started at once on a 32-CPU workstation. Peak RSS is the server's VmHWM; idle RSS was 24 MiB for both.

| profile, burst | binary | admitted (200) | 503 `local_capacity_exceeded` | other failures | peak RSS |
|---|---|---|---|---|---|
| 1 GiB (`--memory-limit 768MiB`), 200 × 4 MiB | before | 200 | 0 | 0 | 2624 MiB |
| 1 GiB, 200 × 4 MiB | after | 32 | 168 | 0 | 816 MiB |
| 4 GiB (`--memory-limit 3GiB`), 400 × 4 MiB | before | 303 | 0 | 97 × 503 `usage_settlement_failed` | 5244 MiB |
| 4 GiB, 400 × 4 MiB | after | 128 | 272 | 0 | 3064 MiB |
| 1 GiB, 512 × 256 KiB (approved profile) | before | 512 | 0 | 0 | 769 MiB |
| 1 GiB, 512 × 256 KiB | after | 512 | 0 | 0 | 786 MiB |

These are synthetic results, not a production SLA. Real requests also carry history, tools and outputs whose memory is not charged.

## Operating notes

- `GET /api/runtime/capacity` reports `limits.bodyBudgetBytes`, `limits.bodyBudgetKeyBytes`, `usage.bodyBytes` and `usage.bodyRejections` (`limits.bodyReaders` is gone). Prometheus: `codex_lb_request_body_bytes`, `codex_lb_request_body_budget_bytes`, `codex_lb_request_body_rejections`.
- Frequent rejections with RSS well below the soft limit mean the budget is too small for the workload: raise `--body-memory-budget` (e.g. `1GiB`) or `--memory-limit`. One noisy key: lower `--body-memory-key-share`. A single-key installation can use `--body-memory-key-share 100`.
- Realtime live WebSocket messages (4 MiB, relayed one at a time) are not charged yet; they belong with the shared realtime socket budget (NET-22).
