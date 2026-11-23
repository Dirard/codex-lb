## ADDED Requirements

### Requirement: Withdrawn Antigravity sources are retired on upgrade

An upgrade from a schema that may contain model sources of kind `antigravity` SHALL delete each such source and its account through the same boundary as an operator deletion, removing the account's credential (the Google refresh token), scopes and conversation owners, and SHALL keep the recorded usage history. Other model sources and accounts MUST NOT change. The runtime MUST NOT route requests to, list, or call Google for such a source.

#### Scenario: Upgrading an installation that used Antigravity
- **GIVEN** an installation at schema 54 with an active Antigravity source and another model source
- **WHEN** it upgrades
- **THEN** the Antigravity source is deleted and its refresh token is gone, the other source is unchanged and usage history stays
