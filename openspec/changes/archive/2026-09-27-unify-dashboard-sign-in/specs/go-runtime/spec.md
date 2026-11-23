# Shared sign-in for administrators and key holders

- Last edited with skill pack: `0.2.2`

## MODIFIED Requirements

### Requirement: API-key holders can view only their own read-only reports

The dashboard sign-in screen SHALL offer administrator and API-key report access at the same entry point, without a separate key-login page. The former `/key-reports` entry SHALL redirect to the common dashboard entry. Administrator password/TOTP, bootstrap and explicitly configured authentication modes SHALL retain their existing authorization rules. A valid active, unexpired, non-internal API key SHALL authenticate report reads without administrator authentication; an exhausted generation quota SHALL NOT prevent those reads. `GET /v1/usage/reports` and its trailing-slash equivalent SHALL bind every report aggregate, comparison period and model/client distribution to the authenticated key on the server, not to client-selected key/account identifiers. Unsupported scope selectors SHALL be rejected. The response SHALL expose only scoped summary, comparison, daily, model and user-agent aggregates, without account identities, credentials or diagnostic payloads. Report reads SHALL use no-store caching and MUST NOT consume key quota, create administrator sessions or grant mutation access. The page SHALL retain the key only in memory and authenticated request headers, never URLs, browser persistence or query-cache keys; logout SHALL cancel outstanding reads and discard credentials and report cache. Authorization failure SHALL hide previously loaded report data and return to the common sign-in screen. Switching sign-in methods SHALL clear the abandoned credential draft and cancel pending key authentication. Key-report mode MUST NOT mount the administrator navigation or issue administrator data requests; the shared public authentication-session read MAY determine the available sign-in flow.

#### Scenario: Choose a sign-in method on the same screen
- **WHEN** an unauthenticated visitor opens the dashboard
- **THEN** administrator and API-key sign-in methods are available without leaving the common screen
- **AND** a successful key login shows only its read-only report, while password login retains the administrator and TOTP flows
- **AND** an old `/key-reports` bookmark reaches the common entry instead of a separate key form

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
- **THEN** reports are no longer displayed and the common login offers an empty key field
- **AND** no previous session's reports can be shown after signing in with a different key

#### Scenario: Switch methods during key authentication
- **WHEN** the visitor changes to administrator sign-in while a key check is pending
- **THEN** the key request is cancelled and its late completion cannot open a report
- **AND** switching back does not restore the abandoned key draft
