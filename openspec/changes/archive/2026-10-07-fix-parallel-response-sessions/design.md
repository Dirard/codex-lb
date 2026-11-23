# Parallel Responses connection leases
- Last edited with skill pack: `0.2.2`

## Context and planning anchor

See `proposal.md` for the incident and scope. `websocketSessions.acquire` currently maps one logical session to one connection and waits on that connection for up to 15 seconds. A full-context request has no dependency on an in-flight sibling merely because both share Session_id. Exact previous-response affinity is different and must remain scoped to provider, account incarnation, API key, route, endpoint, credentials and required capability.

This is an implementation-authorized, bounded concurrency repair. Amend `go-runtime`; reuse its existing safe replay and accounting rules. Separate reverse-documentation is unnecessary because the live paths are established by focused source inspection. No DB, pricing, key/group policy, routing strategy or client model catalog changes belong here.

## Connected responsibilities

- HTTP boundary: `proxy.go#responseOptions` preserves session/thread identity. `proxy_websocket.go` currently consumes the body-reading semaphore for the entire downstream socket lifetime.
- Application: `conversation_identity.go` already isolates distinct Thread-Id values. `proxy.go` owns admission, owner resolution, reservation settlement and the bounded same-owner recovery after missing previous-response state.
- Provider: `provider.go#respondChatGPT` carries the authenticated owner and logical session to the upstream adapter; rejected-before-execution proof reaches accounting.
- Upstream: `websocket_sessions.go` owns connection lookup, leasing, retirement and response affinity; `responses_websocket.go` performs exactly one response.create write per acquired attempt.
- Tests: local WebSocket stubs plus the existing HTTP proxy/SQLite fixture exercise the visible route, not only helpers. Existing required-capability and ownership tests remain mandatory.

## Goals / Non-Goals

Independent work sharing a session must use available stream capacity without sharing event readers. A continued branch must never silently inherit another branch's context. Resource limits remain finite; genuine saturation can still reject work. We do not promise unlimited concurrency or hide upstream failures. No automatic retry after a possibly dispatched write, no account migration for capacity, no paid probes and no rollout in this task.

## Decisions

Use exclusive leases on physical upstream sockets, not a mutex per logical session. Keep an exact response-to-socket index and allow multiple bounded sockets for one session. Prefer a matching idle connection for sequential reuse; independent work opens a sibling when the existing connection is occupied. All sockets count against the existing 256-socket bound, and eviction is idle-only.

Do not multiplex unlabelled upstream event frames on a shared socket. Do not increase the 15-second conflict timeout to conceal head-of-line blocking. Previous-response work retains exact affinity; missing connection state can use the application's already bounded same-owner replay only when reconstruction and zero-execution checks pass. Connection retirement must not send a fresh full-context request through a dead lease.

Downstream WebSockets receive a separate finite admission budget supporting 256 simultaneous clients. Idle sockets no longer consume the 128 simultaneous HTTP-body reader slots. Per-request execution remains governed by application stream/account/key admission.

## Use cases

### 1. Run independent subagents in parallel

authenticated full-context request --resolve owner and admit--> scoped request --lease idle or new socket--> exclusive upstream attempt --settle and retain affinity--> completed branch

Logic Details:
- Lookup and exclusive ownership are decided under the session-store mutex; network I/O occurs outside it.
- Claim the available socket gate under the same mutex so an exact continuation cannot overtake the independent lease. Retirement visits all sibling lanes even while removing them.
- A busy sibling does not become a waiter for a request without a previous-response dependency.
- Release updates only its own response mapping. Cancellation and eviction cannot delete another live socket or its newer affinity.
- If all 256 sockets are in use, reject before response.create without changing account health or inventing usage.

Tests:
- description: Hold many requests with the same authenticated session concurrently and receive their distinct results without cross-delivery; the next request is rejected only at the real bound.
- description: Sequential continuation reuses the correct connection; key, account generation, route, endpoint, credential and capability isolation still apply.

### 2. Preserve or recover an exact continuation

