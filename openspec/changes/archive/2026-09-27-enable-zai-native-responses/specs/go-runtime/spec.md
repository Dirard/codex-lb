# Z.AI native Responses

- Last edited with skill pack: `0.2.2`

## MODIFIED Requirements

### Requirement: Integrate external providers without losing Codex semantics

The server SHALL support configured ChatGPT and external API accounts, including Z.AI and OpenAI-compatible sources using Chat Completions or native Responses. Its integrated adapter SHALL preserve supported tool calls/results, reasoning, streaming, usage, model mappings and continuation state. Unsupported capabilities MUST be rejected explicitly before dispatch rather than dropped or invented. Provider credentials, account scopes and custom pricing MUST remain isolated. Cross-provider switching MUST NOT occur implicitly solely because another provider has available quota. Z.AI source creation and editing SHALL allow either or both supported protocols, require at least one, and retain rejection of audio and embeddings. The configured Responses capability SHALL select native Responses without Chat-only GLM customization; existing Chat-only sources and defaults SHALL remain unchanged. The dashboard MUST NOT replace the operator's endpoint or credentials when selecting a protocol.

#### Scenario: Codex tool round trip through a Chat Completions source
- **WHEN** Codex receives a translated tool call and sends its matching output on the next turn
- **THEN** the adapter reconstructs valid provider history and returns correctly correlated Responses events
- **AND** the exchange does not require an external relay process

#### Scenario: Direct Chat Completions obey declared model capabilities
- **WHEN** a direct source-routed Chat request requires streaming, tools, images or active reasoning that its configured model does not support
- **THEN** the server rejects it with HTTP 400 before upstream dispatch and releases its unused budget without an uncertain usage reservation
- **AND** explicit reasoning effort must be supported by model metadata, while empty tools and disabled controls alone do not require capabilities

#### Scenario: Administrator selects native Responses for Z.AI
- **WHEN** the administrator creates or edits a Z.AI source with Responses enabled and a corresponding base URL
- **THEN** the dashboard and API preserve that protocol selection and the supplied endpoint
- **AND** Responses-only configuration is accepted while a source with no supported protocol or audio/embeddings is rejected

#### Scenario: Native Z.AI responses preserve the provider contract
- **WHEN** a permitted request uses a Z.AI source with Responses enabled
- **THEN** JSON and streaming requests use the native Responses endpoint, even if Chat is also enabled
- **AND** supported tools, reasoning, model mapping and usage remain intact without injecting Chat-only thinking, messages or stream options

#### Scenario: Existing Z.AI Chat source remains compatible
- **WHEN** an existing Z.AI source has Chat enabled and Responses disabled
- **THEN** it continues using Chat Completions with the existing GLM thinking behavior
- **AND** upgrading alone does not modify the source endpoint, credentials or protocol flags
