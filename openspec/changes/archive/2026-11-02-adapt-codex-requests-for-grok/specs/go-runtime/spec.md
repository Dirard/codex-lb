## MODIFIED Requirements

### Requirement: A Grok subscription account can back a model source

A model source of kind `grok_subscription` SHALL name an existing Grok video-pool account in `providerConfig.grokAccountId`, SHALL use the base URL `https://api.x.ai/v1`, SHALL take no API key and SHALL support only Responses and Chat Completions. A create or update request that gives an API key, another base URL, no account or an unknown account SHALL be rejected with 422 and change nothing, and a source of another kind SHALL NOT carry `grokAccountId`. A create request of this kind without models SHALL fill them from the account's `GET https://api.x.ai/v1/language-models`. Each listed id SHALL become an enabled model with xAI's aliases, with input, cached-input and output prices converted from 1e-4 USD to USD per 1M tokens, with vision when the input modalities include `image`, with streaming and tools on, and with a context window of 256000. A failed read SHALL answer 502 `grok_models_unavailable` and create nothing. Explicit models SHALL be stored as given. Responses SHALL default to on and Chat Completions to off.

A request routed to such a source SHALL carry the named account's current access token, obtained through the video pool's guarded refresh, and SHALL be sent only to the provider's configured xAI base URL. The token MUST NOT be stored in the source, returned by an API or logged. When the account is missing, needs a new sign-in or was denied access, the request SHALL fail before dispatch with `grok_account_unavailable`, with no upstream call and no charge. Pausing the account on the Video page SHALL NOT stop its text source. An upstream 401 that arrives before any output, usage or client-visible event SHALL be retried once on the same account with a token newer than the rejected one. Every other outcome SHALL follow the external-source rules, and the request MUST NOT move to another account. Changing `grokAccountId` SHALL retire the source's routing identity as a base URL change does. Catalog visibility, key scopes, model allowlists, pricing, reservation and settlement, concurrency, timeouts and enable/disable SHALL apply exactly as to other model sources.


A Responses request to such a source SHALL be adapted to what xAI accepts:
- Each child function of a `namespace` tool SHALL be declared as a function named `<namespace>-<child>`. A name longer than 64 characters SHALL be cut to 55 characters followed by `-` and 8 hexadecimal characters of its SHA-256. A history `function_call` with a `namespace` SHALL carry the same flat name and no `namespace`. Every answer `function_call` with such a name SHALL reach the client with the child name and its `namespace`. A child that is not a function, or two declarations with the same flat name, SHALL be rejected before dispatch.
- A `web_search` tool SHALL keep only `type`, `filters`, `enable_image_understanding` and `enable_image_search`, and one with `external_web_access: false` SHALL be left out. The hosted `web_search` tool SHALL be allowed for this kind.
- `reasoning.effort` SHALL be fitted to the model. `grok-4.7` and `grok-4.6` take `low` to `xhigh`, and `grok-4.5` takes `low` to `high`. A higher effort SHALL be sent as the model's highest and `none` or `minimal` as `low`. For other models the `reasoning` object SHALL be left out.
- Other fields SHALL be forwarded unchanged.
- New sources SHALL advertise those efforts in the catalog, with default `high`.
#### Scenario: The signed-in account becomes a text source

- **WHEN** an administrator creates a `grok_subscription` source for a signed-in Grok account without models
- **THEN** the source lists the account's xAI text models with their aliases and converted prices, and stores no API key

#### Scenario: A Codex request uses the account's token

- **WHEN** an allowed key sends a Responses request for one of the source's models
- **THEN** xAI receives it at `/v1/responses` with the account's current access token, the client receives xAI's stream, and usage settles at the source's prices

#### Scenario: A rejected token is renewed once

- **WHEN** xAI answers 401 before any output
- **THEN** the request is sent once more with a refreshed token of the same account, and a second 401 is returned without a third attempt

#### Scenario: An account that lost its session

- **WHEN** the named account needs a new sign-in
- **THEN** the request fails with `grok_account_unavailable` before any upstream call and nothing is charged

#### Scenario: The subscription token stays on xAI's endpoint

- **WHEN** a create or update request gives a base URL other than `https://api.x.ai/v1` or an API key for this kind
- **THEN** it is rejected with 422 and the source is unchanged

#### Scenario: Codex CLI request with MCP tools and its own reasoning settings

- **WHEN** Codex sends `grok-4.7` a request with `namespace` tools, `{"type":"web_search","external_web_access":true}` and `reasoning: {effort: "max", summary: "detailed"}`
- **THEN** xAI receives flat function names, `{"type":"web_search"}` and effort `xhigh` with the summary
- **AND** a call xAI makes to a flat name reaches the client as a `function_call` with the child name and its `namespace`

#### Scenario: Model without reasoning effort

- **WHEN** a request for `grok-build-0.1` carries `reasoning`
- **THEN** xAI receives no `reasoning` object
