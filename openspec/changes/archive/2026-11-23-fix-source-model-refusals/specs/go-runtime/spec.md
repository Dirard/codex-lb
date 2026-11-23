## ADDED Requirements

### Requirement: An unserved model is not reported as unpriced

A model source whose enabled models and aliases do not include the requested model SHALL NOT be a candidate for the request and SHALL NOT count as an unpriced candidate. When no permitted account serves the model, the request SHALL fail with 404 `model_not_available` over HTTP and as a WebSocket error frame. `model_not_priced` SHALL be returned only when a permitted account serves the model but has no price for it.

#### Scenario: Only a source is available and it serves other models
- **GIVEN** a key whose only available account is a model source that serves `glm-5.3`
- **WHEN** the client requests `gpt-6-luna`
- **THEN** it receives 404 `model_not_available`, not `model_not_priced`
