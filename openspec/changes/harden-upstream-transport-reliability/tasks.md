## 1. Upstream WebSocket pool

- [x] 1.1 Regression tests from the audit probes: unsolicited idle frames, foreign terminals, slow consumer, dead idle socket and dead continuation anchor.
- [x] 1.2 Per-response lease hand-off, unsolicited marking, response identity checks.
- [x] 1.3 Frame-aware probe, detached pings joined on close, bounded response.create write; `TestWebSocketCompletionStopsPendingProbeAndReusesConnection` green under `taskset -c 0,1 go test -race -count=50 -cpu 2`.
- [x] 1.4 Reuse fence for idle or unsolicited sockets with pre-create redial or continuation rejection.

## 2. HTTP upstream

- [x] 2.1 HTTP/2 health configuration and TLS session cache in the runtime transport literal.
- [x] 2.2 Bounded body drain after the Responses terminal and Chat `[DONE]` (upstream and native provider paths).

## 3. Streaming behavior

- [x] 3.1 Linear tool-call argument accumulation and streamed function calls for Chat sources.
- [x] 3.2 Payload-size stream bound instead of the event-count bound.
- [x] 3.3 Silence-based heartbeats; native SSE data-event heartbeats; downstream WebSocket protocol pings.
- [x] 3.4 Native chat incomplete mapping with `created` and `model`.

## 4. Realtime and operations

- [x] 4.1 Realtime write bounds and pings in both directions.
- [x] 4.2 Trusted-proxy client identity for operation requests.

## 5. Cost

- [x] 5.1 Header-only event decode, single-copy SSE events, buffered `writeSSE`, 8 KiB scanner; record benchmarks.
- [x] 5.2 Raw object re-assembly, single-parse WebSocket create, gated image detection, fresh-map WebSocket reader; record benchmarks; `GOEXPERIMENT=jsonv2` decision.
- [x] 5.3 Remove dead parameters, per-request mux and redundant URL rewriting (ProxyHandler.mux, publicWarmupKey and native pump unification deferred: owner/auth zones).

## 6. Verification

- [x] 6.1 `go vet ./...`, `golangci-lint run ./...` (no new findings; pre-existing findings outside the transport files remain), `go test -count=1 ./...`, targeted `go test -race`; strict OpenSpec validation. Do not archive.
