# Definitive validation rejection accounting

- Last edited with skill pack: `0.2.2`

## Title and scope

Prevent an upstream input-validation refusal from needlessly locking a key's reserved budget, without treating unknown usage as free.

## Planning anchor

The real access-only Luna harness sent a compaction input item with an unsupported status property. The upstream HTTP response was 400, type `invalid_request_error`, code `unknown_parameter`; ordinary Responses returned the error and left one reconciliation reservation. This is distinct from the corrected compact transport. Amend `go-runtime` with the precise nonexecuted refusal contract.

## Connected groups or observed existing logic

- `upstream.beginStream`, `executeResponses`, `executeChat` and failed WS handshakes parse actual HTTP rejections. SSE/WS error events also use `rejectedResult`, so that shared parser alone cannot distinguish pre-stream HTTP rejection from an accepted stream failure.
- `provider.wrapUpstreamFailure` creates the application failure, preserving status and `Dispatched`; `Proxy.settle` asks `operationUsageUncertain`, which currently treats every dispatched validation error as uncertain.
- Translated Chat's stream failure wrapper must preserve the existing typed upstream error, including its status and new classification, rather than replace it with an unclassified error. Stream error events themselves never gain the classification.
- Native/ancillary JSON paths already distinguish definitive 4xx from uncertain accepted calls. Preserve their existing contracts. Introduce a narrow classification flag on the existing error types, not a generic retry mechanism or synthesized usage counter.
- Public-route tests use a real provider adapter and SQLite; verify reserve release, following successful request, no retry and unchanged account state. Parser tests cover every HTTP entrypoint and ambiguous/in-stream counterexamples.

Focused inspection already establishes the flow; separate reverse documentation and broad project mapping are unnecessary. No data migration, historical reconciliation or working-service replacement is authorized by this repair.

## Use cases

### 1. Recognize and settle validation refusal

reserved attempt --receive complete upstream HTTP rejection--> validated error envelope --classify nonexecuted request--> failed zero-charge settlement --return original error--> key budget available

Implementation Logic:
Wrap `rejectedResult` at actual HTTP rejection sites with a classifier for HTTP 400/422 and `error.type=invalid_request_error`. Require absent/null usage, duration and nested response, and absent/empty output and choices. Carry `RejectedBeforeExecution` through the existing failure type. Accounting may release that attempt without changing `Dispatched`, inventing upstream usage fields or authorizing retries. Reject classification when body reading failed or parsing fails.

Tests:
- description: Return a validation rejection on authenticated ordinary Responses, then a valid response.
  expected outcome: First request is failed/zero, no uncertain reserve, second request succeeds with normal usage and no account health mutation.
- description: Repeat the actual Luna malformed-input request through the isolated composed application.
  expected outcome: The same 400 is returned without holding a reservation or retrying.

### 2. Preserve unknown or reported billing

HTTP or stream failure --check output and billing evidence--> not definitively unused --apply existing known-or-unknown accounting--> settle actual counters or retain reconciliation

Implementation Logic:
SSE/WS event failures never receive the new HTTP-only classification. Malformed errors, arbitrary status 400, output, partial/invalid usage and duration do not qualify. Existing billing parser and safe quota/missing-previous behavior remain unchanged.

Tests:
- description: Exercise malformed envelopes, partial usage, duration, output, nested responses and in-band errors across HTTP/SSE/WS/translated Chat.
  expected outcome: No definitive-zero marker; actual usage retained and ambiguous attempts remain pending without replay.

## Implementation checklist

1. [x] Reproduce the real rejection and map classification/settlement boundaries.
2. [x] Implement narrow HTTP rejection classification and existing failure propagation.
3. [x] Verify parser/route/accounting regression, full tests and race checks.
4. [x] Repeat the real Luna rejection check; synchronize and archive specs.

## Open questions

Subscription errors containing only an unstructured detail string remain uncertain in this slice. Status-only or message-heuristic release is deliberately excluded. No automatic repair of old reservations.

## Decision log

- Existing authorized implementation flow applies; no independent review loop is needed for this focused classification fix. Completeness/consistency checks preserve the no-retry rule and keep unstructured rejection forms out of the new contract.
- 2026-09-27: The actual ChatGPT rejection was HTTP 400, type `invalid_request_error`, code `unknown_parameter` for a synthetic input item's unsupported status field. Before the fix: no finalized request, one uncertain reservation. After the fix: one failed zero-charge request, zero uncertain reservations, no replay. The working installation and its credentials were unchanged.
- Regression checks cover HTTP Responses, translated Chat, their SSE entrypoints, rejected WebSocket handshakes, in-stream errors, malformed bodies, billing/output evidence and truncated-body reads. Known usage in an otherwise truncated HTTP rejection is preserved. Real-adapter authenticated route tests verify a following successful request, exact accounting and unchanged account health. Focused race tests and `go vet` passed; full Go tests passed.
