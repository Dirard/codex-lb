## MODIFIED Requirements

### Requirement: API-key holders can view only their own read-only reports

The dashboard sign-in screen SHALL offer administrator and API-key report access at the same entry point, without a separate key-login page. The former `/key-reports` entry SHALL redirect to the common dashboard entry. Administrator password/TOTP, bootstrap and explicitly configured authentication modes SHALL retain their existing authorization rules. A valid active, unexpired, non-internal API key SHALL authenticate report reads without administrator authentication; an exhausted generation quota SHALL NOT prevent those reads. `GET /v1/usage/reports` and its trailing-slash equivalent SHALL bind every traffic aggregate, comparison period and model/client distribution to the authenticated key on the server, not to client-selected key/account identifiers. Unsupported scope selectors SHALL be rejected. The response SHALL expose only scoped traffic reports, the caller's current personal limits, and the separately specified safe same-group limit summaries, without account identities, credentials or diagnostic payloads. Report reads SHALL use no-store caching and MUST NOT consume key quota, mutate limit counters or reservations, create administrator sessions or grant mutation access. The page SHALL retain the key only in memory and authenticated request headers, never URLs, browser persistence or query-cache keys; logout SHALL cancel outstanding reads and discard credentials and report cache. Authorization failure SHALL hide previously loaded report data and return to the common sign-in screen. Switching sign-in methods SHALL clear the abandoned credential draft and cancel pending key authentication. Key-report mode MUST NOT mount the administrator navigation or issue administrator data requests; the shared public authentication-session read MAY determine the available sign-in flow.

#### Scenario: Choose a sign-in method on the same screen
- **WHEN** an unauthenticated visitor opens the dashboard
- **THEN** administrator and API-key sign-in methods are available without leaving the common screen
- **AND** a successful key login shows only its read-only report and permitted limit summaries, while password login retains the administrator and TOTP flows
- **AND** an old `/key-reports` bookmark reaches the common entry instead of a separate key form

#### Scenario: Two keys use the same account
- **WHEN** a holder authenticates with key A while both A and B have requests on the same account
- **THEN** current traffic totals, previous-period comparison, daily rows and distributions include only A
- **AND** changing query parameters cannot reveal B's report or upstream account identities
- **AND** B's personal limit summary is visible only when B belongs to A's current group

#### Scenario: Invalid credentials and read-only access
- **WHEN** credentials are absent, invalid, internal, disabled or expired, including from loopback with proxy authentication disabled
- **THEN** report access is rejected regardless of administrator cookies
- **AND** the report route accepts no mutation methods and the key does not authorize administrator APIs

#### Scenario: Logout or revoke the key
- **WHEN** the holder logs out or a later report request detects revocation/expiry
- **THEN** reports and limit summaries are no longer displayed and the common login offers an empty key field
- **AND** no previous session's reports or group summaries can be shown after signing in with a different key

#### Scenario: Switch methods during key authentication
- **WHEN** the visitor changes to administrator sign-in while a key check is pending
- **THEN** the key request is cancelled and its late completion cannot open a report
- **AND** switching back does not restore the abandoned key draft

## ADDED Requirements

### Requirement: Key reports show current personal and same-group limit usage

Key-report access SHALL show the caller's configured effective personal limits, with consumed and maximum values and the applicable type, window and model filter, when limits exist. A grouped caller SHALL also see a separate read-only mini-list of all non-deleted, non-internal keys in its current group, including itself, with name, enabled/expiry state and the maximum consumed percentage among each key's personal limits. Keys without limits SHALL be distinguished from keys at zero usage. Limits SHALL use the same per-key ledger counters as the administrator key page, including held reservations; they SHALL remain independent of historical report date/model filters. Expired limit windows SHALL display zero usage and an advanced reset boundary without mutating storage. Over-limit values MUST NOT be reduced to the maximum, though progress bars MAY be visually clamped. The report SHALL explain that these are current-window counters including reserved budget. Same-group visibility SHALL be derived from the current authenticated key membership at the authorized snapshot read, not shared upstream accounts or a client-selected group. Peer summaries MUST NOT expose key secrets, hashes, prefixes, account assignments, upstream quotas or traffic details. Disabled/expired peers SHALL remain labeled as such; ungrouped callers SHALL receive no group summary.

#### Scenario: Personal limits with or without a group
- **WHEN** the authenticated key has configured limits, including effective group-provided limits
- **THEN** the report shows that key's own consumed and maximum amounts
- **AND** changing report dates does not redefine its limit windows or combine peer consumption

#### Scenario: Group mini-statistics
- **WHEN** the caller belongs to a group with several keys
- **THEN** the report shows each non-deleted, non-internal member's personal limit usage using the administrator key list's percentage convention
- **AND** it provides no key editing, peer report navigation or access to other groups

#### Scenario: Group membership changes
- **WHEN** a key joins, leaves or changes groups before the authorized snapshot read
- **THEN** the returned mini-list follows the new membership and does not reveal the previous group
- **AND** revoked, expired or deleted callers cannot retrieve any summary

#### Scenario: Current, expired and held budgets
- **WHEN** limits have active counters, held reservations, over-limit usage or elapsed windows
- **THEN** current-window values reflect the existing ledger without resetting or settling any stored value
- **AND** an absent limit is not presented as zero consumption

### Requirement: Key group reports expose aggregate account subscription usage

A grouped key's report SHALL include a separate aggregate percentage of used subscription quota across the group's non-deleted ChatGPT accounts when upstream-quota visibility is permitted by the global setting and the key's account-pool visibility setting. The group and its account membership MUST be derived in the same authorized read snapshot as the peer key summary. Each account SHALL contribute at most once per window regardless of membership in other groups. The percentage SHALL equal total estimated used subscription credits divided by total known subscription capacity for that window. Primary, weekly and monthly windows MUST NOT be combined. Missing, expired or unknown-capacity observations MUST NOT be treated as zero consumption; the report SHALL identify the number of contributing accounts and total subscription accounts so partial coverage is explicit. Unknown windows SHALL display unavailable rather than zero. Purchased credits MUST NOT be included in this subscription percentage or treated as exhausted when subscription usage reaches 100%. Only aggregate percentages and counts SHALL be exposed, never provider account identities, individual balances or credentials. Ungrouped callers and callers with hidden upstream quotas SHALL receive no such aggregate.

#### Scenario: Accounts with different capacities
- **WHEN** a group contains a Plus account at 100% weekly usage and a Pro account at 0%
- **THEN** weekly usage is weighted by their existing subscription-credit capacities, not the arithmetic average of percentages
- **AND** accounts outside the group cannot affect the result

#### Scenario: Missing or hidden quota information
- **WHEN** only some group accounts report a known current window or upstream visibility is disabled
- **THEN** partial observations identify their coverage, absent observations are unavailable, and hidden aggregates remain absent
