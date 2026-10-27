## 1. Upstream WebSocket pool

- [x] 1.1 Regression tests from the audit probes: unsolicited idle frames, foreign terminals, slow consumer, dead idle socket and dead continuation anchor.
- [x] 1.2 Per-response lease hand-off, unsolicited marking, response identity checks.
- [x] 1.3 Frame-aware probe, detached pings joined on close, bounded response.create write; `TestWebSocketCompletionStopsPendingProbeAndReusesConnection` green under `taskset -c 0,1 go test -race -count=50 -cpu 2`.
- [x] 1.4 Reuse fence for idle or unsolicited sockets with pre-create redial or continuation rejection.

## 2. HTTP upstream

- [ ] 2.1 HTTP/2 health configuration and TLS session cache in the runtime transport literal.
- [ ] 2.2 Bounded body drain after the Responses terminal and Chat `[DONE]` (upstream and native provider paths).

## 3. Streaming behavior

- [ ] 3.1 Linear tool-call argument accumulation and streamed function calls for Chat sources.
- [ ] 3.2 Payload-size stream bound instead of the event-count bound.
- [ ] 3.3 Silence-based heartbeats; native SSE data-event heartbeats; downstream WebSocket protocol pings.
- [ ] 3.4 Native chat incomplete mapping with `created` and `model`.

## 4. Realtime and operations

- [ ] 4.1 Realtime write bounds and pings in both directions.
- [ ] 4.2 Trusted-proxy client identity for operation requests.

## 5. Cost

- [ ] 5.1 Header-only event decode, single-copy SSE events, buffered `writeSSE`, 8 KiB scanner; record benchmarks.
- [ ] 5.2 Raw object re-assembly, single-parse WebSocket create, gated image detection, fresh-map WebSocket reader; record benchmarks; `GOEXPERIMENT=jsonv2` decision.
- [ ] 5.3 Remove dead parameters, per-request mux and redundant URL rewriting.

## 6. Verification

- [ ] 6.1 `go vet ./...`, `golangci-lint run ./...`, `go test -count=1 ./...`, targeted `go test -race`; strict OpenSpec validation. Do not archive.
