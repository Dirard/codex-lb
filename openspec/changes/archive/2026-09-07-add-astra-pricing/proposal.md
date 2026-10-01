## Why

GPT-6 Astra is absent from the built-in pricing table, so its requests have missing cost estimates and existing reports undercount usage. The administrator needs both new and historical Astra requests priced for Codex without an API long-context surcharge above 272,000 input tokens.

## What Changes

- Recognize `gpt-6-astra` and its versioned aliases in the existing shared pricing calculator.
- Use standard input/cached-input/output rates of USD 10/1/50 per million tokens, Codex Fast/priority rates of 25/2.5/125, and Flex rates of 5/0.5/25.
- Do not introduce a long-context multiplier or cache-write charge for Codex Astra; leave other model tariffs and external model-source pricing unchanged.
- Backfill retained historical subscription Astra request costs and reconcile their persisted report aggregates without repeating upstream requests or losing other models' totals.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `api-keys`: Astra pricing, tier-aware estimates, and historical cost consistency across request logs and reports.

## Impact

Shared pricing data, pricing/accounting regression tests, and a data migration or existing maintenance path for retained historical request costs. No new dependency, routing changes, frontend redesign, deployment, or VPS changes.
