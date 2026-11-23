## ADDED Requirements

### Requirement: Optional Codex model catalog fields retain nullable wire semantics

The authenticated Codex model catalog, its client-version `/v1/models` alias and their trailing-slash equivalents SHALL encode absent `default_verbosity`, `default_reasoning_level` and `minimal_client_version` values as JSON null rather than empty strings. Recognized declared enum defaults and nonempty version values SHALL remain unchanged; unsupported default enum values SHALL be null rather than fabricated replacements. Catalog repair MUST NOT change stored model configuration, capabilities, prices, authorization or generation requests. Source-only forwarding overrides MUST remain excluded from published metadata.

#### Scenario: A source does not declare verbosity or reasoning defaults
- **WHEN** an authorized client retrieves its Codex catalog
- **THEN** the optional defaults are null and the catalog is usable by the supported Codex client without an invalid enum value
- **AND** no substitute verbosity or reasoning capability is invented

#### Scenario: A model declares supported defaults
- **WHEN** declared verbosity is low, medium or high and reasoning uses a recognized Codex effort
- **THEN** the catalog preserves those values and any nonempty minimum client version

#### Scenario: A default uses an unsupported enum value
- **WHEN** a catalog model contains an unknown default verbosity or reasoning enum
- **THEN** that optional field is null without discarding the model or changing its persisted configuration
