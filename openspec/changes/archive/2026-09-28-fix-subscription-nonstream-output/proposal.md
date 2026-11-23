## Why

Real Luna E2E returns a successful non-streaming Chat Completion with empty text and charged usage. Subscription Responses emits final output in `response.output_item.done` while the terminal event contains an empty `output`; the Go bridge currently discards those items for non-streaming clients.

## What Changes

- Preserve bounded completed output items when assembling subscription non-streaming Responses and translated Chat Completions.
- Reuse compact's indexed output assembly, preserving nonempty terminal output and metadata, accounting and failure semantics.
- Leave streaming forwarding unchanged and verify both public non-streaming routes with actual adapters and real Luna.
- Correct translated Chat streaming usage: Responses uses snake_case wire fields, not the dashboard domain's camelCase JSON fields.

## Capabilities

### Modified Capabilities
- `go-runtime`: complete JSON output when the subscription terminal event omits streamed items.

## Impact

Provider stream-to-JSON bridge and existing compact output collection; focused tests and existing specifications. No new dependency, retry, endpoint, storage schema or deployment.
