## ADDED Requirements

### Requirement: Responses routes take precedence over Realtime call aliases

The fully composed runtime SHALL dispatch authenticated GET WebSocket upgrades and POST Responses requests on `/backend-api/codex/responses` and `/v1/responses`, including their single trailing-slash variants, to Responses handling. A Realtime call-ID wildcard MUST NOT interpret the reserved `responses` path as a call identifier. Existing key, firewall, capability and owner validation SHALL remain unchanged. Retained Realtime aliases SHALL continue to enforce key-scoped call ownership.

#### Scenario: Native Codex connects using the legacy base URL
- **WHEN** an authenticated client upgrades `/backend-api/codex/responses` with the Realtime routes also installed
- **THEN** the server accepts a Responses socket rather than returning `realtime_call_owner_not_found`
- **AND** validation of subsequent Responses frames uses the existing key and account policy

#### Scenario: A real call identifier uses the Realtime alias
- **WHEN** an authenticated client requests `/backend-api/codex/rtc_owned`
- **THEN** the server applies Realtime call ownership and does not treat that identifier as a Responses route
