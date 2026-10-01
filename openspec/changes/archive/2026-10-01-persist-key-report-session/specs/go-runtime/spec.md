## MODIFIED Requirements

### Requirement: API-key holders can view only their own read-only reports

The dashboard sign-in screen SHALL offer administrator and API-key report access at the same entry point, without a separate key-login page. The former `/key-reports` entry SHALL redirect to the common dashboard entry. Administrator password/TOTP, bootstrap and explicitly configured authentication modes SHALL retain their existing authorization rules. A valid active, unexpired, non-internal API key SHALL authenticate report reads without administrator authentication; an exhausted generation quota SHALL NOT prevent those reads. `GET /v1/usage/reports` and its trailing-slash equivalent SHALL remain bearer-only and bind every traffic aggregate, comparison period and model/client distribution to the authenticated key on the server, not to client-selected key/account identifiers. Unsupported scope selectors SHALL be rejected. The response SHALL expose only scoped traffic reports, the caller's current personal limits, and the separately specified safe same-group limit summaries, without account identities, credentials or diagnostic payloads. Report reads SHALL use no-store caching and MUST NOT consume key quota, mutate limit counters or reservations, create administrator sessions or grant mutation access. Key-report mode MUST NOT mount the administrator navigation or issue administrator data requests; public authentication-session reads MAY determine the available sign-in flow.

Browser sign-in SHALL exchange the API key for a separate authenticated-encrypted HttpOnly report-session cookie, never persist the original API key in cookies, URLs, local/session storage or query caches. The cookie SHALL be host-only, restricted to report-session routes, SameSite protected and Secure on HTTPS using the existing trusted-proxy policy. Its lifetime SHALL use the configured dashboard session lifetime with the existing remote cap, bounded by the key expiry. Reloading the page or restarting the server with the same persistent encryption key SHALL restore an unexpired report session without re-entering the API key. Report cookies SHALL authorize only cookie-specific report routes and MUST NOT substitute for bearer credentials or administrator authentication. Session creation and logout SHALL retain cross-origin protection; creation and report reads SHALL retain proxy IP allowlist enforcement. The server SHALL revalidate the current key's active/expiry/internal status and credential fingerprint on every session/report read.

Successful logout SHALL delete the report cookie, cancel outstanding reads, clear the report cache and return to an empty common login form without altering administrator cookies. Revocation, deletion, credential rotation or expiry SHALL hide previously loaded report data on the next authorization check and require signing in again. Network and server failures SHALL show a retryable error without clearing the session or treating the failure as a logout. Switching sign-in methods SHALL clear abandoned credential drafts; method switching SHALL be disabled while a session-creation request is pending so a late response cannot create an abandoned login.

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
- **AND** a report cookie alone cannot authenticate bearer report or generation routes

#### Scenario: Reload and restart keep report access
- **WHEN** a signed-in holder reloads the dashboard before session expiry, including after a server restart retaining its data and encryption key
- **THEN** only that holder's report surface is restored without another API-key entry
- **AND** subsequent report requests use the restricted cookie, not the original API key

#### Scenario: Logout or revoke the key
- **WHEN** the holder successfully logs out or a later session/report check detects key revocation, deletion, credential rotation or expiry
- **THEN** reports and limit summaries are no longer displayed and the common login offers an empty key field
- **AND** a page reload does not restore logged-out access
- **AND** no previous session's reports or group summaries can be shown after signing in with a different key

#### Scenario: Transient failure is not logout
- **WHEN** session restoration, report loading or logout fails due to a network or server error
- **THEN** the UI shows a retryable error without discarding the report session or claiming successful logout

#### Scenario: Switch sign-in methods safely
- **WHEN** a visitor changes sign-in methods before submitting credentials
- **THEN** the abandoned draft is cleared
- **WHEN** a key session-creation request is pending
- **THEN** method switching is disabled until it completes

#### Scenario: Firewall and cross-origin protections remain effective
- **WHEN** a disallowed client tries to create a report session or read reports
- **THEN** the request is denied without bypassing the proxy allowlist
- **AND** session status and logout remain usable without granting report data or blocking administrator repair
- **WHEN** a cross-origin browser request attempts session creation or logout
- **THEN** it is rejected
