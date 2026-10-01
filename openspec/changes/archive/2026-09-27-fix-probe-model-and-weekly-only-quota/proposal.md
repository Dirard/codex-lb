# Luna probes and weekly-only quota

- Last edited with skill pack: `0.2.2`

## Why

The live manual probe used unsupported `gpt-5.4-mini`, while the account's sole 10080-minute quota was stored as primary and displayed as five-hour quota. The user selected `gpt-6-luna` and authorized the fix and local update.

## What Changes

- Use `gpt-6-luna` when a probe/warmup omits a model, preserving explicit model choices and never silently falling back to a more expensive model.
- Classify a sole seven-day upstream window as weekly and persist a complete quota observation without retaining a removed primary window.
- Preserve incomplete/absent telemetry, observation fences, purchased credits and accounting; repair the identified mislabeled weekly history without changing usage amounts.
- Suppress the five-hour legend for weekly-only accounts and verify the production HTTP/SQLite paths before local deployment with backup.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: default probe model and weekly-only quota observation/display.

## Impact

Warmup default, usage adapter/application/SQLite snapshot persistence, account usage legend and regression tests. No new dependencies or schema migration. The existing uncertain administrative probe remains pending; no paid verification probe is implied.
