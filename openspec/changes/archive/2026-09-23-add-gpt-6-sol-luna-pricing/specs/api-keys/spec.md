## ADDED Requirements

### Requirement: GPT-6 Sol and Luna Codex cost estimates

The built-in subscription pricing calculator MUST recognize `gpt-6-sol` and `gpt-6-luna` case-insensitively, including their hyphen-suffixed aliases, without a generic GPT-6 fallback. Standard input/cached-input/output prices per million tokens SHALL be USD `2/0.2/10` for Sol and `0.1/0.01/0.5` for Luna. Above 272,000 input tokens, the full request SHALL use 2x input/cached-input and 1.5x output rates. Codex `fast` and `priority` SHALL use 2.5x the applicable Standard rates, including long-context rates; `flex` SHALL use half the applicable Standard rates. Cached tokens MUST be subtracted from ordinary billable input. Authoritative upstream tier precedence, explicit external-source costs, and other built-in models SHALL retain their existing behavior. No separate Codex cache-write charge SHALL be added. Existing persisted historical costs and settled enforcement counters MUST NOT be rewritten by this change.

#### Scenario: Standard prices below the context boundary
- **WHEN** a Sol or Luna request reports 200,000 input tokens including 50,000 cached tokens and 100,000 output tokens
- **THEN** its cost is USD 1.31 for Sol or USD 0.0655 for Luna

#### Scenario: Context boundary and tier composition
- **WHEN** a Sol request reports 272,000 input tokens including 50,000 cached tokens and 100,000 output tokens
- **THEN** the Standard cost is USD 1.454
- **WHEN** its input rises to 300,000 tokens with the same cached and output usage
- **THEN** the Standard, Fast/priority and Flex costs are USD 2.52, USD 6.30 and USD 1.26 respectively
- **AND** Luna with the 300,000-token usage costs USD 0.126, USD 0.315 and USD 0.063 respectively

#### Scenario: Snapshot aliases and unrelated names
- **WHEN** the model is `GPT-6-SOL` or `gpt-6-luna-2026-09-22`
- **THEN** it uses the corresponding canonical model price
- **AND** `gpt-6`, `gpt-6-solar`, and `gpt-6-lunar` do not inherit these prices

#### Scenario: Actual upstream tier controls persisted cost and settlement
- **WHEN** a client requests priority but upstream reports default for a Sol or Luna completion
- **THEN** the request log, key usage summary and settled cost limit reflect Standard rather than Fast pricing

#### Scenario: External sources remain independent
- **WHEN** Sol or Luna is routed through an external model source
- **THEN** its configured source price remains authoritative
- **AND** an unpriced source retains the existing zero-cost behavior instead of inheriting the subscription tariff
