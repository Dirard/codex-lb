## ADDED Requirements

### Requirement: Dashboard edits the Antigravity client version

The Settings page SHALL show an Antigravity client section beside the Codex client section, with the version field and a Save button that sends only the version change.

#### Scenario: Saving the version
- **WHEN** the operator enters a valid version and saves
- **THEN** the dashboard sends only `antigravityClientVersion` in the settings update
