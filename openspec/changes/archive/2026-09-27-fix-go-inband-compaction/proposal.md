## Why

The authorized real Luna E2E received HTTP 404 from the private `/responses/compact` endpoint. Original codex-lb now posts a terminal `compaction_trigger` to `/responses` and collects its SSE result. The Go adapter still implements the retired wire contract.

## What Changes

- Send subscription compaction through the current in-band Responses contract while keeping public compact routes unchanged.
- Collect bounded SSE output, normalize encrypted compact items and retain actual billing on failure.
- Release create capacity at the first upstream event; prohibit auth/quota replay after observed output.
- Verify the real compact result can continue the conversation without replacing the running service or copying refresh credentials.

## Capabilities

### Modified Capabilities
- `go-runtime`: subscription compaction compatibility, accounting and capacity safety.

## Impact

Provider operation transport/parser, application compact normalization and accounting, regression tests. No schema, dependency, deployment or user configuration change.
