## ADDED Requirements

### Requirement: Subscription requests use a consistent supported Codex client identity

By default, subscription model discovery and subscription provider requests SHALL identify as Codex `0.156.0`. Catalog query and User-Agent versions SHALL agree with the provider Version and User-Agent headers, including HTTP and WebSocket Responses and administrative probes. An authenticated administrator SHALL be able to change the persisted `codexClientVersion` in Settings without rebuilding or restarting. The change SHALL apply to subsequent HTTP requests and new WebSocket handshakes and enqueue a free catalog refresh; existing streams and connection-bound continuations MUST NOT be terminated or reassigned to apply the version. Each request SHALL snapshot one version for its related headers. Explicit adapter client-version overrides SHALL remain honored when no runtime resolver is wired. Updating this identity MUST NOT rewrite the requested model, retry a failed generation, modify account credentials or release unknown usage reservations.

The setting SHALL accept a trimmed numeric major.minor.patch version with an optional alphanumeric/dot/hyphen prerelease suffix, limited to 64 characters. Empty, null, malformed and non-string values SHALL be rejected without a partial write. Existing administrator authentication, cross-origin protection and settings revision checks SHALL apply. The value SHALL survive restart; an omitted update field SHALL retain the previous value. Failure to read or validate the runtime version MUST NOT silently dispatch with a stale fallback identity.

#### Scenario: Default Luna probe reaches a version-gated upstream
- **WHEN** an authenticated administrator probes without an explicit model and the upstream accepts Luna for client version `0.156.0`
- **THEN** the catalog and the single pinned probe request identify with that version and the probe uses `gpt-6-luna`
- **AND** the existing actual-usage settlement rules apply without a fallback model

#### Scenario: An adapter has an explicit version override
- **WHEN** a catalog or provider adapter is constructed with a nonempty client version and no runtime resolver
- **THEN** the adapter sends that version rather than the default

#### Scenario: Change client identity without restarting
- **WHEN** the administrator saves another valid version in Settings
- **THEN** the same running instance uses it for its next HTTP probe/catalog request and new WebSocket handshake
- **AND** an already established connection keeps its existing continuation state
- **AND** the setting remains saved after a service restart

#### Scenario: Invalid or unauthorized update
- **WHEN** an update is unauthenticated, cross-origin, malformed or has a stale expected revision
- **THEN** it is rejected and the previous client version and other settings remain unchanged
