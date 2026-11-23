## ADDED Requirements

### Requirement: Models added to a source take the shown settings

The model source edit dialog SHALL apply the settings it shows (pricing, context window, max output tokens, streaming, tools, vision and reasoning) to every model added to the model list, while models that already existed keep their own values for fields the operator did not change.

#### Scenario: Adding a model
- **WHEN** the operator adds a model to an existing source and saves without touching the pricing fields
- **THEN** the added model is saved with the prices the dialog shows
