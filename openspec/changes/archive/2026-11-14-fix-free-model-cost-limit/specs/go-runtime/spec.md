## ADDED Requirements

### Requirement: Zero-priced requests reserve on cost limits

A request whose reservation budget has input or output tokens but zero cost, such as a model priced at zero by a price override, SHALL hold one microdollar on every cost limit of its key and group while it runs, and settlement SHALL charge its actual cost. It MUST NOT be refused as a missing budget. A reservation with neither tokens nor cost SHALL still be refused as a missing budget.

#### Scenario: Auto-review on a cost-limited key
- **WHEN** a key whose group caps weekly cost sends `codex-auto-review`, which is priced at zero
- **THEN** the request reaches an account and is recorded
- **AND** after settlement the cost limit is back where it was before the request
