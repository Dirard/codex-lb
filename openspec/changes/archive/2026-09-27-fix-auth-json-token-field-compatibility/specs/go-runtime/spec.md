# Auth JSON token naming compatibility

- Last edited with skill pack: `0.2.2`

## ADDED Requirements

### Requirement: Account import accepts Codex and dashboard token field names

The authenticated account import SHALL accept `tokens.id_token`, `access_token`, `refresh_token` and optional `account_id` from standard Codex auth.json, as well as the existing `idToken`, `accessToken`, `refreshToken` and `accountId` aliases. File selection and JSON paste SHALL share this validation. Each required token SHALL resolve to a nonempty string; null or missing aliases SHALL supply no value, and an optional null account ID SHALL retain claim-derived identity behavior. A non-string field or two non-null aliases with different values SHALL reject the whole import before any account or credential changes. The runtime's exported `codexAuthJson` SHALL be importable without renaming fields and SHALL preserve account identity. Existing administrator authorization, size bounds, encryption and safe non-echoing errors SHALL remain enforced.

#### Scenario: Import a standard Codex document
- **WHEN** an administrator imports valid snake_case tokens by file or paste
- **THEN** the account and encrypted credentials are saved through the same import flow
- **AND** a supplied `account_id` is preserved by the existing account-identity rules

#### Scenario: Existing dashboard token aliases remain accepted
- **WHEN** an import uses camelCase aliases or both aliases with identical non-null values
- **THEN** it imports the same tokens without changing their contents

#### Scenario: Reject an ambiguous or incomplete token document
- **WHEN** required token values are missing/empty, a field has a non-string value, or aliases disagree
- **THEN** import returns the existing safe invalid-auth error without exposing tokens or mutating account state

#### Scenario: Reimport the runtime's Codex export
- **WHEN** an administrator imports an unchanged `codexAuthJson` export
- **THEN** the document is accepted and updates the same account rather than creating a duplicate
