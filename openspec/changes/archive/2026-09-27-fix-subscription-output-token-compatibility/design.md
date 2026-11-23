# Fix the unsupported output-token parameter in subscription probes

- Last edited with skill pack: `0.2.2`

## Title and scope

Make the Luna probe accepted by the subscription request contract, keeping external output caps and per-key accounting intact. Compare the failing stream path with the original implementation and verify real requests after local regressions and deployment.

## Planning anchor

`POST /api/accounts/{id}/probe` now reaches GPT-6 Luna but OpenAI rejects `max_output_tokens`. Amend the Go runtime and the obsolete token-floor clause of usage-refresh-policy; retain all unrelated account-status and quota behavior. Existing archived fixes remain historical. The active SDD implementation flow is already authorized.

## Connected groups or observed existing logic

- Probe: `warmupBody` supplies model/input/max_output_tokens/store, while `chatGPTWireBody` adds required subscription streaming/instructions and normalizes input. One pinned generation owns durable internal usage, not user-key limits.
- Compatibility: legacy `_strip_unsupported_fields` removes this parameter for subscription payloads. Public Responses documentation supports it, but the actual private subscription endpoint rejects it. The Go adapter missed that difference; external native Responses and Chat Completions use separate paths.
- Billing: `parseResponse` defaults the output estimate to 2048 but accepts a smaller explicit value. `Proxy.Respond` supplies that estimate to the ledger, which clamps it to the remaining budget. Apply the existing default as an estimate floor only for subscription accounts, while retaining this clamp, actual settlement and uncertain reservations.
- Transport: HTTP/WS share `chatGPTWireBody`; compact has its own object normalization. Both omit this one field without stripping unrelated unknown fields or modifying the caller buffer.
- Live stream evidence: the subscription endpoint returns HTTP 200 with no Content-Type header (zero header bytes) and a valid SSE body. The Go `beginStream` guard closes it before reading anything. A bounded capture replayed through the unchanged parser with an SSE header succeeds and reports known usage (9 input / 6 output tokens in the diagnostic sample). The original repository fetched at commit `09a140fa9979a908e60acc97232367e0a08ef32c` is the comparison baseline.
- Original comparison: `app/core/clients/proxy.py` at that commit enters `_iter_sse_events` after a successful HTTP status without checking Content-Type (direct path lines 4150–4163; routed path 3992–4006). Native egress defaults `NativeSseOptions.content_type_aware` to false and also accepts absent MIME (`app/core/clients/native_egress.py:829–837`). The original administrative probe only returns HTTP status; retain the Go runtime's stronger actual-terminal/usage accounting instead of copying that shortcut.
- Validation: authenticated probe fake rejects the observed parameter; HTTP/WS/compact boundary tests and external passthrough assertions cover the wire difference. Proxy admission regression covers a tiny cap. Reverse documentation/review loops are unnecessary for this focused established path.

## Use cases

### 1. Send a compatible request and preserve accounting

validated selected request --reserve an appropriate account-specific output estimate--> admitted request --omit the unsupported subscription wire field--> one upstream attempt --settle reported usage--> accounted result

Implementation Logic:

Delete `max_output_tokens` from the subscription object in Responses and compact. Name and reuse the existing 2048 default estimate in parsing and the subscription-only reservation floor. Preserve larger estimates, external paths, model selection, credentials and caller bytes. Do not add retries, output cancellation heuristics or synthetic zero billing.

Permit an absent Content-Type only through an explicit upstream capability enabled by the ChatGPT provider. Keep the default media-type check for external providers and reject explicitly incompatible media types. The existing bounded SSE parser and terminal/id/usage validation remain the success boundary; an absent header must never turn HTML, invalid JSON, missing terminal or unknown usage into success.

Tests:

- description: The existing authenticated probe regression reproduces the reported 400 when the field is present and succeeds after the repair, while preserving known/unknown usage cases and one dispatch.
- description: HTTP, WS and compact omit the cap without mutating input; an external Responses provider still receives it.
- description: A one-token hint reserves the normal 2048 estimate, clamped to remaining budget when necessary; the final key usage becomes the reported actual output, not the hint or reservation.
- description: The authenticated probe receives headerless valid SSE and settles known usage; ordinary external streams still reject absent MIME, and subscription streams reject explicit wrong MIME and malformed/incomplete terminal data.