authenticated previous response --resolve exact owned socket--> continuation lease --dispatch once--> settled continuation

Logic Details:
- Preserve bounded serialization where a request depends on a particular occupied socket; do not mistake an arbitrary idle sibling for the owner of previous_response_id.
- An exact previous-response lookup never falls back to SessionID. After waiting, recheck both response mapping and latest anchor under the mutex; a changed/dead anchor is a proven pre-dispatch continuation loss. A busy live exact anchor still uses the existing bounded wait, not speculative replay.
- Retirement before dispatch never repeats work that might already have executed. A reconstructible lost continuation can use existing same-owner replay, with old opaque turn metadata removed.
- Failure or cancellation of one branch leaves other active siblings intact. Required-capability transitions inspect every matching ordinary lane and remain fail-closed while ordinary work is pending.

Tests:
- description: Exercise two branches, exact previous-response routing, cancellation during contention, connection closure and same-owner replay; assert dispatch counts and settled reservations.
- description: Unsafe/opaque continuations remain fail-closed, with no cross-account retry or fabricated successful usage.

### 3. Keep idle client sockets separate from request readers

authenticated socket upgrade --acquire client connection slot--> idle or active connection --close or drain--> released client slot

Logic Details:
- Only HTTP body reads consume the body-reader semaphore. The socket semaphore bounds socket lifetimes independently.
- A rejected/failed upgrade releases its slot; disconnect and runtime drain release accepted sockets.
- The existing per-socket response queue and shared application admission stay bounded.

Tests:
- description: Open at least 129 idle sockets and complete an HTTP request; open 256 sockets, reject overflow, close them and verify admission recovers.

## Risks / Trade-offs

- More genuinely parallel work uses more sockets than serialization; the existing upstream cap and separate downstream cap bound this cost.
- Exact opaque continuation dependencies cannot be made parallel by dropping context; retained serialization is intentional.
- Local fixtures prove proxy mechanics, not unlimited upstream capacity or VPS throughput. No production benchmark is claimed.

## Implementation checklist

1. [x] Trace the incident to connection contention and record the invariants.
2. [x] Add failing parallel-session and downstream-admission regression tests.
3. [x] Implement bounded exclusive connection leases and lifecycle-safe cleanup.
4. [x] Separate downstream socket admission; verify visible routes and accounting.
5. [x] Run focused tests, all Go tests, race tests and vet; synchronize spec/context and archive only after verification.

## Migration and decision log

No schema or configuration migration. A separately authorized release can replace the binary using the normal drain procedure; this task does not restart or update any service. Rollback is a binary rollback.

- 2026-10-06: User authorized the fix. SDD is the default pending the optional answer. Root checked completeness and consistency: clarified exact-only response affinity, post-wait anchor validation and pre-dispatch proof. Structural validation passed. No automatic review loop or evidence matrix.
- Deferred: changing global stream limits, production load tests, releases and deployment.
- Reproduced before the upstream repair: both HTTP and downstream WebSocket admitted only one of four independent same-session requests; a stale previous-response branch was incorrectly sent to the advanced socket. The downstream test failed at idle socket 129 before the semaphore split and passed under the race detector after it.
- 2026-10-07: The old provider waiting test now uses a real exact-response dependency, not mere session sharing. Responses Lite recovery is body-derived from retained additional_tools; a fresh downstream connection still cannot inherit a client-supplied Lite marker for an otherwise ordinary live continuation.
- Final checks: `go test ./...`, `go test -race ./...`, `go vet ./...` and `git diff --check` pass. Focused concurrent HTTP/WS, 256-socket, cancellation and capability checks pass three times under the race detector. Tests use local upstream stubs and real proxy/provider/SQLite paths; there were no paid probes, commits, releases or service changes.
- The change and go-runtime spec pass strict OpenSpec validation. Repository-wide strict spec validation still reports 22 existing Purpose placeholders in unrelated historical specs; these are not expanded into this fix. The remaining true capacity limits and bounded exact-anchor waits are intentional, not promises of unlimited concurrency.
