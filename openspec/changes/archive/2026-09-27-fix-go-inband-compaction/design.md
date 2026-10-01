# In-band subscription compaction repair

- Last edited with skill pack: `0.2.2`

## Title and scope

Repair the real Luna compaction failure and verify continuation without updating the working installation.

## Planning anchor

Real E2E returned 404 for compact, after successful answer/tool/resume/image operations. Original commit `09a140fa9979a908e60acc97232367e0a08ef32c`, `app/core/clients/proxy.py`, `_CompactCommandTransport.execute`, `_compact_response_payload_from_sse` and `_normalize_compact_response_payload_shape` establish the current in-band wire contract. Amend `go-runtime`; leave normal Responses and external-source compact support unchanged.

## Connected groups or observed existing logic

- HTTP compact and Codex final trigger reach `CodexOperations.CompactWithOptions`; owner/capability checks and per-attempt reservations occur before `provider.Compact`.
- `provider.Compact` currently sends JSON to the retired endpoint. `chatGPTBody` already owns credential/header resolution and one uncharged authentication retry; `operation` owns bounded HTTP reads and error billing. Retain these mechanisms, supplying a compact-specific success reader rather than copying the auth flow.
- Reuse exported `upstream.ReadSSE` for framing. Retain only bounded latest output items, not a transcript of every event. Explicit HTTP failures keep the existing JSON operation parser and service-tier/usage validation.
- `NormalizeCodexCompactOutput` is called only by compact surfaces. Add original message-shaped summary compatibility here; do not reinterpret ordinary Responses messages globally.
- `CodexOperationResult.OutputObserved` and target first-event callback bridge the new streaming behavior to replay, settlement and account leases. Unknown partial output must not become a free quota rejection. Compact and its public warmup caller now acquire stream leases; the old nonstreaming assumption is invalid for in-band compact. `billedAdmitted` supplies the callback with its effective lease to both callers.
- Existing operation-refresh, compact-failover, capacity, route and usage tests cover adjacent contracts. Add actual provider-backed route coverage because previous mocks accepted the obsolete endpoint.

Focused source tracing establishes the existing behavior; a separate reverse-document artifact is unnecessary. No configuration/schema migration, general retry framework or deployment is needed.

## Use cases

### 1. Execute and collect compact

authorized compact --preserve existing selection and reservation--> admitted attempt --normalize trigger and private wire fields--> upstream Responses stream --collect bounded output and terminal usage--> normalized compact result --settle and retain owner--> client continuation

Implementation Logic:
Reuse private wire normalization for scalar input, version, Lite and reasoning. Append a trigger only when not already terminal. Use a success-reader hook in the existing operation path; JSON errors retain its existing parser. SSE completion does not require an ID. Track indexed added/done items and unindexed done items only, capped by the existing operation response limit and event bound. Prefer nonempty terminal output. Accept JSON success for the original supported compatibility form. Normalize explicit compaction output first, then the last nonempty message text, then the top-level summary.

Tests:
- description: Request canonical and equivalent compact/trigger routes with the real adapter and streamed upstream output.
  expected outcome: Correct private endpoint/trigger, client-compatible result, retained usage and continuation ownership.
- description: Collect missing output, message-shaped output and ID-less completion.
  expected outcome: Usable compact output without relaxing usage validation.

### 2. Fail without duplicate billing or leaked capacity

admitted stream --receive first event--> create lease released --observe output or terminal failure--> known or unknown usage --settle or retain reservation--> stream lease released

Implementation Logic:
Expose first-event notification on the operation target. An observed output prohibits both auth refresh replay and quota replay even with zero reported tokens. Unknown usage after output retains its reservation; a true pre-output uncharged quota refusal retains existing failover behavior. Preserve existing duration/partial-usage authentication guards and valid error service tiers. Cancellation closes the upstream body and releases both leases.

Tests:
- description: Inject failures with valid, partial, absent and duration-bearing billing, including quota after output.
  expected outcome: Exactly one charged attempt or an unknown reservation, no unsafe replay; pre-acceptance zero-cost retry still works.
- description: Block compact after its created event and admit another account request.
  expected outcome: Create capacity is available while compact still owns its stream slot; all slots release on return.

## Implementation checklist

1. [x] Confirm real failure and original contract; trace callers and billing/capacity effects.
2. [x] Implement compact stream transport/normalization and output-aware safety.
3. [x] Run focused provider, public-route, capacity, billing and race checks.
4. [x] Verify real Luna compaction and post-compact continuation in a safe isolated runtime.
5. [x] Synchronize and archive verified specs; keep the broader product goal open.

## Open questions

No implementation blocker. Real verification must not copy OAuth refresh credentials or write concurrently into the working database. Use a non-refreshing access-token-only test target with expiry checks and isolated accounting.

## Decision log

- SDD implementation flow remains active. This bounded compatibility repair needs no separate review loop. Completeness and consistency checks retain current billing and ownership safety rather than introducing a generic Responses retry path.
- 2026-09-27: Actual private Luna compact and HTTP-triggered compact both preserved random markers on continuation. Four real requests settled 580 tokens / 132 microdollars, no uncertain reservations. The separate real Codex CLI run generated disposable padding, auto-compacted once and resumed with the exact marker: three real requests, 50,224 tokens / 2,703 microdollars, no uncertain reservations. Original refresh credentials and the working database remained untouched; the access-only test source had no refresh implementation.
- Test-harness corrections: raw synthetic SSE output contains lifecycle status, but Codex's typed Compaction request item excludes it; echoing the response verbatim caused a genuine upstream unknown-parameter rejection. A 1,000-token compaction threshold was below the client's fixed prompt size and correctly failed; the successful CLI threshold is based on measured initial tokens with removable padding. Neither result justified changing production response semantics.
- Full `go test ./...`, `go vet ./...`, and race checks for Compact/Compaction/PublicWarmup passed. Existing JSON mocks were updated to valid encrypted results; route tests now require the actual Responses endpoint and SSE. Working service remained active at PID 733312 with zero restarts.
