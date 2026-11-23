## ADDED Requirements

### Requirement: Realtime always requires a real API key

Realtime call creation and both sideband path aliases SHALL authenticate an active user API key through Bearer authentication even when general API-key enforcement is disabled and the peer is locally trusted or explicitly allowed by an unauthenticated-client CIDR. They MUST NOT substitute the internal local principal or an administrator session. A call created with one key MUST remain inaccessible to another key. Ordinary local/CIDR keyless Responses behavior SHALL remain unchanged.

#### Scenario: A trusted keyless caller starts or attaches a call
- **WHEN** the caller provides no valid API key while general keyless access is enabled
- **THEN** Realtime returns 401 before creating a provider call or opening a WebSocket

#### Scenario: Another key attempts to attach
- **WHEN** a call is owned by key A and valid key B requests either sideband alias
- **THEN** the owner lookup fails closed without an upstream sideband connection
