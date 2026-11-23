# Долгое ожидание модели без обрыва Codex WebSocket
- Last edited with skill pack: `0.2.2`

## Title and scope

Устранить переподключения Codex из-за отсутствия служебных текстовых кадров во время долгого ответа. Не маскировать мёртвое upstream-соединение, не повторять возможно исполненную генерацию и не менять учёт квот.

## Planning anchor

`httpapi.ProxyHandler.websocket` синхронно ждёт `Proxy.Respond` без heartbeat. `responseOutput.send` буферизует created/in_progress для безопасного quota failover. `streamResponsesWebSocket` ждёт frames до общего двухчасового deadline. `responses-api-compat` native heartbeat contract reuse; `go-runtime` amend. Reverse-documentation skipped: narrow code inspection and previous diagnostic established this path.

## Connected groups or observed existing logic

- Boundary: authenticated native and generic WS routes share the handler, including trailing slashes. Auth, admission and capability checks remain unchanged.
- Application: a passive response-ID callback observes real startup events before buffering and clears the ID at each attempt boundary. It never changes visible output, provider usage, diagnostics or retry eligibility.
- Downstream: reuse the SSE worker/event acknowledgement pattern for a single WS writer, bounded writes and joined cancellation. Native heartbeats use a real response ID when available; otherwise a vendor keepalive. Generic routes receive neither synthetic event.
- Upstream: the existing reader consumes ping/pong concurrently with application frames. A scoped ping probe detects broken transport; no new provider request, account failover or HTTP fallback follows a post-dispatch probe failure.
- Persistence: no schema changes. Existing reservation settlement/reconciliation and owner fences remain authoritative. UI and pricing are unaffected.
- Validation: route tests cover native aliases, pre-created and post-created silence, generic absence, quota retry, terminal/cancel cleanup and exact usage. Upstream peers cover responsive silence and withheld pong without replay.

## Use cases

### 1. Поддержать ожидающий Codex
разрешённый WS запрос --запустить ограниченный worker--> ожидание ответа --передать реальные события или native heartbeat--> завершённый ответ --остановить и дождаться worker--> освобождённый запрос

Logic Details:
- Native-only ticker every 10 seconds emits `codex.keepalive` before a current upstream response ID, or `response.in_progress` with that real ID. A single downstream writer preserves terminal ordering. No heartbeat follows a terminal event.
- Observe real response IDs before the prelude buffer; clear between attempts and after dispatch returns. Heartbeats bypass application emit, metrics and diagnostics. Quota refusal can still discard the failed prelude and safely select another authorized account.
- Cancel/disconnect and failed bounded writes cancel the worker and join it; no leaked request, heartbeat or admission lease remains. Keep existing uncertain billing rules.

Tests:
- description: A deterministic local slow provider spans multiple heartbeat intervals before created and before terminal, then returns one accounted response.
- description: Native canonical/trailing slash routes receive heartbeats; public v1 canonical/trailing slash routes do not. No synthetic ID or fabricated token/usage is produced.
- description: Quota refusal after heartbeat retries only under existing safe-zero rules; cancellation and terminal stop all request-owned work.

### 2. Отличить молчание модели от потери соединения
активный upstream WS --проверить ping с ограниченным ожиданием pong--> [живой транспорт --продолжить ожидание в общем deadline--> ответ модели, мёртвый транспорт --завершить без повторного dispatch--> неопределённый учёт]

Logic Details:
- Use an active-turn protocol ping every 30 seconds with a 10-second pong bound while the existing reader drains application frames. Join the probe on completion/cancel and discard the broken socket.
- Pong and downstream heartbeats do not reset the overall response deadline or count as model progress. Preserve the existing two-hour deadline: a successful 21-minute reasoning request must not be killed by a five-minute application-silence policy.
- Failure after create is dispatched, not proven-zero. Preserve diagnostics and reconciliation; do not manufacture a quota refusal or migrate the owner.

Tests:
- description: A responding WS peer stays valid during application silence and completes once; a peer withholding pong fails within the probe budget and is not replayed.
- description: Parent cancellation and normal completion join the probe and preserve healthy continuation behavior.

## Implementation checklist

1. [x] Establish connected scope and compatibility rules without changing deployment.
2. [x] Implement passive attempt-scoped response-ID observation and native WS heartbeats.
3. [x] Add bounded upstream transport probes and lifecycle regressions.
4. [x] Run route/accounting tests, full Go/race/vet and spec validation; synchronize contracts and release notes.
5. [x] Prepare Linux amd64/arm64 release assets for the explicitly approved publication; retain live services unchanged.

## Open questions

- Blocking: none. SDD default chosen pending optional user response. User explicitly confirmed commit/push/go-v1.0.7 publication; live service updates are excluded.

## Decision log

- 2026-10-08: implementation authorized by «исправляй и готовь релиз». Existing routing edits preserved. No arbitrary shorter reasoning deadline, no new dependencies/configuration, no paid probes.
- Completeness/consistency checked against the two bounded stream lifecycles; no independent feature added. Frame capture was unavailable, so this fixes the proven missing protection without claiming every upstream delay has the same cause.
- Verification: full `go test ./...`, `go test -race ./...`, `go vet ./...`, focused stream tests three times under race, UI build and 11 offline binary/UI contract tests passed. The cancellation fixture now waits for provider dispatch before cancelling: a pre-dispatch heartbeat does not prove execution has started.
- OpenSpec: all 61 specs pass normal validation; changed go-runtime and this delta pass strict validation. Repository-wide strict validation reports pre-existing placeholder-purpose warnings in 22 legacy specs; unrelated docs are not rewritten for this fix. The layered design validator passed.
- A single independent readonly review found no blockers in callback, writer, probe or cancellation lifetimes. ARM64 was cross-compiled, not executed; no paid provider E2E or deployment occurred.
