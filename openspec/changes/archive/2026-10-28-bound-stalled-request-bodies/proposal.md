## Why

Review of `budget-request-body-memory` found that a declared Content-Length was charged in full when the headers arrived and held until the 2-minute server ReadTimeout. Four authenticated connections that sent headers and no body exhausted a 1 GB host's 128 MiB budget, so every body-carrying request got 503 until the timeout, and reconnecting kept it that way. Separately, a gzip or chunked body past the 32 MiB maximum received a retryable 503 instead of 413 whenever the key's free share was no larger than the maximum (always on the 64 MiB minimum plan).

## What Changes

- A declared length is checked against the free budget (fast 503 when it cannot fit, 413 above the maximum) but no longer reserved: every body is charged as its bytes arrive, so a request that declares a length and sends nothing holds nothing.
- A body must keep arriving: each read that returns data moves the connection's read deadline 10 s ahead, never past 2 minutes from the start of the body (the server ReadTimeout). A stalled upload ends with 408 `request_body_timeout` and releases its charge.
- The charged stream stops at the route maximum; one more byte, read without charge, yields 413 `request_too_large` (transcription audio keeps 413 `audio_too_large`), so an oversized body is never reported as budget pressure.
- The requirement «Idle client WebSockets do not consume HTTP body-reader capacity» is renamed to «…request-body capacity»; the 1 GB sizing example is stated for real 1 GB VPS memory.

## Capabilities

### Modified Capabilities

- `go-runtime`: request-body admission charges bytes as they arrive, bounds idle uploads and prefers 413 over 503 past the maximum.

## Impact

`internal/adapters/httpapi` body admission and readers, `cmd/codex-lb` ReadTimeout constant. New client-visible error: 408 `request_body_timeout` for an upload that stops for 10 s. No schema, protocol or dashboard change.
