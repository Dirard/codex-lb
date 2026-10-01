# Key reports and account group editing

- Last edited with skill pack: `0.2.2`

## ADDED Requirements

### Requirement: API-key holders can view only their own read-only reports

The runtime SHALL provide `/key-reports` independently of administrator sign-in. A valid active, unexpired, non-internal API key SHALL authenticate report reads; an exhausted generation quota SHALL NOT prevent those reads. `GET /v1/usage/reports` and its trailing-slash equivalent SHALL bind every report aggregate, comparison period and model/client distribution to the authenticated key on the server, not to client-selected key/account identifiers. Unsupported scope selectors SHALL be rejected. The response SHALL expose only scoped summary, comparison, daily, model and user-agent aggregates, without account identities, credentials or diagnostic payloads. Report reads SHALL use no-store caching and MUST NOT consume key quota, create administrator sessions or grant mutation access. The page SHALL retain the key only in memory and authenticated request headers, never URLs, browser persistence or query-cache keys; logout SHALL cancel outstanding reads and discard credentials and report cache. Authorization failure SHALL hide previously loaded report data.

#### Scenario: Two keys use the same account
- **WHEN** a holder authenticates with key A while both A and B have requests on the same account
- **THEN** current totals, previous-period comparison, daily rows and distributions include only A
- **AND** changing query parameters cannot reveal B or upstream account identities

#### Scenario: Invalid credentials and read-only access
- **WHEN** credentials are absent, invalid, internal, disabled or expired, including from loopback with proxy authentication disabled
- **THEN** report access is rejected regardless of administrator cookies
- **AND** the report route accepts no mutation methods and the key does not authorize administrator APIs

#### Scenario: Logout or revoke the key
- **WHEN** the holder logs out or a later report request detects revocation/expiry
- **THEN** reports are no longer displayed and no previous session's reports can be shown after signing in with a different key

### Requirement: Account cards can assign multiple groups atomically

The administrator SHALL be able to select multiple groups in an account card and save the membership set once. The authenticated write endpoint SHALL require an explicit group ID array with at most 1000 entries, accept an empty array to remove memberships and reject missing/null, duplicate, empty or unknown identifiers. The existing live account and every target group SHALL be validated before committing all membership changes in one transaction. Updates MUST NOT modify other accounts' memberships, group limits, group-to-key bindings, key consumption or reservations. The account card SHALL distinguish loading/error from empty membership, prevent writes while read-only/busy and reset unsaved selections when switching accounts. Failed saves SHALL keep the editable draft without unhandled rejections; successful saves SHALL return to the current server-backed membership state.

#### Scenario: Add one account to several groups
- **WHEN** the administrator selects two groups and saves the account card
- **THEN** the account belongs to both groups after one atomic update
- **AND** keys of both groups use the updated existing scope rules without changing their limits or consumption

#### Scenario: Validation failure or removal
- **WHEN** an update contains an unknown group or account
- **THEN** no membership is changed
- **WHEN** the administrator submits an explicit empty group list for an existing account
- **THEN** only that account's memberships are removed, preserving all accounts and groups