### 2. Validate the deployed probe against the original stream contract

original implementation and captured failure --identify the protocol difference--> verified repair --back up and replace local service--> healthy local instance --probe through the real API--> observed terminal outcome and accounted usage

Logic Details:

The original single-probe authorization was consumed. The user subsequently authorized as many probes as needed and explicitly requested investigation of the original implementation. Use a minimal number of short Luna probes, preserve their real accounting, and record outcomes. A temporary opt-in Go diagnostic can call the same WarmupService/provider, capture at most 64 KiB in memory, and replay those bytes locally through the low-level parser; only static failure classifications and payload types may be printed. Remove this temporary diagnostic after replacing it with a deterministic sanitized regression. Do not emit secrets or create another same-host test login. No schema migration is needed; keep a consistent rollback backup.

## Implementation checklist

1. [x] Add wire and reservation regressions and reproduce the unsupported-parameter failure.
2. [x] Implement normalization/floor, run tests/vet/build and synchronize requirements/context.
3. [x] Resolve invalid_stream using original-code comparison and safe live capture, update the local service, and verify the real probe through the dashboard. Temporary diagnostic code removed.

## Open questions

None. Missing Content-Type is confirmed by live captures and original-code comparison; both application/provider and deployed dashboard probes now complete with settled actual usage.

## Decision log

- Keep the requested Luna model and existing short prompt. The subscription endpoint does not enforce the public API cap; do not promise that it does.
- Restore the omitted compatibility behavior at the adapter boundary, rather than patching only the probe caller. The matching reservation floor prevents a new limit bypass.
- Do not release historical uncertain reservations or infer their billing from this repair.
- The ledger's intentional legacy behavior clamps estimates to remaining budget rather than rejecting every estimate above it. Keep that behavior and test the in-flight reserved amount; do not change global admission semantics to satisfy a mistaken test assumption.
- Local verification passed: the full Go suite, focused race suite, go vet, binary build, ten real-binary frontend integration tests, layered validation and strict validation of both changed specs. These checks do not establish acceptance by the live upstream.
- Local port 2456 now runs `go-local-e2e-probe-output-compat`; readiness, SQLite quick_check, client version 0.156.0, two accounts, four keys and the encryption key were verified. Rollback is `/home/dirard/.local/share/codex-lb-go-local/rollback-pre-output-compat.rHAjra`.
- Exactly one real Force probe was sent through the authenticated dashboard: `warmup_bda61077205008e7672c1cd9`, gpt-6-luna, 2026-09-27 17:22:33 UTC. It returned invalid_stream after about 2184 ms, with no recoverable response/events or known usage in the archive. The reservation remains pending. Reservation count increased from four to five; no further probe was sent. The output-parameter rejection is no longer the observed failure, but the full probe is NOT fixed or verified successful.
- After expanded authorization, three diagnostic warmups confirmed HTTP200 with absent Content-Type; a bounded in-memory capture replayed as valid SSE. With the missing-MIME capability enabled, a real warmup through the same application/provider finalized successfully: `warmup_55e96d0be1fb86d47dbc3225`, Luna, input9/output5/cached0, cost3 microdollars, needs_reconciliation=false. This confirms the cause rather than inferring it from a mocked response. Historical unknown reservations remain unchanged.
- Final verification passed: full Go suite, focused race checks of MIME/probe/terminal/provider paths, go vet, binary build and ten real-binary integration tests. The temporary live diagnostic was removed, and provider/upstream tests passed again without it. Main spec and delta validation remain strict for the changed capabilities; unrelated legacy placeholder warnings are not expanded into this task.
- The installed service now runs `go-local-e2e-probe-sse-compat` with readiness healthy, no unexpected restarts and unchanged encryption key. Backup: `/home/dirard/.local/share/codex-lb-go-local/rollback-pre-sse-compat.J4SvqB`.
- The real deployed dashboard Force probe also succeeded: `warmup_972509f5e6b19dbaf0322cd5`, Luna, input7/output7/cached0, cost4 microdollars, finalized, needs_reconciliation=false. The UI updated to 28 tokens / 2 successful requests. Both latest real requests have known usage and status ok. Historical uncertain entries were not deleted or fabricated as zero.
