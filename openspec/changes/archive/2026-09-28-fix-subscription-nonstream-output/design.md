# Complete subscription JSON output

- Last edited with skill pack: `0.2.2`

## Title and scope

Return the actual generated text and tool output to non-streaming subscription clients.
Preserve actual token counts in translated Chat streaming usage too.

## Planning anchor

Two real Luna requests to `/v1/chat/completions` returned empty content with successful usage (29 tokens each). `provider.callChatGPT` replaces the non-streaming consumer with a no-op, while `upstream.terminalFromResponsesEvent` keeps only terminal output. The same issue reaches `/v1/responses`. Amend go-runtime; preserve the streaming contract and prior compact normalization.

## Connected groups or observed existing logic

- NativeAPIService.Chat translates to Responses and consumes Proxy.Respond's final JSON. Ordinary non-streaming Responses returns the same provider result.
- Both subscription transports use callChatGPT and upstream event callbacks. Its send closure is the per-attempt boundary; retries must not inherit collected output.
- Compact already retains indexed items and fills empty terminal output within the operation response byte bound. Extract only this small output container for reuse, keeping compact's event parsing and accounting unchanged.
- Stream-to-JSON assembly occurs before the existing application settlement. Return the original result with any assembly error so known usage is not lost. Actual streaming clients do not use the collector.
- The subsequent real streaming Chat check returned correct text and internally charged 29 tokens, but client usage was zero. Its translator decodes snake_case Responses usage into a camelCase domain struct; explicitly decode the wire fields before the existing Chat usage rendering.

Focused caller and original-code inspection grounds this local compatibility slice; no separate reverse document or broad connected map is needed. No schema, settings, UI or deployment changes are needed.

## Use cases

### 1. Assemble the non-streaming response

subscription request --collect bounded completed items--> terminal response --fill missing output in index order--> complete JSON --settle once--> Responses or translated Chat reply

Implementation Logic:
Use a shared bounded container holding the latest JSON object per nonnegative output index and an ordered list for unindexed completed items. Validate item shape and cumulative bytes; never allocate a sparse index-sized array. Fill only missing/null/empty terminal output, preserving other fields and rejecting malformed output. Reuse the existing operation byte ceiling for retained items and the complete assembled object. The non-streaming per-attempt consumer records only output_item.done. Compact retains its current added/done selection. Propagate collection/assembly errors without discarding actual usage or marking them as pre-dispatch rejections. Do not buffer deltas or add retries.

Tests:
- description: Real route, provider and SQLite fixture returns message/tool items before an empty terminal output on both JSON routes.
  expected outcome: Original text/tool arguments and terminal usage reach the client, with one upstream request and one settled record.
- description: Indexed items arrive out of order or are repeated, and a terminal may already contain output.
  expected outcome: Stable index ordering, replacement without duplication, and authoritative terminal output.
- description: Malformed/oversized items or a missing terminal interrupt collection.
  expected outcome: Safe failure with no executed-request retry, preserving known usage or uncertainty.
- description: Run the real Luna JSON/SSE Chat checks again and a non-streaming Responses check.
  expected outcome: Exact synthetic reply and valid usage, no uncertain reservations; no working-server replacement.

### 2. Translate final usage for Chat clients

validated Responses terminal --decode snake_case usage--> token counts --render Chat fields--> final usage chunk

Implementation Logic:
Use explicit wire field names, including nested cached-input and reasoning details, then reuse chatUsage. Keep this separate from financial settlement, which already has correct values. Do not reinterpret a missing/invalid upstream count as proof of free execution.

Tests:
- description: The actual native Chat route streams a Responses terminal with input, output, cached and reasoning counts.
  expected outcome: Exact counts appear in the final Chat usage chunk; the real Luna stream matches its ledger and has no uncertain reserve.

## Implementation checklist

1. [x] Reproduce the real empty reply and trace both public consumers.
2. [x] Reuse bounded output collection in compact and the per-attempt non-streaming bridge.
3. [x] Verify public routes, ordering/bounds, failure/accounting, unchanged Responses streaming and correct translated Chat usage.
4. [x] Repeat live checks, run Go validation and synchronize the verified spec.

## Open questions

None blocking. Real quota exhaustion and deployment remain outside this test operation.

## Decision log

The current implementation flow authorizes this narrow E2E-discovered repair. Full-response buffering is limited to clients explicitly requesting JSON; collecting every ordinary long stream would conflict with the resource requirement. A dedicated completeness review loop is unnecessary for this bounded slice; semantic checks cover both consumers, existing compact behavior, bounds and financial error handling.

Original `legacy/app/modules/proxy/api.py::_merge_collected_output_items` confirms indexed collection and authoritative nonempty terminal output. The new public-route regression failed before the fix for both message and function-call output on Responses and Chat, then passed. Failure tests retain reported usage when assembly rejects malformed terminal output and keep an uncertain hold on a truncated stream, without replay.

The final real Luna rerun passed JSON Chat, SSE Chat with exact usage, and JSON Responses: three requests, 87 tokens, 18 microdollars in the isolated ledger, zero uncertain reservations. The full Go suite and vet passed; focused provider/HTTP race tests covered the changed collection, compact and native Chat paths. All 1245 frontend tests and lint passed, as did ten real-binary dashboard contract checks. The binary was rebuilt locally; no running service was replaced.
