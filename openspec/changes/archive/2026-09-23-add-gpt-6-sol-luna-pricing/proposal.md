## Why

Requests for GPT-6 Sol and GPT-6 Luna currently lack built-in cost estimates. Add their published tariffs to the existing subscription-account pricing path so new logs, reports, and API-key cost limits recognize these models.

## What Changes

- Recognize canonical and versioned Sol/Luna model IDs using the shared price table.
- Use Codex Fast pricing, Flex pricing, and the published long-context rates without extending Astra's explicit exception to other models.
- Preserve existing models, explicit external-source costs, and authoritative upstream service-tier precedence.
- Do not change stored history, database schema, deployment, or routing.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `api-keys`: built-in GPT-6 Sol/Luna pricing for usage accounting.

## Impact

Shared pricing calculator, existing unit/integration tests, and pricing specifications. No new dependency or configuration.
