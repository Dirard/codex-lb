## ADDED Requirements

### Requirement: GPT-6 Astra Codex cost estimates

For subscription-backed Codex requests, the shared request-log, API-key reservation, settlement, and aggregate cost calculator MUST recognize `gpt-6-astra` and its versioned aliases. The USD-equivalent input, cached-input, and output rates per million tokens SHALL be `10 / 1 / 50` for standard requests, `25 / 2.5 / 125` for Codex `priority` or `fast`, and `5 / 0.5 / 25` for `flex`. The authoritative upstream service tier SHALL retain precedence over the requested tier. This Codex estimate MUST NOT apply a context-length surcharge, including above 272,000 input tokens, or a separate cache-write charge. It MUST NOT override external model-source tariffs or serve as a fallback for an unpriced external source. Other model prices MUST remain unchanged. Batch pricing SHALL NOT be inferred without corresponding request fields.

#### Scenario: Astra standard usage above the API long-context boundary
- **WHEN** a standard subscription-backed Codex `gpt-6-astra` request reports 300,000 input tokens, including 50,000 cached input tokens, and 100,000 output tokens
- **THEN** the estimated cost is USD 7.55 without a long-context multiplier

#### Scenario: Astra Fast usage preserves the Codex multiplier
- **WHEN** the same usage is reported with the `priority` or `fast` service tier
- **THEN** the estimated cost is USD 18.875

#### Scenario: Astra Flex usage remains independent of context length
- **WHEN** the same usage is reported with `flex`
- **THEN** the estimated cost is USD 3.775

#### Scenario: Versioned Astra aliases resolve without a generic GPT-6 fallback
- **WHEN** the model is `gpt-6-astra-2026-09-01`
- **THEN** it resolves to the canonical Astra rates
- **AND** an unrelated unconfigured GPT-6 model does not inherit the Astra price

#### Scenario: External Astra sources do not inherit subscription pricing
- **WHEN** a request uses an external model source with the `gpt-6-astra` slug, including above 272,000 input tokens
- **THEN** recorded cost and API-key settlement use that source's configured prices
- **AND** an unpriced source retains the existing zero-cost behavior instead of inheriting the subscription estimate

### Requirement: Historical Astra costs appear in retained reports

An upgrade introducing Astra pricing SHALL price retained subscription Astra request logs with sufficient recorded usage and reconcile the corresponding persisted report totals. It MUST preserve other models, request metadata, tokens, explicit model-source costs, and already pruned historical contributions. Re-running the correction MUST NOT add costs twice. Requests with incomplete usage SHALL NOT receive invented token values. Correcting the history MUST NOT send upstream requests or retrospectively consume a client's active API-key limit budget. Historical reservation records and settled key counters SHALL retain their admission-time semantics; new requests SHALL use the Astra tariff.

#### Scenario: Previously unpriced Astra request
- **WHEN** a retained subscription Astra request has sufficient usage but no recorded cost
- **THEN** its request detail and applicable report totals include the newly calculated Astra cost after upgrade

#### Scenario: Folded history and a live tail
- **WHEN** retained Astra requests exist both before and after the report fold watermark
- **THEN** the stored folded contribution is corrected and the live-tail contribution is read from corrected logs
- **AND** the combined report counts each correction once

#### Scenario: Explicit source pricing and incomplete usage remain unchanged
- **WHEN** Astra-named model-source requests have explicit provider pricing or a subscription request lacks sufficient usage
- **THEN** the historical correction does not overwrite those prices or invent a cost for the incomplete request
