## ADDED Requirements

### Requirement: Dashboard creates a Grok subscription model source

The model source dialog SHALL offer the provider `Grok subscription`. For it, creation SHALL ask only for a name, one of the Grok accounts listed by `GET /api/video/accounts`, a timeout and a concurrency limit, and SHALL send `kind: grok_subscription`, `providerConfig.grokAccountId`, the base URL `https://api.x.ai/v1`, Responses on and no models or API key. Editing such a source SHALL hide the API key field and keep the base URL read-only. The model source list SHALL show the account's label or e-mail. Texts SHALL be localized in `en`, `ko` and `zh-CN`.

#### Scenario: Operator adds the signed-in Grok account as a text source

- **WHEN** an operator chooses `Grok subscription`, picks a signed-in account and submits a name
- **THEN** the dashboard calls `POST /api/model-sources/` with the kind, the account id, the fixed base URL, Responses on and no models or key
- **AND** the new source appears in the list with the account's label or e-mail

#### Scenario: Editing keeps the fixed endpoint

- **WHEN** an operator edits a Grok subscription source
- **THEN** no API key field is shown and the base URL cannot be changed
