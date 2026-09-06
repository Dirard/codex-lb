## Why

An upstream account quota rejection should be recovered inside the proxy when another eligible account can safely serve the request. Clients should not need to resubmit a recoverable turn after one account runs out of quota.

## What Changes

- Make safe recovery from an explicit upstream quota rejection select an eligible replacement before emitting a terminal quota error downstream.
- Preserve owner affinity at locally reported zero quota until OpenAI actually rejects the request for quota exhaustion.
- Keep existing account access restrictions, replay safety, partial-output handling, and usage settlement intact.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `sticky-session-operations`: require automatic recovery of replayable quota rejections when an eligible replacement exists.
- `responses-api-compat`: keep model-capacity retries consistent with quota-only hard-owner transfer.

## Impact

Responses proxy quota retry paths and focused transport-level regression tests. No database migration or deployment is included.
