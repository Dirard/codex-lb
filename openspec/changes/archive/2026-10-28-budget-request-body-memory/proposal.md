## Why

Request-body admission counts requests, but memory is what large contexts exhaust. 128 HTTP body readers admit 128 × 32 MiB and are released right after reading although the body stays in memory until the request ends; Chat Completions, embeddings, compact (32 MiB), transcription (25 MB audio), control and file routes read with no bound at all (audit NET-14, SEC-4). A burst of auto-compactions or large contexts can therefore exceed the host's memory long before any count is reached, and one key can take all of it.

## What Changes

- One request-body budget counted in bytes, shared by every proxy route that reads a body (Responses HTTP and WebSocket, Chat Completions, embeddings, compact, control, files, transcription, realtime call creation, public warmup). A declared Content-Length is charged before reading; chunked, gzip and WebSocket bodies are charged while they are read. The charge is held until the request finishes using the body and released exactly once on every path.
- The budget is one eighth of the sizing memory (at least 64 MiB; 128 MiB on a 1 GB host, ~486 MiB on a 4 GB host) with a per-API-key share of 50% by default. `--body-memory-budget` and `--body-memory-key-share` override them.
- Exhaustion returns the existing 503 `local_capacity_exceeded` envelope with Retry-After (on a WebSocket: an error frame for that message only); it is never provider quota.
- The count-based HTTP body readers and WebSocket large-body permits are removed. Short WebSocket controls stay free.
- `GET /api/runtime/capacity`, the dashboard capacity card and Prometheus show the budget, retained bytes and rejections.

## Capabilities

### Modified Capabilities

- `go-runtime`: body admission becomes byte-weighted on every proxy route; the large-body WebSocket requirement drops its count and its prohibition of a shared payload-byte ceiling; capacity sizing derives the body budget instead of body readers.

## Impact

`internal/domain` capacity plan, `internal/adapters/httpapi` (Responses, WebSocket frames, native API, operations, capacity route), `cmd/codex-lb` flags, wiring and gauges, dashboard capacity card. The admin capacity response replaces `limits.bodyReaders` with `limits.bodyBudgetBytes`/`bodyBudgetKeyBytes` and adds `usage.bodyBytes`/`bodyRejections`. No schema or update-protocol change. Requests that previously fit only because memory was unbounded may now receive 503 with Retry-After under a burst.
