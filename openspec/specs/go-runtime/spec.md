# go-runtime Specification

- Last edited with skill pack: `0.2.2`

## Purpose

Define the independently maintained, single-process Go codex-lb runtime, its selected compatibility boundary, financial and ownership safeguards, administrator controls, and safe offline migration from the preserved legacy implementation.

## Requirements

### Requirement: Preserve the legacy implementation during in-repository rewrite

The rewrite SHALL preserve the existing Git repository, required licenses and attribution. The temporary legacy source copy SHALL remain recoverable until the administrator authorizes its removal. After explicit removal approval, the active checkout and release source SHALL contain the Go implementation without requiring an in-tree Python/Rust runtime. Source cleanup MUST NOT change an installed server, delete its runtime data or expose ignored credentials/build data to version control. The Go server SHALL run as one binary without invoking Python, Rust, Node or an external codex-relay process. The supported offline legacy-data import SHALL remain implemented in Go.

#### Scenario: Modified files are relocated
- **WHEN** the legacy source tree is moved during the rewrite
- **THEN** modified and untracked implementation files retain their contents until approved removal
- **AND** `.git` and active planning instructions remain available
- **AND** credentials, databases and build caches remain excluded from commits and binary assets

#### Scenario: Administrator retires the old implementation
- **WHEN** the administrator approves removal of the temporary legacy source tree after rewrite verification
- **THEN** the old tree is removed from the active checkout recoverably and no longer belongs to the release source
- **AND** required licenses, Git history, installed data and the running service are preserved
- **AND** building and testing the Go implementation does not require the removed source directory

### Requirement: Key group reports include remaining purchased credit totals

When the group's upstream account-quota summary is visible, the key report SHALL also show the group's remaining Purchased credits separately from subscription usage percentages. Each non-deleted group ChatGPT account SHALL contribute once regardless of its quota-window count or membership in other groups. Known finite remaining balances SHALL be summed without treating an unknown balance as zero; negative remaining balances SHALL contribute zero available credits. An explicit zero SHALL remain distinguishable from unavailable data. Any explicit unlimited-credit account SHALL make the purchased-credit display Unlimited. The number of accounts with known balance/unlimited evidence SHALL be reported so incomplete coverage is explicit. These totals SHALL obey the same authenticated current-group scope and upstream-visibility policy as subscription summaries. Only group totals/counts SHALL be exposed, never individual provider balances or credentials. Reading the report MUST NOT purchase, redeem, reserve, settle or alter credits.

#### Scenario: Mixed known and unknown balances
- **WHEN** two group accounts have finite balances of 10.5 and 25.25, one has zero and two have no balance data
- **THEN** the report shows 35.75 purchased credits remaining with three known balances out of five accounts
- **AND** unrelated accounts and repeated window rows do not change that sum

#### Scenario: Unlimited or unavailable credits
- **WHEN** a group account explicitly has unlimited credits or all group credit balances are unknown
- **THEN** the report displays Unlimited for the former and unavailable for the latter, never an invented zero

### Requirement: Release standalone Linux binaries from the matching source

A published Go release SHALL identify its source commit and include statically linked Linux amd64 and arm64 server artifacts with the embedded dashboard, required licenses and SHA-256 checksums. Release version metadata MUST distinguish the Go version from retired legacy releases. The release SHALL retain the existing offline import and data-backup requirements. Publishing release artifacts MUST NOT implicitly update a running installation or migrate production data.

#### Scenario: Download a Go release for a VPS
- **WHEN** an operator downloads and verifies the matching Linux architecture artifact
- **THEN** the package contains the server, embedded dashboard and license texts without requiring a Python, Rust or Node runtime
- **AND** the reported version matches the release tag and checksums cover the downloadable packages

### Requirement: Deliver selected functionality before cutover

The Go implementation SHALL provide the selected account/OAuth, many-to-many group, API-key, per-key limit, model, quota, price, report, admin-security, firewall, Codex Responses/file/image/voice, warmup/scheduled-ping and reset-credit functionality before production cutover. It SHALL NOT provide upstream install telemetry, multi-replica runtime coordination, the quota phase planner, guest dashboard access, PostgreSQL runtime or proxy pools. Binary/systemd and optional ordinary Docker delivery SHALL replace Helm/Kubernetes, Nix and distroless-specific delivery. Local diagnostics and usage statistics MUST remain available independently of removed telemetry.

#### Scenario: Images used by Codex are retained
- **WHEN** Codex submits supported image input or uses its built-in image generation workflow
- **THEN** the proxy preserves that capability on a capable provider
- **AND** Codex image generation and editing reach a subscription account through the image generation routes

#### Scenario: Account groups survive removal of proxy pools
- **WHEN** one account belongs to multiple groups
- **THEN** every group retains its independent membership, key policies and limits
- **AND** shared membership does not create additional upstream quota

#### Scenario: Purchased credits and separately metered quotas survive restart
- **WHEN** usage metadata is persisted or imported and the Go server restarts
- **THEN** purchased-credit status, monthly windows and additional quota windows retain their observed values and timestamps in routing and the dashboard
- **AND** usable purchased credits remain distinct from calculated subscription credits and permit admission after included primary, weekly or monthly windows are exhausted
- **AND** a model's canonical additional quota uses its own fresh windows and registry restrictions for new admissions, without falling back to standard windows when required evidence is absent
- **AND** the established-owner continuation exception remains unchanged
- **AND** absent additional-quota metadata preserves prior samples, an explicit empty list clears them, and older samples cannot replace newer values

### Requirement: Dashboard account import accepts a file or pasted auth JSON

The account import dialog SHALL offer file selection and manual JSON paste as explicit alternatives, with file selection as the default. Both modes SHALL use the same authenticated account-import endpoint and server credential validation. Pasted content SHALL be sent as one UTF-8 file part named `auth_json` with filename `auth.json` and media type `application/json`. Empty input and files exceeding 1 MiB SHALL be rejected before dispatch. Import controls SHALL prevent another submission while busy. Failed imports SHALL keep the draft available for correction and display safe error feedback without an unhandled rejection. Changing modes, closing the dialog or completing import SHALL clear the corresponding temporary credential draft; reopening SHALL start empty. The UI MUST NOT read clipboard contents automatically or persist/log the credential draft outside the import flow.

#### Scenario: Import a local auth.json file
- **WHEN** an operator selects a file and submits it
- **THEN** the selected file reaches the existing import operation unchanged
- **AND** success clears the draft and closes the dialog

#### Scenario: Paste auth.json from the clipboard
- **WHEN** an operator chooses the paste mode and pastes a nonempty document
- **THEN** explicit submission sends that exact document through the same file-import contract
- **AND** normal keyboard/context-menu paste works without Clipboard API permission

#### Scenario: Invalid or oversized input
- **WHEN** the input is empty, exceeds the file limit or is rejected by the existing import operation
- **THEN** no account is reported as imported and the dialog remains available with appropriate safe feedback
- **AND** validation messages do not echo credential contents

#### Scenario: Close or change the import method
- **WHEN** the operator changes the mode or closes and reopens the import dialog
- **THEN** the previous mode's credential draft is discarded and cannot be accidentally submitted

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

### Requirement: Administrators can reset a group's key accounting periods

An authenticated administrator SHALL be able to reset group usage through `POST /api/account-groups/{id}/reset-usage` and its trailing-slash equivalent. The reset SHALL atomically clear current usage and restart every active limit window for all current non-deleted, non-internal keys in that group, including disabled keys. It MUST NOT alter historical requests, group membership, limit configuration, provider quotas or purchased credits. Existing reservations SHALL remain settleable against their original window without charging a newly reset window. Repeated resets within one clock tick MUST NOT reuse a prior reservation window identifier. An existing empty group SHALL succeed without side effects; an unknown group SHALL return not found. The administrator UI SHALL require confirmation, prevent duplicate submission while pending, show retryable failures and refresh displayed group/key usage after success. Key-report authentication MUST NOT authorize this mutation.

#### Scenario: Reset a group while prior requests remain unsettled
- **WHEN** the administrator confirms Reset period for a group with active and disabled member keys
- **THEN** all those keys' active limit counters are reset together and new reset boundaries are shown
- **AND** later settlement of a pre-reset request preserves its historical usage without charging the new windows
- **AND** other groups, deleted/internal keys and upstream balances remain unchanged

### Requirement: Group membership and model-source assignment are independent scopes

For a grouped API key, group membership SHALL restrict ChatGPT accounts while external accounts SHALL follow the key's model-source assignment scope. All model sources SHALL permit otherwise eligible sources independently of ChatGPT group membership; an explicit closed source scope MUST NOT grant access. The same scope rules SHALL apply to catalog visibility, initial selection, established-owner authorization and usage reservation. An empty ChatGPT group MUST NOT grant access to other ChatGPT accounts and MUST NOT suppress a separately permitted external source. Existing model restrictions, credentials, account status, egress restrictions, provider ownership and independent per-key limits SHALL remain enforced.

#### Scenario: Grouped key uses an external model source
- **WHEN** a key belongs to a ChatGPT-account group and permits an enabled external source and its model
- **THEN** the source model is available in the server catalog and its request may use that source without adding it to the ChatGPT group
- **AND** unrelated ChatGPT accounts and explicitly denied sources remain unavailable

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

### Requirement: Empty group membership is visibly distinct from unrestricted accounts

The group creation and edit account picker SHALL describe an empty membership as no accounts selected, including the menu action that clears the selected accounts. Clearing membership SHALL continue to submit an empty account ID array, not expand it to every account. Direct API-key assignment and all-account automation pickers SHALL retain their existing unrestricted-selection label and behavior.

#### Scenario: Create or clear an empty group
- **WHEN** the administrator opens a new group or clears its selected accounts
- **THEN** the trigger and checked empty-selection menu item describe no accounts selected
- **AND** submission preserves the empty membership and the backend's closed-scope behavior

#### Scenario: An ungrouped key uses every permitted account
- **WHEN** the direct API-key account selector is empty
- **THEN** it continues to display All accounts with its existing assignment semantics

### Requirement: Keep domain rules independent of transport and storage

Business rules SHALL be independent of HTTP, SQLite and provider SDK implementations. Application scenarios SHALL own their required ports; the composition root SHALL wire concrete adapters. The implementation MUST NOT introduce a generic framework or duplicate provider behavior merely to satisfy layer naming. Request-owned resources and background jobs SHALL have explicit bounded lifetimes.

#### Scenario: Exercise routing without real upstream services
- **WHEN** tests supply deterministic clocks, repository state and upstream adapters
- **THEN** ownership, quota eligibility and failover rules can be checked without provider credentials or production requests

#### Scenario: First-run dashboard distinguishes local and remote setup
- **WHEN** the dashboard loads before a password is configured
- **THEN** no request is classified as local: its session response requires a bootstrap token for every standard-mode request and names whether the token comes from the environment or the generated file in the data directory
- **AND** the dashboard remains gated until setup completes, and no socket peer, `Host` value or forwarded header bypasses bootstrap verification

### Requirement: Drain strategies preserve their intended ordering

New-account selection SHALL apply `fill_first` by descending primary usage, descending secondary usage and stable account ID. `sequential_drain` SHALL prefer lower configured long-window capacity rather than account ID or remaining usage. `reset_drain` SHALL prefer the earliest future secondary-reset day, fall back to primary when needed, and prefer more remaining primary then long-window quota within that day before comparing exact reset time. Drain ties SHALL remain stable instead of following last-selected recency. Explicit manual burn/preserve priority, account/key/group scope, model eligibility and established-owner rules MUST remain enforced. Generic earlier-reset preference MUST NOT replace the explicit drain or round-robin ordering.

#### Scenario: Account ID does not determine sequential drain
- **GIVEN** a higher-capacity account sorts before a lower-capacity account by ID
- **WHEN** both are eligible at the same manual routing priority
- **THEN** sequential drain selects the lower-capacity account even when its remaining quota is larger

#### Scenario: A reset-day tie prefers usable quota
- **GIVEN** two eligible accounts reset within the same day bucket
- **WHEN** reset-drain selection compares them
- **THEN** the account with more remaining primary quota is preferred before exact reset time
- **AND** an expired secondary reset falls back to a future primary reset, while unknown resets sort last

### Requirement: Relative availability tuning remains operator-controlled

The existing settings API SHALL persist and import relative-availability power and top-K. Power MUST be finite and positive; top-K MUST be an integer from 1 through 20. Missing values on a new or older Go installation SHALL use the legacy defaults of 2 and 5. New Responses and compact selections SHALL apply the saved values without restart and without changing established-owner, scope or quota gates.

#### Scenario: Operator tunes relative availability
- **WHEN** the administrator saves non-default power and top-K with the current settings version
- **THEN** reads and restart preserve those values and subsequent selections use them
- **AND** invalid bounds or a stale settings version fail without changing the stored configuration

### Requirement: OAuth help retains its runtime address contract

The selected Windows OAuth help SHALL retain authenticated `GET /api/settings/runtime/connect-address` and its trailing-slash equivalent, returning `connectAddress`. The trimmed installation override `CODEX_LB_CONNECT_ADDRESS` SHALL take precedence over the request host. Without an override, the helper SHALL prefer a non-loopback IPv4 request host or a bounded IPv4 DNS lookup, retain an unresolved hostname, and show `<codex-lb-ip-or-dns>` for empty or loopback hosts. Returned values MUST NOT introduce command syntax into the displayed instruction; invalid overrides SHALL fail configuration validation without echoing their contents. Forwarded host headers MUST NOT override the chosen address. This informational helper MUST NOT widen the OAuth callback listener or initiate provider work.

#### Scenario: The callback address differs from the dashboard address
- **WHEN** an authenticated administrator opens OAuth help with a valid installation override
- **THEN** the existing UI SHALL receive that override without DNS lookup or a server-side network configuration change
- **AND** an unauthenticated caller SHALL be rejected before address resolution

### Requirement: Provider plan changes preserve account identity and fresh usage

Provider-reported plan/workspace/seat metadata SHALL update atomically with its accepted quota snapshot without overwriting operator status or credentials. An observation for older credentials, identity or fetch order MUST NOT overwrite a newer state. Workspace-less paid-to-free changes SHALL require two agreeing free observations; the first observation MUST NOT publish unconfirmed quota. Explicit reauthentication SHALL reset pending confirmation without making routine token rotation a reset. Legacy pending confirmation counts need not be imported and SHALL start unconfirmed after cutover.

#### Scenario: A paid subscription expires without explicit workspace evidence
- **WHEN** two current usage fetches for the same unchanged paid account report `free`
- **THEN** the second accepted observation commits the free plan and matching quota atomically
- **AND** a stale fetch or an observation captured before credential/identity replacement cannot overwrite that state

#### Scenario: The provider identifies the existing workspace explicitly
- **WHEN** current usage reports a valid plan with the same explicit workspace
- **THEN** its plan may update immediately without the workspace-less confirmation delay
- **AND** a different workspace is rejected rather than changing account ownership

### Requirement: Account deletion revokes access without forgiving financial obligations

The administrator DELETE endpoint SHALL validate and honor `delete_history` with a default of false, retain the first deletion choice for that account incarnation, immediately hide the account and erase its credentials and access memberships. Removing the final scoped member SHALL NOT enable unrestricted routing or scheduling. Cleanup SHALL be bounded and restartable through existing maintenance. Until cleanup completes, reimport of that deleted identity SHALL return a conflict.

With history retained, global/key request statistics SHALL survive with deleted attribution and without appearing as a reimported account's history. With history removed, attributable raw/folded usage and error archives SHALL be removed and report totals adjusted without double-subtracting imported rollups or inventing attribution for undimensioned residuals. Neither mode SHALL refund current key-limit consumption or discard outstanding financial and anti-replay receipts.

Reimport SHALL create a fresh monotonically identified incarnation without restoring prior memberships, security/warmup grants or operational owners, and SHALL preserve mandatory egress denial. Late credential, quota, ownership and diagnostic writes SHALL remain bound to their captured incarnation. Old reservations SHALL still settle exactly once against their original limit items; the original deletion policy SHALL control their report side effects. Deletion SHALL NOT release unknown billing, replay issued claims or remove REQUIRED-capability lineage.

#### Scenario: A deleted account is imported while an old request is unresolved

- **WHEN** cleanup has finished, the same account is reimported and an old unresolved reservation is later reconciled
- **THEN** its original key budget is settled once, its deleted-incarnation reporting policy is respected and the fresh account receives no old owners, usage or credentials

#### Scenario: The last assigned account is removed while keeping request history

- **WHEN** the administrator deletes the only account in a group, explicit key scope or scoped automation with `delete_history=false`
- **THEN** those scopes remain closed, global/key reports keep deleted-attributed history, and removing history does not refund or reset any key limit

### Requirement: Client-plane reasoning is aliased only on the upstream wire

The runtime SHALL send the client-plane `ultra` effort as `max` on subscription and external Responses HTTP/WebSocket, compact, translated Chat and native Chat requests. Key policy SHALL remain applied before wire aliasing, and existing effective-reasoning report fields and persisted configuration MUST NOT be rewritten to the wire alias. Source capability checks SHALL compare wire-equivalent efforts without bypassing reasoning support or accepting unrelated unsupported efforts. Other reasoning fields and unrelated JSON MUST be preserved.

#### Scenario: A key enforces ultra on a supported source
- **WHEN** policy selects effective `ultra` and the source advertises `max` or `ultra`
- **THEN** the provider receives `max` without mutating the key policy, request-log effective effort or caller's original body
- **AND** a source advertising only `high` rejects the request before dispatch

#### Scenario: A private compact or WebSocket request uses ultra
- **WHEN** a subscription compact, automation compact ping or Responses WebSocket sends `ultra`
- **THEN** the outgoing effort is `max` while summary, Lite context and other provider fields remain unchanged

#### Scenario: Native reasoning aliases cannot bypass a key rule
- **WHEN** native Chat supplies an explicit effort through a recognized alias rather than `reasoning_effort`
- **THEN** the same key allowlist or forced-effort rule applies before source selection, reservation and wire aliasing
- **AND** enforcement updates each supplied effort field without dropping unrelated native fields

#### Scenario: Minimal effort is adapted only after selecting subscription transport
- **WHEN** an effective `minimal` request is sent to the private subscription backend through Responses HTTP/WebSocket, compact or warmup
- **THEN** only its outgoing effort SHALL use the first supported non-minimal effort in subscription catalog order, or `low` when no usable catalog effort is available
- **AND** key enforcement and the client-plane report SHALL remain unchanged, while a source declaring `minimal` SHALL continue receiving `minimal` even if its model slug shadows a subscription model

### Requirement: HTTP transport policy preserves client fallback and explicit intent

Downstream HTTP/SSE SHALL honor explicit upstream HTTP/WebSocket selection before its per-key or global HTTP policy. Auto/default SHALL use subscription catalog/native transport evidence, retain HTTP for oversized/image requests and native Codex HTTP fallback, and apply `smart`, `always_http`, `pinned` and `always_websocket` only to compatible HTTP ingress. Per-key policy SHALL override the global policy without overriding a higher-precedence explicit transport or weakening ownership/capability scope. Downstream subscription WebSocket SHALL retain its dedicated native WS path; external sources SHALL retain their supported protocol and transport without inheriting subscription catalog preferences.

#### Scenario: A sticky native Codex client falls back to HTTP
- **WHEN** a native Codex client sends HTTP with continuation headers and auto/default upstream transport
- **THEN** the request SHALL remain on HTTP without losing its proven account owner
- **AND** sticky metadata or an HTTP policy SHALL NOT promote it back to WebSocket

#### Scenario: Auto WebSocket handshake cannot accept a create frame
- **WHEN** subscription auto WS receives a qualified426 or positive403 edge-challenge rejection before sending response.create
- **THEN** one HTTP attempt MAY run on the same account and reservation with HTTP-compatible Lite normalization
- **AND** generic errors, quota/auth refusals, uncertain/charged usage, explicit WS and REQUIRED capability SHALL NOT authorize that fallback

#### Scenario: Reused subscription sockets retain request-scoped Codex metadata

- **WHEN** successive requests use different nonblank `x-codex-turn-metadata`, `x-openai-subagent`, `x-codex-parent-thread-id` or `x-codex-window-id` headers
- **THEN** subscription HTTP SHALL forward only those allowlisted compatibility headers and subscription WebSocket SHALL project them into each request's `client_metadata`, even when the socket is reused
- **AND** body values SHALL take precedence, header lookup SHALL be case-insensitive, and external sources SHALL NOT receive this subscription-only projection

### Requirement: HTTP-only model sources trigger client transport fallback before dispatch

For every Responses WebSocket route and its trailing-slash equivalent, a request assigned to an external account with HTTP upstream transport SHALL return a WebSocket error with status 503 and code `model_source_requires_http_transport` before reserving key budget, saving request affinity or dispatching upstream. This SHALL include empty `generate:false` prewarm requests and incremental continuations. The fallback MUST NOT bypass current key, group, model, account or hard-owner authorization. The same authorized model SHALL remain available through HTTP Responses with complete client context. Subscription WebSocket and explicitly configured external upstream WebSocket SHALL retain their native transport behavior.

#### Scenario: Codex starts an external-source WebSocket session
- **WHEN** an authenticated Codex client sends a prewarm or generation frame for an external source using HTTP upstream transport
- **THEN** the proxy returns `503 model_source_requires_http_transport` without provider work or a usage reservation
- **AND** a subsequent authorized HTTP request can complete through that source

#### Scenario: Continue a native WebSocket request
- **WHEN** the selected account is a subscription or an external source explicitly using upstream WebSocket
- **THEN** the new fallback rule does not reject or silently convert that request

#### Scenario: Reject an unauthorized request
- **WHEN** the key cannot use the requested model or account
- **THEN** existing authorization and hard-owner errors remain enforced without dispatch or reservations

### Requirement: Shared prompt-cache hints do not pin independent logical threads together
New unowned requests with distinct logical Thread-Id identities SHALL NOT inherit each other's account preference solely from a shared explicit prompt-cache key. Soft cache affinity SHALL remain API-key and logical-session/thread scoped when Thread-Id is present. The configured selector SHALL choose among eligible accounts without rewriting the provider-facing cache key or weakening hard ownership.

#### Scenario: New sibling threads share a process and cache key
- **WHEN** independent unowned threads share Session_id and an explicit prompt-cache key but provide distinct Thread-Id values
- **THEN** each thread is eligible for its own account selection under the configured strategy
- **AND** neither a sibling's cache pin nor a pre-upgrade key-wide cache pin forces the new thread onto that account
- **AND** HTTP, WebSocket and compact account selection use the same scope rule

#### Scenario: A repeated thread retains its scope
- **WHEN** requests repeat the same API key, logical session/thread and cache hint
- **THEN** their valid soft binding and TTL remain reusable
- **AND** a different API key, session or thread cannot inherit that binding

#### Scenario: Existing continuations keep their owner
- **WHEN** a request has confirmed previous-response, session, turn-state or file ownership
- **THEN** changing cache hints or admitting a sibling thread does not move that owner
- **AND** existing zero-quota continuation, key/group/model authorization and quota-only failover rules remain enforced

#### Scenario: Clients without a logical Thread-Id retain compatibility
- **WHEN** a client supplies an explicit cache key without Thread-Id
- **THEN** its existing API-key-scoped cache affinity remains available independently of automatic Sticky threads
- **AND** requests without an explicit cache key keep existing automatic affinity behavior

#### Scenario: Invalid scoped hints are rejected
- **WHEN** an identity value used to scope an explicit cache hint exceeds the existing byte bound or the cache key is malformed
- **THEN** the request fails before an affinity lookup, account reservation or provider dispatch

### Requirement: Soft affinity is optional, scoped and operator-manageable

New unowned requests SHALL apply key-scoped typed locality hints without weakening account/model/group/security scope, atomic capacity selection or hard ownership. The existing dashboard settings SHALL control automatic sticky locality, prompt-cache TTL and split primary/secondary pressure thresholds, survive restart/import and affect selection. The legacy single threshold SHALL remain a primary-threshold alias. A healthy eligible soft preference MAY move for pressure or local capacity; this MUST NOT authorize replay or transfer of an already-owned thread. The soft mapping SHALL be compared and set in the same durable transaction as the attempt's reservation, only after the reservation's key and account checks succeed. Dashboard affinity list, filtering, paging, sorting, single/batch/filtered deletion and stale purge SHALL use the existing admin authentication/CSRF and UI contracts. Deleting cache/locality mappings MUST NOT delete response, file, voice or capability-lineage ownership. Unscoped legacy hints MUST NOT be promoted to authenticated key-scoped ownership; the original private snapshot SHALL retain them.

#### Scenario: Soft preference yields to available capacity without changing a hard owner
- **WHEN** a new request's permitted soft-preferred account is locally full and a permitted alternate has room
- **THEN** account selection and lease acquisition SHALL atomically select the alternate
- **AND** a request with a confirmed previous/session/turn/file owner SHALL remain constrained to that owner instead

#### Scenario: Operator clears stale cache hints
- **WHEN** the administrator purges stale prompt-cache affinities
- **THEN** only expired prompt-cache mappings SHALL be deleted
- **AND** durable locality kinds and active hard-owner aliases SHALL remain unchanged

#### Scenario: Locality persistence fails after reservation
- **WHEN** saving a soft mapping fails inside the reservation transaction before provider dispatch
- **THEN** no reservation SHALL remain, the provider SHALL NOT be called and the client SHALL receive HTTP 503 `affinity_persistence_failed`
- **AND** a lost compare-and-set race SHALL NOT fail the reservation, retry the provider request or overwrite the winning mapping

### Requirement: Preserve active owner continuity and distinguish quota errors

A permitted established Codex continuation SHALL remain on its owner when quota telemetry reports zero remaining but upstream has not rejected it for quota. This exception MUST NOT make new sessions eligible or bypass authorization, revocation or account policy. Active cross-account continuation SHALL occur only after a classified upstream quota failure and only with safe reconstructible context inside the allowed key/group/model scope. Local capacity errors, transport timeouts and arbitrary HTTP 429 responses MUST NOT be treated as quota proof. Account-scoped files, turn state and previous-response IDs MUST NOT be reused under a different owner.

#### Scenario: Shared process identity is not shared logical-thread ownership
- **WHEN** separate logical Thread-Id values share a process Session_id
- **THEN** new work SHALL NOT inherit a sibling thread's hard owner solely from that process identity
- **AND** each confirmed logical thread SHALL retain its independent owner, while bare-session clients keep their compatible owner contract

#### Scenario: Explicit and synthesized turn states remain distinguishable
- **WHEN** a client presents an unknown explicit turn state without another proven hard owner
- **THEN** the request SHALL fail before provider dispatch
- **AND** known conflicting hard aliases SHALL fail closed, while a proxy-generated downstream token SHALL never be forwarded as an upstream turn-state token
- **AND** a new hard alias SHALL be established only after a successful accounted response

#### Scenario: Client turn alias follows a quota-authorized replay
- **WHEN** a successful quota-authorized replay changes the conversation owner
- **THEN** its retained client turn-state alias SHALL resolve to the replacement only as a proxy-side anchor
- **AND** the former account's opaque upstream token SHALL NOT be forwarded to the replacement

#### Scenario: Zero remaining is not an upstream refusal
- **GIVEN** a permitted continuation has a known owner with zero reported remaining quota
- **WHEN** upstream still accepts that continuation
- **THEN** the response proceeds on the same owner without a local quota denial

#### Scenario: Quota failover is possible
- **GIVEN** upstream rejects the active owner for quota before incompatible downstream output and a safe eligible replacement exists
- **WHEN** the proxy retries the reconstructible request
- **THEN** it clears owner-specific anchors, preserves complete valid context and uses the replacement without leaking another key's state

#### Scenario: Safe replay is impossible
- **WHEN** the request has an unresolved tool delta, owner-bound file or uncertain already-executed operation
- **THEN** the proxy reports a truthful continuation failure instead of fabricating context or blindly repeating tool actions

#### Scenario: A quota-blocked account reaches a confirmed new window
- **GIVEN** an account is blocked only by a prior quota/rate-limit outcome
- **WHEN** a fresh usage fetch confirms that an elapsed window has advanced by at least one minute to a future reset and all retained governing windows are available
- **THEN** the account may return to active without sending a paid probe
- **AND** this recovery cannot overwrite a newer provider outcome, operator pause, deactivation, reauthentication requirement or unresolved required-egress decision
- **AND** telemetry exhaustion alone still cannot disable an established owner

#### Scenario: An older quota refusal arrives after a newer same-account turn
- **WHEN** a failed reservation attempts to mark a logical session already saved by a newer reservation on the same account
- **THEN** the older refusal SHALL NOT change that session's quota flag
- **AND** the current generation's failed reservation can still mark quota refusal after settlement, but another key/account or an unsettled reservation cannot do so

### Requirement: Recover quota-blocked accounts after provider-side resets
Fresh usage refresh SHALL recover quota-blocked ChatGPT accounts when the backend explicitly reports `rate_limit.allowed=true` and `limit_reached=false` and all governing quota windows are complete and available. Recovery SHALL NOT require a changed reset deadline, a prior exhausted sample or a legacy block timestamp. It SHALL preserve account incarnation, newer provider outcomes and administrator policy.

#### Scenario: A provider reset keeps the same deadline
- **WHEN** a `rate_limited` or `quota_exceeded` account receives a fresh complete available snapshot with explicit backend permission
- **AND** reset deadlines remain unchanged or absent
- **THEN** the account becomes active after that snapshot is accepted
- **AND** no paid probe, reset redemption or accounting reset is performed

#### Scenario: Polls already saved the restored allowance
- **WHEN** quota rows already show zero used while the account remains blocked
- **AND** the next accepted snapshot explicitly permits usage and confirms complete available governing windows
- **THEN** that poll restores the account even without a stored refusal timestamp

#### Scenario: Percentages alone are insufficient
- **WHEN** no natural-reset proof exists and explicit permission is missing or a known governing window is missing, incomplete or exhausted
- **THEN** the account stays blocked even if another window shows 100% remaining

#### Scenario: Explicit denial overrides reset evidence
- **WHEN** a fresh snapshot explicitly reports `allowed=false` or `limit_reached=true`
- **THEN** ordinary usage refresh does not recover that account even if a reset deadline advanced

#### Scenario: Concurrent policy and newer provider outcomes win
- **WHEN** a newer provider outcome, incarnation change, pause, deactivation, reauthentication requirement or egress restriction appears during refresh
- **THEN** the stale recovery attempt cannot overwrite that state
- **AND** an active account is never made quota-blocked merely by polling

### Requirement: Independent Responses sharing a session can run concurrently
The runtime SHALL allow independent Responses requests sharing a logical session to execute concurrently within its bounded capacity. Each active upstream WebSocket SHALL carry at most one response.create at a time. Session sharing alone MUST NOT cause a per-conversation capacity denial while an independent connection can be admitted.

#### Scenario: Parallel full-context subagents
- **WHEN** independent requests without previous_response_id share the same authenticated logical session
- **AND** stream, account and connection admission permit them
- **THEN** each request can reach upstream without waiting for the sibling's terminal event
- **AND** each client receives only its own events and final accounting

#### Scenario: A branch continues on its exact connection
- **WHEN** a request continues a retained response while multiple sockets exist for the logical session
- **THEN** it uses the response's exact authenticated connection scope
- **AND** an arbitrary sibling socket cannot substitute its context
- **AND** sequential continuation retains connection reuse

#### Scenario: An exact anchor disappears while waiting
- **WHEN** a pending continuation's exact connection closes or advances beyond its retained response before that continuation is dispatched
- **THEN** the runtime detects the lost anchor before response.create and releases the unused reservation
- **AND** only an existing safe same-owner reconstruction may recover it, without replaying a possibly executed request

#### Scenario: A sibling is cancelled or its connection is retired
- **WHEN** one parallel attempt is cancelled or loses its connection
- **THEN** the other active connections and their response ownership remain usable
- **AND** cleanup releases only the affected attempt's resources
- **AND** no possibly executed attempt is automatically repeated

#### Scenario: Actual upstream connection capacity is exhausted
- **WHEN** every bounded upstream connection is active and no idle connection can be reused or evicted
- **THEN** excess work is rejected before provider execution without evicting an active peer
- **AND** this local rejection neither proves provider quota exhaustion nor authorizes account migration

### Requirement: Idle client WebSockets do not consume request-body capacity
The runtime SHALL admit up to the host-sized client WebSocket budget (4096 on a 1 GB-class host) without adding a lower per-key socket ceiling, independently of the request-body budget, active-generation and upstream-socket admission. Empty client sockets MUST NOT dispatch upstream, reserve generation/key usage or charge the request-body budget. The global connection slot MUST be acquired after authentication and released on every failed upgrade, disconnect and drain. Actual socket exhaustion SHALL return the existing 503/local_capacity_exceeded envelope and Retry-After. The existing 120-second idle timeout and per-request authorization SHALL remain in force.

#### Scenario: HTTP remains usable beside idle subagent connections
- **WHEN** more than 256 authenticated client WebSockets are idle within the global budget
- **THEN** an eligible HTTP request and a response.create on an admitted WebSocket SHALL still execute under independent generation admission
- **AND** opening those idle sockets SHALL NOT create upstream work, usage reservations or request-body charges

#### Scenario: One key uses the available global pool
- **WHEN** a valid key opens connections while the global socket budget has room
- **THEN** its connections SHALL NOT be refused by a lower per-key socket ceiling

#### Scenario: Socket admission is released
- **WHEN** an upgrade fails, a client disconnects or the runtime drains
- **THEN** its global connection slot SHALL be released without changing active generation or continuation ownership

### Requirement: Standby socket capacity does not multiply retained large input bodies
Large incoming WebSocket messages across reading, queued and executing responses SHALL be bounded by the shared request-body budget in bytes, allowing a current and one staged next message per socket. A message larger than 4096 bytes MUST be charged to that budget, under the socket's key, before retaining data beyond its small prefix and keep the charge until handled or discarded; data read beyond the prefix is charged as it arrives. Waiting on an empty socket and receiving a short control frame (cancel, interrupt) MUST NOT be charged. The existing 32 MiB message limit SHALL remain enforced. Rejection, validation failure, completion, cancellation, disconnect and queue cleanup MUST release owned charges exactly once.

When the budget cannot hold a message, the runtime SHALL discard only the excess message with bounded scratch space under the existing message-size validation, return local capacity for that message, and keep the connection usable. It MUST NOT cancel the connection's active response, replay upstream work, or reclassify pressure as provider quota. The byte budget MUST NOT be presented as a hard process-memory limit.

#### Scenario: Short cancellation remains available at the large-body ceiling
- **WHEN** the request-body budget is full and an active client sends a short response.cancel
- **THEN** cancellation SHALL still be processed and release the active request's charge

#### Scenario: Excess input does not abort admitted work
- **WHEN** an active client sends another large message while the budget cannot hold it
- **THEN** only that excess message SHALL receive local capacity without being retained or dispatched
- **AND** the same connection's active request and subsequent short cancellation SHALL remain usable

#### Scenario: A queued frame outlives the client
- **WHEN** a client disconnects with a queued or partially read large request or an upgrade fails
- **THEN** cleanup SHALL leave no leaked connection slots or body charges

### Requirement: Codex interruption preserves the active response and its accounting

Authenticated Responses WebSockets, including canonical and trailing-slash Codex and v1 routes, SHALL accept `response.interrupt` with a nonempty bounded `response_id` and `mode: discard_partial_items`. A valid command for the connection's current ChatGPT response SHALL be relayed to that response's exact leased upstream WebSocket. The runtime MUST NOT substitute context cancellation, fabricate acceptance or move the request to another account. Actual interruption acknowledgements, output-item interruption events and the terminal response SHALL reach the client, and reported terminal usage SHALL settle once through the existing ledger.

#### Scenario: User input interrupts an active response
- **WHEN** Codex sends a valid interruption after response creation
- **THEN** upstream receives the interruption on the socket executing that response
- **AND** the client receives the actual acknowledgement and terminal with actual usage
- **AND** a subsequent explicit response.create can continue the interrupted response on its retained owner

#### Scenario: Stale or foreign interruption target
- **WHEN** the response ID is not the connection's current response, the response has ended, or interruption is unsupported by the selected provider transport
- **THEN** the client receives response.interrupt.failed identifying that target
- **AND** the runtime does not cancel another response, dispatch a new request or change an account binding

#### Scenario: Malformed control message or revoked key
- **WHEN** an interruption has invalid fields or the proxy key is no longer authorized
- **THEN** the existing validation and authorization error semantics apply without forwarding the control
- **AND** short interruption messages do not acquire a large-body permit

### Requirement: Interruption acknowledgement races cannot contaminate reused sockets

The runtime SHALL bound pending interruption controls to the active response. When a terminal arrives after an interruption was sent but before its acknowledgement, the runtime SHALL retain terminal usage and wait at most five seconds for the matching acknowledgement before retiring that upstream socket. A queued control not yet sent when the response terminates SHALL receive a local response.interrupt.failed, not be sent on a released socket. Missing or invalid acknowledgement MUST NOT discard known terminal usage, leak an acknowledgement to the next response, automatically replay a request or count as quota exhaustion. Disconnect and existing response.cancel SHALL retain bounded cleanup.

#### Scenario: Terminal precedes acknowledgement
- **WHEN** upstream sends terminal usage before acknowledging a sent interruption
- **THEN** the matching acknowledgement and terminal are delivered without releasing the socket early
- **AND** the next response receives none of the previous response's control events

#### Scenario: Acknowledgement never arrives
- **WHEN** terminal usage is known but the acknowledgement deadline expires
- **THEN** the runtime reports interruption failure, preserves terminal accounting and retires only that upstream connection

#### Scenario: Upstream rejects interruption
- **WHEN** upstream emits response.interrupt.failed
- **THEN** it is forwarded as a control acknowledgement rather than a fatal generic response error
- **AND** the current response can still reach its terminal event

### Requirement: Native WebSocket waits preserve client liveness without committing output
The runtime SHALL send application-text heartbeats during a pending native `/backend-api/codex/responses` WebSocket turn and its trailing-slash equivalent after every ten seconds without a real downstream write. It SHALL use `codex.keepalive` when the current attempt has no real response ID and `response.in_progress` with the real ID when known. Heartbeats MUST NOT change visible-output classification, token usage, diagnostics, ownership or quota-retry eligibility. Attempt changes MUST clear the previous response ID. Generic `/v1/responses` routes MUST NOT receive synthetic heartbeats. Completion, cancellation and disconnect MUST stop and join request-owned heartbeat/worker activity with bounded downstream writes.

#### Scenario: Slow native reasoning remains connected
- **WHEN** a native WebSocket upstream is silent before or after response.created
- **THEN** the client SHALL receive application-text heartbeats without fabricated response IDs or usage
- **AND** real response events SHALL retain their order and a terminal event SHALL end heartbeats

#### Scenario: Continuous output needs no heartbeat
- **WHEN** real events are written at least every ten seconds
- **THEN** no heartbeat SHALL be inserted between them

#### Scenario: Quota refusal follows a heartbeat
- **WHEN** the current attempt explicitly refuses quota without output or billable usage after a heartbeat
- **THEN** existing safe failover SHALL remain possible and the previous attempt ID SHALL NOT be reused in subsequent heartbeats

#### Scenario: A generic WebSocket waits
- **WHEN** a public v1 WebSocket request is pending
- **THEN** it SHALL receive only real response events or the existing error envelope, not synthetic native heartbeats

### Requirement: Active upstream WebSocket transport has bounded liveness checks
The runtime SHALL probe an active upstream WebSocket every thirty seconds with at most ten seconds to receive pong, independently of downstream heartbeats. Any frame received from upstream SHALL also count as transport liveness, so a socket that received a frame within the interval SHALL NOT be pinged, and the probe SHALL NOT fail while the socket reader waits for a slow consumer to accept a frame or when a frame arrived after the ping was sent; the downstream write bound limits that wait. Protocol pong MUST NOT count as model progress or extend the overall response deadline. A ping SHALL be bounded only by its own timeout and MUST NOT be cancelled by lease completion or request cancellation; stopping a probe SHALL NOT wait for pong. An unresponsive connection after dispatch MUST be closed without automatic replay, quota refusal or owner migration, and existing unknown-usage reconciliation SHALL apply. Probe tasks MUST stop and be joined on completion and cancellation, and in-flight pings SHALL be joined when the upstream adapter closes.

#### Scenario: Responsive upstream performs long reasoning
- **WHEN** the upstream responds to protocol pings but emits no application events
- **THEN** the request SHALL remain pending within the existing overall response deadline

#### Scenario: Upstream stops answering pings
- **WHEN** an active upstream fails a bounded ping after response.create was sent and sends no frame
- **THEN** the attempt SHALL terminate as a transport failure without another dispatch or fabricated zero usage

#### Scenario: A downstream client is slower than the probe
- **WHEN** a healthy upstream streams faster than the client accepts events, within the downstream write bound
- **THEN** the response SHALL complete with its actual usage

#### Scenario: A response completes while a ping is in flight
- **WHEN** the terminal arrives while a liveness ping is being written or awaiting pong
- **THEN** the owner socket SHALL remain usable for the next continuation

### Requirement: Bound streaming and finalize local accounting once

HTTP/SSE/WebSocket processing SHALL bound queues, waiting, per-request memory and resource lifetimes. A stalled consumer MUST NOT block unrelated streams. Cancellation, disconnect and terminal failures MUST release admission leases and finalize local reservation/accounting ownership once. Local waiting, upstream connection and upstream progress timings MUST remain distinguishable. Keepalive frames MUST NOT mask absent upstream progress or convert truncated/decode-failed responses into successful completion.

The single-instance runtime SHALL enforce persisted/environment-backed subscription per-account create/stream limits, recovery reserve and optional congestion fair-share across Responses, compact and synthetic warmup paths. Overrides SHALL distinguish omitted, null/inherit and explicit zero/disabled. A first-created settings row SHALL pin the process startup values; a pre-capability Go database upgrade SHALL inherit environment values through nullable overrides until an operator changes them. New ownership may select another available permitted account before dispatch; capacity pressure MUST NOT move a confirmed existing owner or mutate quota/health. The recovery reserve SHALL protect confirmed key-scoped conversation/file owners, not client-asserted identity or synthetic warmup. Create capacity SHALL release on the first upstream response event or attempt return, and stream capacity SHALL release on every return/cancel/error path. Bounded local exhaustion SHALL use a distinguishable rate-limit response with HTTP Retry-After; external source concurrency SHALL retain its separate contract.

#### Scenario: A dispatched native request has no trustworthy usage
- **WHEN** a native Chat Completions or embeddings attempt may have reached its provider but reports no trustworthy usage
- **THEN** its reservation remains durably marked for reconciliation rather than finalized with invented zero usage or stale-released
- **AND** explicit provider allowance for missing usage does not bypass a key's metered limits
- **AND** an error discovered after streamed chunks is returned as a sanitized error frame, not a successful DONE marker

#### Scenario: Ordinary Responses cannot invent free usage after uncertainty
- **WHEN** a possibly dispatched HTTP/SSE/WebSocket Responses or translated Chat attempt lacks valid usage or loses its outcome
- **THEN** the existing reservation SHALL be retained for reconciliation and visible as unknown billing, never finalized or stale-released as zero
- **AND** confirmed zero remains distinguishable from missing usage, and confirmed usage on a failed response or before later delivery failure is settled once
- **AND** a source opt-in for missing usage cannot bypass a key's metered limits or create a fabricated finalized account outcome
- **AND** uncertainty alone does not authorize replay, while classified actual quota refusal preserves safe failover after settlement

#### Scenario: A refusal includes unusable billing counters
- **WHEN** a quota, missing-previous or authentication refusal reports partial or invalid usage
- **THEN** it SHALL retain the reservation as unknown billing without automatic replay or authentication retry, rather than treating the report as absent or zero
- **AND** a separately valid quota error SHALL still establish fenced quota-derived owner/account state after the reservation is retained

#### Scenario: Native or ancillary rejection reports billing
- **WHEN** a native Chat/embeddings rejection or streaming error chunk reports billing
- **THEN** valid counters SHALL settle once and partial/invalid reported counters SHALL retain reconciliation ownership, including on HTTP 4xx
- **AND** ancillary authentication retry MUST NOT repeat a billed attempt or an attempt with unusable reported tokens/duration

#### Scenario: A billed or partially delivered quota refusal is not forgotten
- **WHEN** the provider explicitly refuses quota after reporting charges or producing visible output
- **THEN** this attempt MUST NOT be replayed automatically
- **AND** the confirmed refusal SHALL update quota-derived owner/account state only after durable settlement or retained-reconciliation ownership, preserving account/key/sequence fences
- **AND** retained unknown usage MUST NOT authorize a healthy-success account outcome

#### Scenario: Explicit counters are distinct from incomplete billing
- **WHEN** valid input and output counters are present but their redundant total is omitted
- **THEN** accounting may compute the total without treating actual usage as unknown
- **AND** inconsistent totals or invalid detail counters cannot become trusted billing
- **AND** an auth or missing-previous error carrying nonzero confirmed usage cannot cause an automatic duplicate attempt

#### Scenario: Compact or external transcription has uncertain billing
- **WHEN** a possibly dispatched compact or metered external transcription lacks valid billing usage, is interrupted, or returns invalid/overflowed billing counters
- **THEN** its existing reservation remains durably marked for reconciliation, not zero-settled or stale-released
- **AND** a nominally successful compact without usage returns an error without publishing new ownership or successful synthesized SSE
- **AND** per-minute audio requires a confirmed finite nonnegative duration, while token pricing requires explicit valid token counters; explicit zero remains distinct from absence
- **AND** a valid subscription transcription retains zero-token billing, and known provider rejection or local pre-dispatch failure can release an unused reserve
- **AND** canonical and trailing-slash backend/v1 transcription routes apply identical authentication, multipart parsing and accounting

#### Scenario: Codex requests client-triggered compaction
- **WHEN** an HTTP Codex Responses stream supplies a single final top-level `compaction_trigger` input item
- **THEN** the operation follows compact dispatch with the same owner, key policy and capability restrictions and settles its usage exactly once
- **AND** a successful operation produces the Codex-compatible created, compaction-item added/done and completed stream with encrypted content and actual usage
- **AND** malformed, repeated or non-terminal triggers are rejected before admission, while the generic `/v1/responses` route is not redirected to compact
- **AND** Codex WebSocket validates the same trigger shape but forwards the original operation to a subscription account rather than synthesizing HTTP compact events or routing to an external model source

#### Scenario: A compact request requires account-specific model support
- **WHEN** standalone or trigger compaction selects an account from multiple permitted subscriptions
- **THEN** the same model and service-tier catalog policy as Responses filters the candidates before reservation or dispatch
- **AND** a known conversation/file owner that fails that policy is rejected rather than silently replaced
- **AND** an effective-tier change authorized by catalog policy is reflected in the compact request sent upstream

#### Scenario: Compact cannot bypass effective key policy
- **WHEN** standalone or triggered compact applies model, tier and reasoning settings
- **THEN** the same key-policy preparation as Responses enforces allowed reasoning and the global Fast Mode prohibition even when a tier is forced by the key
- **AND** fields unsupported by compact are removed before that preparation, while scalar text input follows the same retained-history budget as array input
- **AND** policy rejection occurs before reservation or dispatch

#### Scenario: New compaction shares current subscription selection
- **WHEN** a compact request without an established conversation or file owner obtains capacity
- **THEN** the same current routing strategy, pricing availability and purchased/monthly/additional quota rules as subscription Responses select its account
- **AND** external sources are excluded and separately metered models cannot bypass unavailable additional quota by using standard remaining quota
- **AND** selection happens after the capacity wait without double admission or settlement

#### Scenario: Compact recovers from an upstream quota refusal
- **WHEN** a compact attempt receives a classified upstream quota refusal and its reservation/outcome are persisted
- **THEN** a complete account-neutral replay may continue on an untried permitted subscription inside the same admission and overall deadline
- **AND** previous-response, turn-state and account-affinity state are not sent to the replacement
- **AND** each dispatched attempt is settled once and the successful response/session owner is saved for the replacement without exposing the internal quota error
- **AND** unsafe history, owner-bound resources, a local accounting failure or a non-quota upstream error cannot authorize another attempt

#### Scenario: Oversized compaction retains required conversation state
- **WHEN** standalone or trigger compact input exceeds the legacy estimated wire budget
- **THEN** preparation retains mandatory Lite/system/developer/plan/goal state and required terminal context in their original order
- **AND** tool calls and outputs are matched by protocol and occurrence, not only by reused call ID
- **AND** complete historical side-effect pairs have bounded priority over optional ordinary context and omitted ranges receive explicit trim markers
- **AND** selection of unchanged items precedes inline-image elision, which is restricted to already-observed required tool outputs
- **AND** input that still cannot fit is rejected before reservation or dispatch, and omitted file references do not pin the prepared request

#### Scenario: Responses Lite signaling follows the actual request body
- **WHEN** a Responses or compact input contains `additional_tools`
- **THEN** ChatGPT HTTP requests use the canonical Lite header and WebSocket requests use the canonical per-frame metadata marker without the header in the handshake
- **AND** the input prefix and top-level instructions remain intact, while first-party Lite reasoning uses `context: all_turns`
- **AND** inbound header values and reserved metadata variants cannot enable Lite on their own or leak to an external source

#### Scenario: An accepted Lite response authorizes only its matching continuation
- **WHEN** a direct WebSocket follow-up contains the reserved true marker without repeating the Lite prefix
- **THEN** it is trusted only for the same effective model and latest accepted Lite response ID in that connection
- **AND** preparation, failed hidden attempts and non-Lite acceptances cannot overwrite the accepted Lite state
- **AND** a fresh replay without the prefix drops inherited Lite signaling, while a full replay retaining the prefix derives it again

#### Scenario: A key cannot force Lite onto a confirmed non-Lite model
- **WHEN** key model enforcement targets a model explicitly declared `use_responses_lite: false` for a Lite request
- **THEN** Responses and compact reject it before reservation and dispatch with `responses_lite_model_mismatch`
- **AND** absent or unknown capability metadata is not treated as an explicit rejection

#### Scenario: Ready consumer receives a burst
- **WHEN** a healthy upstream emits more than one internal queue's event count in a burst
- **THEN** the ready consumer receives complete ordered output without a spurious overload caused solely by dispatch starvation

#### Scenario: Cancel while waiting
- **WHEN** the client cancels during admission, refresh or streaming
- **THEN** its owned resources are released within the lifecycle bounds
- **AND** no database transaction or socket lease remains indefinitely checked out

### Requirement: Integrate external providers without losing Codex semantics

The server SHALL support configured ChatGPT and external API accounts, including Z.AI and OpenAI-compatible sources using Chat Completions or native Responses. Its integrated adapter SHALL preserve supported tool calls/results, reasoning, streaming, usage, model mappings and continuation state. Unsupported capabilities MUST be rejected explicitly before dispatch rather than dropped or invented. Provider credentials, account scopes and custom pricing MUST remain isolated. Cross-provider switching MUST NOT occur implicitly solely because another provider has available quota. Z.AI source creation and editing SHALL allow either or both supported protocols, require at least one, and retain rejection of audio and embeddings. The configured Responses capability SHALL select native Responses without Chat-only GLM customization; existing Chat-only sources and defaults SHALL remain unchanged. The dashboard MUST NOT replace the operator's endpoint or credentials when selecting a protocol.

#### Scenario: Codex tool round trip through a Chat Completions source
- **WHEN** Codex receives a translated tool call and sends its matching output on the next turn
- **THEN** the adapter reconstructs valid provider history and returns correctly correlated Responses events
- **AND** the exchange does not require an external relay process

#### Scenario: Direct Chat Completions obey declared model capabilities
- **WHEN** a direct source-routed Chat request requires streaming, tools, images or active reasoning that its configured model does not support
- **THEN** the server rejects it with HTTP 400 before upstream dispatch and releases its unused budget without an uncertain usage reservation
- **AND** explicit reasoning effort must be supported by model metadata, while empty tools and disabled controls alone do not require capabilities

#### Scenario: Administrator selects native Responses for Z.AI
- **WHEN** the administrator creates or edits a Z.AI source with Responses enabled and a corresponding base URL
- **THEN** the dashboard and API preserve that protocol selection and the supplied endpoint
- **AND** Responses-only configuration is accepted while a source with no supported protocol or audio/embeddings is rejected

#### Scenario: Native Z.AI responses preserve the provider contract
- **WHEN** a permitted request uses a Z.AI source with Responses enabled
- **THEN** JSON and streaming requests use the native Responses endpoint, even if Chat is also enabled
- **AND** supported tools, reasoning, model mapping and usage remain intact without injecting Chat-only thinking, messages or stream options

#### Scenario: Existing Z.AI Chat source remains compatible
- **WHEN** an existing Z.AI source has Chat enabled and Responses disabled
- **THEN** it continues using Chat Completions with the existing GLM thinking behavior
- **AND** upgrading alone does not modify the source endpoint, credentials or protocol flags

### Requirement: Live external-source edits fence retired routes without losing usage

An external source kind, base URL or Responses/Chat protocol change SHALL atomically retire its previous routing identity, including A-to-B-to-A edits. A request admitted to the retired route MUST NOT be dispatched using the new configuration or silently retried there. A request already sent SHALL retain its original endpoint and financial reservation; known or uncertain usage SHALL settle exactly once under the existing accounting rules without deleting history or resetting key consumption. Late operational ownership, affinity, capability, quota and transport state MUST NOT reactivate the retired route. Metadata, pricing and concurrency/timeout edits SHALL preserve routing identity and SHALL NOT clear an upstream-derived quota block merely because the source remains enabled. Existing Go databases SHALL migrate without losing reservations or active current-route state.

#### Scenario: A provider endpoint changes during an admitted request
- **WHEN** the administrator changes the source endpoint after admission but before dispatch
- **THEN** that request SHALL fail locally without sending its body or credential to the new endpoint
- **AND** changing the endpoint back SHALL NOT make the old request current again

#### Scenario: A response arrives from a retired source route
- **WHEN** an already-dispatched request finishes after the source route changes
- **THEN** its original financial obligation SHALL be retained exactly once
- **AND** its late response/session/quota callbacks SHALL NOT overwrite the current route state

#### Scenario: An administrator edits only the price or display name
- **WHEN** the enabled source route is unchanged
- **THEN** existing conversation ownership and the confirmed quota status SHALL remain unchanged

### Requirement: Separate error archives from usage and continuation storage

All requests SHALL contribute content-free usage, cost, timing and outcome data to reports. Long-term diagnostic content archives SHALL contain only errors and SHALL apply authentication, secret redaction, size bounds and retention. Successful conversation content MUST NOT be archived merely for diagnostics. Necessary operational continuation state MAY be retained independently under bounded lifetime and size policies; eviction MUST lead to an explicit recoverability decision, not silent context loss. Existing historical data MUST NOT be deleted under this prospective archive policy.

#### Scenario: Successful request is accounted but not archived
- **WHEN** a request completes successfully
- **THEN** its usage is visible in reports and settled against the correct key
- **AND** no successful-payload diagnostic archive record is created

#### Scenario: Pending accounting is visible without invented usage
- **WHEN** an authenticated administrator queries request logs or their filter options
- **THEN** reservations marked for reconciliation appear alongside settled events with their known request/account/key/model/time identity and `reconciliation_required` status
- **AND** unknown actual usage, cost, timing and unrecorded request metadata remain null or explicitly unknown rather than inferred from reserved budget
- **AND** filters, stable page ordering, total and hasMore use the combined snapshot; ordinary live reservations are not reported as pending failures
- **AND** atomic settlement or release removes the pending row without double display or artificial report totals
- **AND** the dashboard accepts the runtime's request kinds and distinguishes pending accounting from success or quota rejection

### Requirement: Read-only weekly-credit pace follows persisted settings
Dashboard overview/projections SHALL compute the selected read-only weekly-credit pace from fresh persisted quota observations, including saved UTC working weekdays and smoothing interval. Persisted observations are the newest observation of each five-minute bucket of an account window, with reset or window changes kept as separate observations. Missing samples SHALL remain distinguishable from zero burn, reset/window changes SHALL not be smoothed together, and stale/ineligible observations SHALL not inflate usable capacity. The report SHALL preserve its existing nullable forecast, confidence and reset-event contracts without controlling routing or initiating provider work. The deprecated dashboard overview SHALL keep its key attribution of the last two hours; the projections SHALL return an empty key attribution and SHALL NOT read usage events for it. Settings SHALL survive restart and import and affect the actual report, not merely be accepted by the settings API.

#### Scenario: Saved weekday and smoothing settings reach the report
- **WHEN** the administrator saves working weekdays or a supported smoothing interval and reloads the runtime
- **THEN** overview and projections SHALL apply those saved values to the same persisted observations without fetching provider data

#### Scenario: There is no fresh usable weekly observation
- **WHEN** all candidate observations are stale, invalid or operator-ineligible
- **THEN** weeklyCreditPace SHALL be null rather than showing fictional available credits or zero consumption

#### Scenario: An idle polled account
- **WHEN** an eligible account is polled without consumption for six hours
- **THEN** its history still contains an observation in every five-minute bucket so the pace reports zero burn rather than missing samples

#### Scenario: Projections without key attribution
- **WHEN** API keys made requests in the last two hours and the pool has a weekly credit pace
- **THEN** the overview's pace SHALL list the top keys of those hours, and the projections' pace SHALL carry the same forecast with an empty key attribution

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

### Requirement: Subscription requests omit unsupported output token caps

The ChatGPT subscription wire payload for Responses HTTP/WebSocket and compact SHALL omit `max_output_tokens`, including administrative probes and scheduled warmups. The caller's original request SHALL remain unchanged. External-provider Responses SHALL preserve the field and Chat Completions SHALL retain its existing translation. Removing the field SHALL NOT trigger another generation or change model/account ownership. The subscription backend's output MUST NOT be described as capped to that requested value.

For subscription Responses, the output-token estimate used for per-key reservation SHALL NOT be reduced below the normal uncapped estimate of 2048 tokens by the unsupported field; a larger supplied estimate SHALL remain conservative. The existing reservation clamp to remaining key budget and external-provider estimates SHALL retain their current behavior. Settlement SHALL use actual provider usage once, including usage exceeding the supplied value; absent usage SHALL remain unresolved, not become free.

#### Scenario: Manual probe reaches an upstream that rejects the parameter
- **WHEN** an administrator probes the pinned account using Luna
- **THEN** exactly one upstream request is sent without `max_output_tokens`
- **AND** the actual usage is settled without a model fallback or repeat

#### Scenario: External provider supports an output cap
- **WHEN** a Responses request with `max_output_tokens` targets an external provider
- **THEN** that field remains in its upstream request

#### Scenario: A tiny unsupported cap cannot under-reserve key budget
- **WHEN** a subscription request supplies an output cap of one token and its key has 1024 output tokens available
- **THEN** admission reserves the remaining 1024 tokens rather than reserving only one output token
- **AND** settlement replaces that reservation with actual reported usage

### Requirement: Subscription streaming accepts a missing media type only with valid SSE

A successful ChatGPT subscription HTTP response with no Content-Type SHALL be processed by the same bounded SSE parser and terminal/id/usage validation as an explicit `text/event-stream` response. An explicit incompatible media type SHALL still be rejected. External-provider streaming SHALL retain its existing Content-Type requirement. A missing media type MUST NOT make a non-SSE body, malformed event, missing terminal or unreported usage into a successful or free request.

#### Scenario: Real subscription stream omits Content-Type
- **WHEN** the subscription upstream returns HTTP 200 without Content-Type and valid SSE ending in response.completed with known usage
- **THEN** the probe or normal Responses request completes and settles the reported usage exactly once

#### Scenario: Missing header does not replace protocol validation
- **WHEN** a headerless subscription body is HTML, malformed SSE or lacks a valid terminal usage report
- **THEN** the request fails and unknown usage remains unresolved

#### Scenario: External stream omits Content-Type
- **WHEN** an external model source returns a response without Content-Type
- **THEN** the existing invalid-stream rejection remains in force

### Requirement: Default administrative probes use Luna without model fallback

An administrative probe or warmup without an explicit model SHALL use `gpt-6-luna`. Explicit configured/request models SHALL remain unchanged. Failure MUST NOT silently select another model or repeat the generation. Existing account pinning, administrator authentication and accounting SHALL remain enforced; unknown usage SHALL remain pending rather than be fabricated as zero.

#### Scenario: Probe without a model
- **WHEN** an authenticated administrator probes an eligible account without supplying a model
- **THEN** one request is sent to that account using `gpt-6-luna`
- **AND** a failure does not dispatch a more expensive fallback

### Requirement: A sole weekly quota is not a five-hour quota

A ChatGPT usage response with only a primary window lasting exactly seven days SHALL be represented as a weekly/secondary quota, with no five-hour/primary quota. Existing dual-window, unknown-duration and monthly-only handling SHALL remain compatible. A fresh complete nonempty standard-quota observation SHALL atomically replace older current standard windows; absent, incomplete or stale observations MUST NOT erase previously known windows. Account/credential/generation and pending plan-confirmation fences SHALL still apply. Replacing an incorrectly labeled seven-day primary window SHALL correct that account's corresponding seven-day history labels without changing observation values or financial usage. Weekly-only account views and legends SHALL not describe their quota as five-hour quota. No synthetic zero-percent primary quota SHALL be created.

#### Scenario: Weekly-only observation replaces the old mislabeled window
- **WHEN** a fresh provider observation contains one seven-day window at 41 percent used
- **THEN** accounts and dashboard show 59 percent weekly remaining and no primary quota
- **AND** the old primary row and its incorrect history labels do not continue to appear as five-hour capacity

#### Scenario: A partial or stale observation arrives
- **WHEN** usage windows are absent/incomplete or the observation predates accepted account state
- **THEN** the observation does not erase more recent known windows or bypass account identity fences

#### Scenario: Both standard windows exist
- **WHEN** the provider reports both five-hour and weekly windows
- **THEN** both periods retain their own usage, reset times and current display

### Requirement: Preserve scheduled work and operator-controlled reset credits

Admin and scheduled warmups SHALL use the existing durable reservation/reconciliation mechanism without consuming a user's key limits or becoming a usable internal API credential. Known warmup billing SHALL settle once; unknown billing or unpriceable reported usage SHALL remain pending instead of becoming free. A warmup failure MUST NOT reassign a conversation or authorize an implicit repeat. Cleanup SHALL preserve accounting after cancellation, and accounting failures MUST NOT be hidden by quota classification.

Public `/v1/warmup` body and URL-mode routes SHALL support normal/strict/force with API-key authentication, current key/group/account scope, effective model policy, bounded fanout of at most five and per-account sanitized snake_case results. Normal mode SHALL skip targets without unused 5-hour primary telemetry; strict SHALL reject the entire ineligible set before dispatch; force SHALL bypass only that telemetry condition, never key/owner/operator authorization. Public key-authenticated warmup requests SHALL reserve and settle that key's budget, correcting the legacy limit bypass, without reassigning a conversation or silently retrying an uncertain call.

The dashboard SHALL omit automatic reset-credit redemption controls and payload fields. The selected manual display preferences `showResetCreditBadges` and `showResetCreditExpiryBadge` SHALL default to true, persist across restart and import their legacy values without enabling automatic redemption.

Enabled warmups and scheduled pings SHALL retain targeting, timezones, quota accounting and restart-safe execution. They MUST NOT be replaced by the removed quota phase planner. OAuth refresh and quota polling SHALL remain separate operations. Reset-credit redemption SHALL remain available through authenticated administrator actions and SHALL preserve idempotency; retries MUST NOT accidentally consume another credit. No automatic reset redemption SHALL be inferred from low quota.

#### Scenario: Lost redemption response is retried
- **WHEN** the same reset-redemption identifier is retried
- **THEN** the existing redemption is reconciled rather than spending the next credit

#### Scenario: Existing dashboard reset routes preserve upstream selection contracts
- **WHEN** the administrator starts a new redemption through `rate-limit-reset-credits/consume`
- **THEN** the freshest list must report a positive available count and an available credit before the service pins and sends its identity
- **AND** `usage-reset-credits/consume` leaves selection to upstream and sends only the redemption identifier
- **AND** a retry through either route preserves the original pinned upstream request, including after a server restart

#### Scenario: Manual reset restores an available account before natural expiry
- **WHEN** an authenticated administrator redeems a reset credit and fresh usage confirms all previously governing windows available
- **THEN** an earlier quota-derived account block can be cleared before its original natural reset deadline
- **AND** the outcome checkpoint retained with the original redemption prevents a retry from clearing a newer provider refusal or operator block
- **AND** the dashboard receives before/after quota and account status without an automatic paid warmup

#### Scenario: A stale credit fetch finishes after redemption
- **WHEN** an in-flight credit-list fetch predates a completed consume operation
- **THEN** it cannot restore the invalidated snapshot or make the consumed credit appear available again
- **AND** `nothing_to_reset` and `no_credit` are not recorded as successful redemptions

#### Scenario: A cached reset-credit read targets an unavailable account
- **WHEN** a cached reset-credit dashboard read targets a missing, deleted or ineligible account
- **THEN** it returns null without calling upstream and invalidates any older cached snapshot
- **AND** malformed upstream credit lists are rejected rather than stored as an authoritative empty list

### Requirement: Import and deploy without in-place data loss

The runtime SHALL support a single-process SQLite installation with embedded web assets and external persistent data/key files. Import SHALL read a consistent source copy, verify credentials and policy compatibility, and preserve applicable identities, ledgers and aggregate statistics without mutating the original. A removed required proxy binding MUST NOT silently become direct egress. Readiness MUST remain false when schema/key/import checks fail. Cutover SHALL require explicit authorization after selected features and regression checks pass. Active sockets MUST NOT be described as transparently transferable between processes; post-cutover writes MUST be addressed in rollback planning.

#### Scenario: Import is validated before service replacement
- **WHEN** the administrator supplies a valid source snapshot for dry-run import
- **THEN** the original remains unchanged and the destination can be checked before traffic is moved
- **AND** no service update occurs as a side effect of importing

### Requirement: Edit model prices without deploying a new server

An authenticated administrator SHALL be able to add prices for a new Codex model and override or restore a bundled model price from the dashboard. Custom prices SHALL persist in SQLite and take effect for subsequent request admissions without rebuilding, upgrading or restarting the server. Price validation SHALL reject invalid, negative or out-of-range rates. Standard input, cached-input and output rates, and every rate in an explicitly supplied optional tier, MUST be present; omitted or null fields MUST NOT silently become zero. Explicit zero rates MAY be configured. External model-source prices MUST remain scoped to their source and MUST NOT be replaced by a global Codex override.

Saving a model price SHALL recalculate historical costs for that model from retained token usage and request-tier/context data, including requests previously recorded at zero cost. Reports, account/key aggregates and affected current cost/credit limit consumption SHALL remain consistent without double-counting or changing token counts. Repricing MUST NOT issue provider requests. Missing historical detail MUST be reported explicitly rather than assigned invented costs. An in-flight settlement MUST NOT reintroduce an obsolete price after a completed price update. Historical repricing SHALL run in bounded chunks so reservations and settlements are not blocked for the duration of the whole history; each chunk SHALL update event costs together with their aggregate and current-limit deltas. An unfinished repricing SHALL be recorded durably and resumed after restart, and repeating it SHALL NOT change already repriced costs. Removing an override SHALL restore an existing bundled rate; removing a custom-only model price SHALL leave that model explicitly unpriced rather than free.

#### Scenario: A new model is priced while the server is running
- **WHEN** the administrator saves a valid price for a model absent from the bundled rate card
- **THEN** the next permitted request for that model uses the saved price
- **AND** the price survives a server restart without a binary update

#### Scenario: A tariff is edited during a response
- **GIVEN** a request has already selected its price
- **WHEN** an administrator changes that model's tariff
- **THEN** the active attempt settles consistently with the updated tariff and later admissions use the new tariff
- **AND** retained historical requests of that model are repriced, including prior zero-cost records

#### Scenario: Historical details are no longer retained
- **WHEN** some affected historical aggregates lack sufficient token/tier/context information for exact repricing
- **THEN** the operation reports the portion that cannot be recalculated accurately
- **AND** it does not invent a token distribution or claim that all history was updated

#### Scenario: Price editing remains administrator-only
- **WHEN** an unauthenticated caller or an ordinary proxy API key attempts to modify model prices
- **THEN** the dashboard API rejects the mutation without changing rates

#### Scenario: A historical cost changes after a manual key-limit reset
- **WHEN** a tariff edit reprices requests whose charges were cleared by an explicit key-limit reset
- **THEN** their historical report costs are updated without reinstating cleared current-limit consumption
- **AND** missing current-limit charge provenance found before saving rejects the edit without changing the tariff or any financial value

#### Scenario: The ledger keeps working during a long repricing
- **WHEN** a tariff edit reprices a large history while requests are being reserved and settled
- **THEN** those reservations and settlements SHALL succeed and interleave with repricing chunks
- **AND** a repricing interrupted by a restart SHALL complete after the next start without double-counting

### Requirement: Preserve required-capability conversation isolation

The Go Responses ingress SHALL preserve the required-capability and durable-lineage contracts of the existing `responses-api-compat` and `sticky-session-operations` capabilities. The exact `trusted_cyber` signal SHALL require a real API key and WebSocket transport. Validation MUST inspect raw request/frame data before normalization so duplicate, spoofed or misplaced markers cannot be hidden. Required lineage SHALL remain key-scoped across session, turn, response and supported parent/window aliases and MUST NOT downgrade when an alias is presented over HTTP or storage lookup fails.

Required requests SHALL use only currently permitted, authorized ChatGPT accounts. The marker MUST NOT be forwarded to a provider or retained in diagnostics. Ordinary and required upstream sockets SHALL remain isolated, and response aliases SHALL be durably marked before their IDs are exposed to the client. Loss of capability authorization MUST NOT permit cross-account transfer of an established owner without the separately required upstream quota refusal.

#### Scenario: Required lineage is continued over HTTP
- **WHEN** an HTTP request omits the explicit marker but supplies an alias of a required conversation
- **THEN** the request is rejected before provider dispatch rather than treated as ordinary traffic

#### Scenario: A capability grant is revoked
- **WHEN** an established required conversation loses its owner's capability grant or key scope
- **THEN** it cannot use an ordinary socket or silently move to another account
- **AND** retired upstream resources remain owned until pending work has been finalized

### Requirement: Concurrent policy reads do not queue behind unrelated durable writes

The file-backed Go SQLite runtime SHALL permit bounded concurrent read-only
committed snapshots while retaining a serialized durable writer. Read-check-write
admission, key-limit reservations, settlement, ownership fences and repricing
SHALL retain atomicity and exactly-once local accounting. Every opened connection
SHALL apply its required isolation and integrity settings, including reconnects.
Writer durability MUST remain WAL/FULL; no response or admission may be reported
durably recorded before its required commit. The change MUST NOT introduce stale
authorization caches, unbounded connection growth, hidden write retries or
fire-and-forget financial mutations.

#### Scenario: A writer is busy while another request checks committed policy
- **WHEN** an unrelated writer transaction has not committed
- **THEN** a pure read SHALL complete from a consistent committed snapshot without waiting for the writer connection
- **AND** it SHALL NOT expose uncommitted policy or bypass subsequent transactional reservation checks

#### Scenario: Connections are reopened or startup fails
- **WHEN** the read pool opens a new physical connection or any startup stage fails
- **THEN** each connection SHALL retain its required settings and partial resources SHALL be closed
- **AND** foreign databases and literal-path safety SHALL retain their previous refusal behavior

### Requirement: Grouped short writes retain independent financial outcomes

The runtime SHALL group only already waiting short SQLite mutations into bounded
transactions without delaying an idle write to collect more work. Each job
SHALL retain independent rollback isolation, while successful jobs MUST NOT be
acknowledged before the shared FULL commit succeeds. A caller cancellation MUST
NOT interrupt unrelated jobs or return an indeterminate reservation while its
callback continues mutating state. Failed savepoint control, callback panic or
outer commit failure SHALL fail the affected pending batch without hidden retry
or subsequent accidental autocommit. Closing the store SHALL stop new admissions
and finish accepted jobs before closing the database. Existing financial and
account/key/incarnation/route/outcome-sequence fences MUST remain unchanged.

#### Scenario: A batch contains a failed or cancelled job
- **WHEN** an isolated job fails before release and its savepoint can be rolled back
- **THEN** its mutation SHALL be discarded without undoing successful sibling jobs
- **AND** successful siblings SHALL wait for the shared durable commit before returning

#### Scenario: The batch cannot commit
- **WHEN** savepoint integrity is lost or the outer transaction commit fails
- **THEN** no pending successful job SHALL be acknowledged or retried automatically
- **AND** callers SHALL retain the existing fail-closed accounting/reconciliation behavior

### Requirement: Streaming optimization is verified under concurrent user load

Performance changes SHALL be compared against the preserved pre-change Go binary
on identical offline disk-backed workloads and durability settings, including
8, 32, 128 and 256 concurrent requests and multiple keys. Results SHALL report
first-token and completion p50/p95/p99, errors, resource consumption and
cancellation cleanup; failed requests MUST NOT be counted as fast successes.
Both native streaming Chat and Codex Responses SHALL preserve their current
output, usage, ownership and terminal contracts. Results for a synthetic local
provider MUST NOT be presented as a real-provider or production capacity SLA.
The target deployment is 1vCPU/1GB with a 500–600 MB service memory budget
and predominantly long-lived streams; measured memory use and workload-dependent
limitations SHALL be reported. Verification SHALL include single-Go-processor
runs, peakRSS and larger request context rather than only tiny short requests.
At least512 active streams SHALL be tested without counting queued requests as
active. Additional concurrency MUST NOT weaken accounting/authentication.
Configuration/data files MAY be reorganized only
where measurement supports the change and recovery/data-safety contracts remain.

The global active-stream bound SHALL follow the host capacity plan, supporting
512 concurrent requests with a bounded 128-request queue on a 1 GB-class host,
retain the 15-second queue timeout, and preserve all per-account
recovery/create/stream and configured source caps. Raising the
global bound MUST NOT authorize scope bypass or weaken cancellation cleanup.
The production HTTP transport MUST allow as many concurrent connections to one
upstream host as the active-stream bound, so its HTTP/1.1 connection queue does
not reduce this bound. The upstream WebSocket session store SHALL support as
many independent live sessions as the active-stream bound
without evicting active peers; its bounded-capacity refusal and idle-session
eviction SHALL retain owner, key, credential and capability isolation.

#### Scenario: The candidate is accepted after load testing
- **WHEN** the optimized runtime completes repeated concurrent streaming trials
- **THEN** measured TTFT and tail latency improvements SHALL be stated against the matching durable baseline
- **AND** accounting, cancellation, resource bounds and existing protocol regression tests SHALL remain passing

#### Scenario: At least256 independent subscription streams overlap
- **WHEN** 256 authorized clients have sufficient capacity across 40 eligible subscription accounts and the stub holds terminal responses after the first event
- **THEN** all 256 streams SHALL become active before release without bypassing per-account caps
- **AND** successful completion or cancellation SHALL release their owned resources and preserve accounting

#### Scenario: 512 mixed native generations complete without bypassing policy
- **WHEN** 512 permitted native WebSocket requests have sufficient provider capacity and a declared mixed-input workload is started progressively on the single-CPU offline runtime
- **THEN** all 512 SHALL overlap without counting queued work as active, preserve content and usage, and complete without local-capacity errors
- **AND** measured memory, CPU, latency and workload limitations SHALL be reported without claiming production capacity for arbitrary contexts

### Requirement: Optional Codex model catalog fields retain nullable wire semantics

The authenticated Codex model catalog, its client-version `/v1/models` alias and their trailing-slash equivalents SHALL encode absent `default_verbosity`, `default_reasoning_level` and `minimal_client_version` values as JSON null rather than empty strings. Recognized declared enum defaults and nonempty version values SHALL remain unchanged; unsupported default enum values SHALL be null rather than fabricated replacements. Catalog repair MUST NOT change stored model configuration, capabilities, prices, authorization or generation requests. Source-only forwarding overrides MUST remain excluded from published metadata.

#### Scenario: A source does not declare verbosity or reasoning defaults
- **WHEN** an authorized client retrieves its Codex catalog
- **THEN** the optional defaults are null and the catalog is usable by the supported Codex client without an invalid enum value
- **AND** no substitute verbosity or reasoning capability is invented

#### Scenario: A model declares supported defaults
- **WHEN** declared verbosity is low, medium or high and reasoning uses a recognized Codex effort
- **THEN** the catalog preserves those values and any nonempty minimum client version

#### Scenario: A default uses an unsupported enum value
- **WHEN** a catalog model contains an unknown default verbosity or reasoning enum
- **THEN** that optional field is null without discarding the model or changing its persisted configuration

### Requirement: Subscription compaction uses the in-band Responses contract

Standalone and HTTP-triggered subscription compaction SHALL post to the private `/responses` endpoint with `store=false`, `stream=true` and one terminal `compaction_trigger`, preserving existing owner, policy, session and billing constraints. The adapter SHALL accept bounded valid SSE with an optional absent Content-Type and the original-compatible JSON response form. It SHALL collect terminal output or reconstruct missing terminal output from indexed item events and unindexed done items. A successful compact SHALL expose a `response.compaction`-compatible envelope containing the encrypted compact item, using explicit compaction items before the last message-shaped encrypted summary. Missing response IDs alone SHALL NOT invalidate compaction. Actual usage and service tier MUST survive output normalization and error classification.

#### Scenario: Upstream returns a streamed compact result
- **WHEN** upstream completes an in-band compact operation
- **THEN** public compact and trigger callers receive a usable compact item with actual accounted usage
- **AND** the old private `/responses/compact` route is never used

#### Scenario: Upstream omits the terminal output array
- **WHEN** completed output items arrived before a valid terminal response
- **THEN** the adapter reconstructs the compact output in index order followed by unindexed done items, within bounded memory

#### Scenario: An operation fails after upstream output
- **WHEN** a compact stream has emitted output and then fails or disconnects
- **THEN** no authentication or quota replay duplicates that operation
- **AND** valid reported usage settles once while unknown usage remains reserved for reconciliation

#### Scenario: Compaction holds account admission
- **WHEN** the first valid upstream event arrives
- **THEN** create capacity is released while stream capacity remains owned until attempt completion or cancellation

### Requirement: Definitive request validation rejection releases an unused reservation

An ordinary Responses or translated Chat request receiving an actual upstream HTTP 400 or 422 with a valid `invalid_request_error` envelope and no output, nested accepted response, usage or duration SHALL settle its unused reservation as a failed zero-charge attempt. This classification MUST NOT be inferred from HTTP status alone, malformed/truncated error bodies, an in-stream error event or an error carrying billing/output evidence. It MUST NOT authorize replay, change account quota health, mark an account healthy or erase prior unknown reservations. Valid reported billing SHALL still settle once; incomplete reported billing and ambiguous dispatched failures SHALL remain pending.

#### Scenario: An unsupported parameter is refused before streaming
- **WHEN** a complete upstream HTTP 400 response declares `invalid_request_error` without billing or output
- **THEN** the client receives the error and the failed request releases only its own unused reserve
- **AND** the key can make a subsequent permitted request without a reconciliation lock caused by that refusal

#### Scenario: A rejection cannot prove the attempt unused
- **WHEN** an error is malformed, in-stream, not a validation envelope, or carries output, usage or duration
- **THEN** no definitive-zero classification is inferred from status and existing confirmed/unknown accounting rules remain in force

### Requirement: Codex Realtime preserves private call transport and bounded sideband messages

The authenticated Realtime call route SHALL preserve supported JSON, SDP and multipart request bytes, complete Content-Type parameters and bounded query values when forwarding to the subscription account. Unsupported encodings, malformed media-type metadata and oversized requests SHALL fail before dispatch. Successful SDP and Location responses SHALL be returned only after key-scoped call ownership is durable; query and fragment values in Location MUST NOT enter the call identifier. Realtime request/response payloads MUST NOT be archived as diagnostics, and upstream failure bodies, headers and arbitrary codes MUST NOT appear in client errors or content-free request statistics. Existing key/group/account authorization SHALL remain enforced.

Both sideband peers SHALL accept messages up to the shared 4 MiB bound and reject larger messages without unbounded buffering. Normal close and cancellation SHALL release relay tasks and admission; an abnormal upstream close MUST NOT be reported as successful completion. Accounting SHALL record a sanitized terminal outcome after cancellation without using the cancelled request context for its cleanup write.

#### Scenario: Codex creates a private call
- **WHEN** an authenticated client sends a supported bounded JSON, SDP or multipart offer with query parameters
- **THEN** upstream receives the original bytes, full media type and query, and the client receives the SDP answer and Location after durable owner binding

#### Scenario: Call creation fails with private content
- **WHEN** upstream rejects a call, a transport fails, or owner binding cannot be committed
- **THEN** only a fixed safe failure code/message and appropriate HTTP status are exposed
- **AND** neither SDP/ICE content, response error text nor Location secrets enter diagnostics or request-log error fields

#### Scenario: A sideband transports a larger audio/control frame
- **WHEN** a permitted peer sends a valid frame larger than 32 KiB but no larger than 4 MiB
- **THEN** it is relayed unchanged in both directions
- **AND** an oversized message or abnormal upstream close terminates safely with a failed content-free outcome, not an artificial successful close

### Requirement: Codex control aliases preserve canonical behavior

The Go runtime SHALL accept GET and POST at `/backend-api/codex/thread/goal/get`. Retained Codex control routes and Realtime call creation SHALL also accept their single trailing-slash equivalent with the same authentication, request limits and owner policy. Dispatch SHALL use the canonical upstream path without changing the incoming method, body or query. Unsupported methods MUST remain rejected and aliases MUST NOT bypass authorization or expose a usable Realtime call before owner binding.

#### Scenario: Codex reads a goal using POST
- **WHEN** an authorized client sends a POST body to the goal-read route or its trailing-slash equivalent
- **THEN** the provider receives that same method/body/query on the canonical goal-read path

#### Scenario: A client uses a trailing slash
- **WHEN** a client calls a retained control or Realtime creation route with one trailing slash
- **THEN** the canonical handler's authorization and behavior apply without an unnecessary redirect

### Requirement: Realtime always requires a real API key

Realtime call creation and both sideband path aliases SHALL authenticate an active user API key through Bearer authentication even when general API-key enforcement is disabled and the peer is locally trusted or explicitly allowed by an unauthenticated-client CIDR. They MUST NOT substitute the internal local principal or an administrator session. A call created with one key MUST remain inaccessible to another key. Ordinary local/CIDR keyless Responses behavior SHALL remain unchanged.

#### Scenario: A trusted keyless caller starts or attaches a call
- **WHEN** the caller provides no valid API key while general keyless access is enabled
- **THEN** Realtime returns 401 before creating a provider call or opening a WebSocket

#### Scenario: Another key attempts to attach
- **WHEN** a call is owned by key A and valid key B requests either sideband alias
- **THEN** the owner lookup fails closed without an upstream sideband connection

### Requirement: Realtime protocol negotiation preserves native client variants

Realtime call creation and sideband requests SHALL forward bounded `OpenAI-Alpha` and `OpenAI-Beta` negotiation values without accepting arbitrary client headers or replacing provider-owned authentication/account identity. Responses-only Beta tokens (`responses=experimental` and `responses_websockets` declarations) MUST NOT enter Realtime upstream requests. Invalid or oversized negotiation headers SHALL be rejected before provider dispatch or WebSocket upgrade.

The runtime SHALL support `/v1/realtime?call_id=<id>` and its trailing-slash equivalent for v1/v2 sidebands, forwarding to the upstream query-based `/realtime` endpoint. Exactly one valid call ID is required and SHALL retain the same active Bearer key/account-generation owner checks as v3 `/live/<id>` and backend aliases. V3 path-based requests MUST reject query-supplied call IDs. All variants SHALL retain bounded frames, private failures, cancellation cleanup and sanitized accounting.

#### Scenario: Native Codex selects frameless Realtime
- **WHEN** call creation or v3 attachment carries `OpenAI-Alpha: quicksilver=v2`
- **THEN** the subscription transport preserves that value with the selected account's own authentication

#### Scenario: A v1/v2 client attaches by query
- **WHEN** an authenticated client supplies one owned `call_id` on the legacy route
- **THEN** the upstream request uses `/realtime` with that single call ID and other bounded query values, not the v3 live path

#### Scenario: A caller mixes ownership selectors
- **WHEN** a legacy route has zero/multiple call-ID values or a path-based route also supplies a call-ID query value
- **THEN** the request fails before WebSocket upgrade and upstream dispatch without changing ownership

### Requirement: Subscription non-streaming replies retain streamed output

Non-streaming subscription Responses and translated Chat Completions SHALL include completed output items received before the terminal event when that event has absent, null or empty output. Items SHALL retain their upstream fields and output-index ordering without duplicating indexed items. Nonempty terminal output SHALL remain authoritative. Response identity, terminal metadata and reported usage MUST be preserved, with one accounting settlement and no additional provider request. Malformed or oversized assembled output MUST fail safely without inventing usage, retrying an executed request or reporting an incomplete stream as successful. Streaming clients SHALL retain their original event flow without accumulating full output for this compatibility path.

#### Scenario: Luna completes with empty terminal output
- **WHEN** upstream emits a completed message or tool item and then a completed terminal event with empty output
- **THEN** non-streaming Responses contains the item and Chat Completions exposes its corresponding text or tool call
- **AND** actual terminal usage is settled once

#### Scenario: Terminal output is already complete
- **WHEN** the terminal event contains nonempty output alongside earlier item events
- **THEN** that terminal output is preserved without appending duplicate items

#### Scenario: Collection fails after dispatch
- **WHEN** the stream is incomplete or output collection exceeds its bound
- **THEN** the request fails without automatic replay and retains known usage or the existing uncertain-reservation behavior

### Requirement: Translated Chat streaming usage preserves upstream token counts

When a Chat Completions client requests `stream_options.include_usage` and the Responses provider reports valid usage, the translated final usage chunk SHALL preserve input, output, cached-input and reasoning counts in their corresponding Chat Completions fields, with total tokens equal to input plus output. This conversion MUST NOT change the ledger or cause another request.

#### Scenario: Subscription Responses returns snake_case usage
- **WHEN** upstream completes with nonzero `input_tokens`, `output_tokens` and token-detail fields
- **THEN** the Chat stream returns matching `prompt_tokens`, `completion_tokens`, total and detail counts rather than zeroes

### Requirement: Responses routes take precedence over Realtime call aliases

The fully composed runtime SHALL dispatch authenticated GET WebSocket upgrades and POST Responses requests on `/backend-api/codex/responses` and `/v1/responses`, including their single trailing-slash variants, to Responses handling. A Realtime call-ID wildcard MUST NOT interpret the reserved `responses` path as a call identifier. Existing key, firewall, capability and owner validation SHALL remain unchanged. Retained Realtime aliases SHALL continue to enforce key-scoped call ownership.

#### Scenario: Native Codex connects using the legacy base URL
- **WHEN** an authenticated client upgrades `/backend-api/codex/responses` with the Realtime routes also installed
- **THEN** the server accepts a Responses socket rather than returning `realtime_call_owner_not_found`
- **AND** validation of subsequent Responses frames uses the existing key and account policy

#### Scenario: A real call identifier uses the Realtime alias
- **WHEN** an authenticated client requests `/backend-api/codex/rtc_owned`
- **THEN** the server applies Realtime call ownership and does not treat that identifier as a Responses route

### Requirement: Dashboard account credits reflect durable capacity and upstream refusal

Authenticated dashboard account summaries SHALL expose the observed applicable window metadata, known calculated subscription capacity and remaining credits, and persisted purchased-credit has/unlimited/balance fields separately. Unknown values MUST remain null, known exhausted balances MUST remain zero, and unlimited credits MUST retain their explicit flag. Purchased credits MUST NOT increase calculated subscription capacity. Account summaries and new-request selection SHALL use consistent effective quota status. Usable purchased credits SHALL permit exhausted included windows in both selection and transactional reservation; an explicit finite nonpositive balance SHALL override a generic has-credits flag unless unlimited is true. A stored upstream quota refusal MUST NOT be cleared for new admissions by a credit observation at or before that refusal. Newer usable credit evidence MAY restore credit-backed admission. Administrator-disabled states, actual rate-limit blocks, authentication, key/group/model restrictions and existing continuation ownership MUST remain enforced.

#### Scenario: Known, unknown and unlimited balances
- **WHEN** the overview contains accounts with observed subscription windows and purchased-credit metadata
- **THEN** account cards receive the same independent balance values as the account list, including zero and unlimited
- **AND** unknown plans or absent credit metadata are not represented as an invented balance

#### Scenario: Included quota exhausted but purchased credits remain
- **WHEN** a permitted account has exhausted its included primary or long window and has usable purchased credits without a newer upstream refusal
- **THEN** selection and reservation admit the request without marking the account exhausted solely because of included quota
- **AND** later explicit zero-credit metadata removes that override for new requests

#### Scenario: Confirmed refusal after a credit snapshot
- **WHEN** OpenAI refuses quota after the stored positive-credit observation
- **THEN** subsequent new requests and reservations cannot use that old balance to reopen the account
- **AND** normal failure settlement and eligible-account failover remain unchanged

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

A grouped key's report SHALL include a separate aggregate percentage of used subscription quota across the group's non-deleted ChatGPT accounts when upstream-quota visibility is permitted by the global setting and the key's account-pool visibility setting. The group and its account membership MUST be derived in the same authorized read snapshot as the peer key summary. Each account SHALL contribute at most once per window regardless of membership in other groups. The percentage SHALL equal total estimated used subscription credits divided by total known subscription capacity for that window. Primary, weekly and monthly windows MUST NOT be combined. Missing, expired or unknown-capacity observations MUST NOT be treated as zero consumption; the report SHALL identify the number of contributing accounts and total subscription accounts so partial coverage is explicit. Unknown windows SHALL display unavailable rather than zero. Purchased credits MUST NOT be included in this subscription percentage or treated as exhausted when subscription usage reaches 100%. Only group-level totals, percentages and counts SHALL be exposed, never provider account identities, individual balances or credentials. Ungrouped callers and callers with hidden upstream quotas SHALL receive no such aggregate.

#### Scenario: Accounts with different capacities
- **WHEN** a group contains a Plus account at 100% weekly usage and a Pro account at 0%
- **THEN** weekly usage is weighted by their existing subscription-credit capacities, not the arithmetic average of percentages
- **AND** accounts outside the group cannot affect the result

#### Scenario: Missing or hidden quota information
- **WHEN** only some group accounts report a known current window or upstream visibility is disabled
- **THEN** partial observations identify their coverage, absent observations are unavailable, and hidden aggregates remain absent

### Requirement: Changes and releases pass automated gates on a patched toolchain

The module SHALL pin a Go toolchain without known reachable standard-library vulnerabilities. Pull requests and `main` SHALL run vet, lint, unit tests, race tests, vulnerability scanning, dashboard lint/typecheck/tests and OpenSpec validation without provider credentials. A pushed `go-vX.Y.Z` tag SHALL build the dashboard and the Linux amd64/arm64 binaries in CI, rerun the gates, and publish deterministic archives whose layout is accepted by the runtime updater together with `SHA256SUMS` and the release notes file for that tag. Building the same commit twice MUST produce identical checksums. A clean checkout without built dashboard assets SHALL compile and pass tests.

#### Scenario: A release tag is pushed
- **WHEN** an operator pushes `go-v1.0.11` after merging its release notes
- **THEN** CI publishes `codex-lb-go-v1.0.11-linux-amd64.tar.gz`, `codex-lb-go-v1.0.11-linux-arm64.tar.gz` and `SHA256SUMS` only after the gates pass
- **AND** each archive contains exactly `codex-lb`, `LICENSE`, `LICENSE.codex-relay` and `codex-lb.service`
- **AND** no running installation is updated by publication

#### Scenario: Reproducing a release locally
- **WHEN** a maintainer runs `scripts/release.sh go-v1.0.11 <dir>` on the tagged commit
- **THEN** the produced `SHA256SUMS` equals the published one

### Requirement: Capacity defaults follow the detected host

At startup the runtime SHALL detect the CPUs (effective GOMAXPROCS), the memory available to the process (the smallest of the cgroup v2/v1 limit along its cgroup path and physical memory, assuming 1 GiB when unknown) and the descriptor limit, and SHALL derive its bounds from that profile. Memory tiers SHALL be: below 768 MiB — 256 generations and 2048 client WebSockets; below 1.75 GiB — 512 and 4096; below 3.5 GiB — 1024 and 8192; below 7 GiB — 2048 and 16384; otherwise 4096 and 32768. The queue SHALL be a quarter of the generation bound (64…1024); the request-body budget SHALL be one eighth of the sizing memory (at least 64 MiB) with a 50% per-key share; upstream WebSocket sessions and connections per upstream host SHALL equal the generation bound; SQLite readers SHALL be 2×CPU+4 within 8…32. Client WebSockets SHALL be reduced so that sockets, twice the generation bound and 256 reserved descriptors fit the descriptor limit, never below 64.

The Go soft memory limit SHALL default to 75% of available memory. An exported GOMEMLIMIT SHALL be kept and used for sizing; `--memory-limit off` SHALL leave the runtime limit unset and `--memory-limit <size>` SHALL set it and size from it. `--max-generations`, `--max-client-websockets`, `--body-memory-budget` and `--body-memory-key-share` SHALL override the derived values; invalid values MUST fail startup naming the flag. The runtime SHALL log the detected profile and effective limits before serving, and `GET /api/runtime/capacity` (administrator only) SHALL return them with the overridden limits and current usage (active and queued generations, client WebSockets, retained request-body bytes and body rejections, Go heap, process RSS). Host-derived bounds MUST NOT bypass per-account, per-key or source caps.

#### Scenario: The service moves from a 1 GB to a 4 GB host
- **WHEN** the same binary starts on a 6-CPU host whose process may use 3.8 GiB
- **THEN** it admits up to 2048 generations with a 512-request queue, 16384 client WebSockets, a ~486 MiB request-body budget and 16 SQLite readers, and sets a 75% Go soft memory limit
- **AND** on a 1 GB host it keeps 512 generations, a 128-request queue and 4096 client WebSockets with a request-body budget of one eighth of its memory (about 120 MiB when it reports 960 MiB)

#### Scenario: A container or systemd unit limits memory
- **WHEN** the process runs under `MemoryMax=900M` on a larger machine
- **THEN** sizing uses the 900 MiB cgroup limit and reports `cgroup_v2` as its source

#### Scenario: The operator overrides capacity
- **WHEN** the operator starts with `--max-generations 300 --memory-limit 2GiB --body-memory-budget 1GiB`
- **THEN** 300 generations, a 75-request queue, a 2 GiB Go soft limit and a 1 GiB request-body budget are in effect and the capacity endpoint lists the three overrides

### Requirement: Requests and background work are observable without exposing secrets

Every HTTP request and WebSocket SHALL receive a server-generated `X-Request-Id` and SHALL produce one structured log line when it ends, with method, path without query, status, response size, duration, resolved client IP, route group and the stable `error.code` of our JSON error envelope when present. Failures SHALL be logged at INFO (4xx) or WARN (5xx) and successes at DEBUG; `--log-level` SHALL select the minimum level. Request bodies, query strings, credentials and provider error messages MUST NOT be logged. Background loops (usage polling, automations, model catalog, maintenance) SHALL recover panics with their stack, report every run, log failures by a stable class without message text — immediately on a new class, at most every ten minutes while it persists, and once on recovery — and SHALL start after a bounded jitter. Startup SHALL log the effective configuration summary and drain SHALL log its duration and whether the grace deadline cancelled work. The final startup error SHALL be a single JSON line.

#### Scenario: A request is rejected for lack of accounts
- **WHEN** a client request fails with 503 `no_available_accounts`
- **THEN** one WARN line with the request ID, path, status and `error_code` is logged and the same ID is returned in `X-Request-Id`
- **AND** no request body, query or credential appears in the log

#### Scenario: A background poll keeps failing
- **WHEN** the usage poll fails with the same class on consecutive runs
- **THEN** the failure is logged once, again after ten minutes, and a recovery line is logged after the next success
- **AND** a panic in a run is logged with its stack and the loop continues

### Requirement: Runtime metrics are available on an opt-in private listener

When `--metrics-listen` is set, the runtime SHALL serve Prometheus text metrics and pprof on that separate listener: HTTP requests and durations by fixed route group and status class, in-flight requests, active and queued generations and client WebSockets against their limits, process and Go memory, goroutines, uptime, accounts by status, database and WAL size, background runs by result with their last success time, and build version. Labels MUST NOT contain account, key or request identifiers. The listener SHALL be off by default and SHALL warn when bound beyond loopback. On the public listener `/metrics` and `/debug` SHALL return 404 rather than the dashboard shell.

#### Scenario: An operator scrapes metrics
- **WHEN** the server starts with `--metrics-listen 127.0.0.1:9464`
- **THEN** `GET http://127.0.0.1:9464/metrics` returns the metric families in text format
- **AND** `GET /metrics` on the public address returns 404

### Requirement: Deployment templates restart, bound descriptors and sandbox the service

The systemd template SHALL restart indefinitely with backoff, set a descriptor limit sufficient for the host-sized socket budget, allow at least the drain grace plus accounting cleanup to stop, and apply systemd sandboxing that keeps `/proc/meminfo` and the cgroup filesystem readable for capacity detection. The Docker image SHALL disable the built-in version manager, because containers update by replacing the image, and SHALL document a stop timeout of at least 120 seconds.

#### Scenario: A transient startup failure
- **WHEN** the listen port is briefly busy at boot
- **THEN** systemd keeps retrying with increasing delay instead of leaving the unit failed

### Requirement: Pooled upstream WebSockets deliver frames only to their own response

The runtime SHALL hand an upstream WebSocket frame only to the response whose response.create is currently in flight on that socket. A frame read while no response owns the socket, or after the owning response stopped reading, SHALL be dropped and SHALL cause every later reuse of that socket to be preceded by a successful protocol ping round trip. An event announcing or terminating a response whose ID differs from the current response, a second response.created with another ID, or a terminal repeating the socket's previous response ID SHALL fail the attempt as an invalid stream and retire the socket; such a terminal MUST NOT settle the request, and the existing unknown-usage reconciliation SHALL apply.

#### Scenario: A frame arrives after a terminal while the socket is idle
- **WHEN** upstream sends frames after a response's terminal event and the socket is reused
- **THEN** the next response SHALL receive none of those frames
- **AND** the socket SHALL be reused only after a successful ping, keeping its exact continuation

#### Scenario: A terminal belongs to another response
- **WHEN** a leased response receives a terminal whose response ID differs from its response.created or equals the previous response on the socket
- **THEN** the attempt SHALL fail as an invalid stream without emitting that terminal or settling its usage
- **AND** the next request SHALL use a new upstream connection

### Requirement: Reused idle upstream WebSockets are proven alive before dispatch

Before writing response.create on a pooled upstream WebSocket that has been idle for at least twenty seconds, or that sent unsolicited frames, the runtime SHALL ping it under the lease with at most three seconds to receive pong. On failure it SHALL discard the socket; a request without previous_response_id SHALL continue on another socket or a new handshake, and an exact continuation SHALL be rejected as continuation-not-found before execution. A recently used socket without unsolicited frames SHALL be reused without an extra round trip. Request cancellation during this check SHALL release the lease without discarding the socket. The response.create write SHALL be bounded by thirty seconds.

#### Scenario: An idle socket died silently
- **WHEN** a pooled socket stopped answering while idle and a new request leases it
- **THEN** the request SHALL be sent on a new upstream connection within the ping bound
- **AND** no response.create SHALL be written on the dead socket

#### Scenario: An exact continuation anchor died while idle
- **WHEN** a continuation's exact socket fails the liveness check
- **THEN** the request SHALL be rejected before execution and its unused reservation released
- **AND** it MUST NOT move to another connection or be replayed

#### Scenario: A warm socket is reused
- **WHEN** a socket is reused shortly after its previous response without unsolicited frames
- **THEN** response.create SHALL be written without a preceding ping

### Requirement: Upstream HTTP connections are health-checked and reusable

The shared upstream HTTP transport SHALL send HTTP/2 health pings after thirty seconds without frames, fail a connection whose ping is unanswered for ten seconds or whose writes stall for thirty seconds, limit the per-stream receive buffer to 1 MiB, and resume TLS sessions from a bounded client session cache. After a Responses terminal event or a Chat `[DONE]` marker, an HTTP/1.x response body SHALL be drained for at most 64 KiB and 100 milliseconds before closing so that a keep-alive connection can be reused; draining MUST NOT change the result.

#### Scenario: HTTP/1.1 body ends shortly after the terminal
- **WHEN** an HTTP/1.1 upstream ends each stream body a few milliseconds after its terminal event
- **THEN** sequential streams SHALL reuse one upstream connection

#### Scenario: Upstream keeps the body open after the terminal
- **WHEN** the body does not end after the terminal
- **THEN** the stream SHALL complete with its terminal result after the drain bound

### Requirement: Upstream streams are bounded by size and time

Upstream SSE and WebSocket response streams SHALL be bounded by 256 MiB of total event payload per response, the existing 16 MiB per-event bound and the overall response deadline, not by an event count. Exceeding the payload bound SHALL fail the stream as incomplete.

#### Scenario: A long response has many small events
- **WHEN** a healthy upstream sends more than one hundred thousand small events before its terminal
- **THEN** the response SHALL complete with its terminal usage

### Requirement: Native Codex SSE waits keep the client idle timer alive

During a dispatched native `/backend-api/codex/responses` HTTP/SSE response and its trailing-slash equivalent, the runtime SHALL send a heartbeat as an SSE data event after ten seconds without any real downstream write: `response.in_progress` with the real current response ID when known, otherwise `codex.keepalive`. Generic `/v1/responses` SSE SHALL keep SSE comment heartbeats. Heartbeats SHALL NOT be sent before dispatch, and MUST NOT change visible-output classification, usage, ownership or retry eligibility.

#### Scenario: Native SSE reasoning is silent
- **WHEN** a dispatched native SSE response emits nothing for more than ten seconds
- **THEN** the client SHALL receive a data-event heartbeat without fabricated usage

#### Scenario: Events flow continuously
- **WHEN** real events are written at least every ten seconds
- **THEN** no heartbeat SHALL be inserted between them

#### Scenario: A generic SSE client waits
- **WHEN** a dispatched `/v1/responses` SSE response is silent
- **THEN** it SHALL receive only SSE comment heartbeats

### Requirement: Downstream WebSocket turns carry protocol pings

During an active Responses WebSocket turn on every Responses WebSocket route, the runtime SHALL send a protocol ping every thirty seconds. The ping is transport traffic, not an application event; turn completion MUST NOT cancel an in-flight ping write, and pings SHALL stop when the connection handler exits.

#### Scenario: A generic WebSocket turn is silent
- **WHEN** a `/v1/responses` WebSocket turn waits longer than the ping interval
- **THEN** the client SHALL observe protocol pings and no synthetic application events

### Requirement: Chat-source function calls stream before completion

For Chat-compatible sources, the runtime SHALL accumulate tool-call arguments in linear time. When the first arguments delta of a declared non-custom function tool arrives with a known call ID and name, the runtime SHALL emit `response.output_item.added` and stream `response.function_call_arguments.delta` before the upstream `[DONE]`; custom tools and calls without ID or name SHALL keep terminal-time emission. A call whose name or ID changes after streaming started, an undeclared tool or a duplicate call ID SHALL fail the response as an invalid tool call, and the completed item SHALL keep the streamed item ID.

#### Scenario: A long function call is generated
- **WHEN** a Chat source streams a declared function call in many argument deltas
- **THEN** the client SHALL receive the added item and argument deltas before the completed response
- **AND** the final item SHALL contain the complete arguments

#### Scenario: A streamed call changes its name
- **WHEN** a name fragment arrives after the call started streaming
- **THEN** the response SHALL fail as an invalid tool call

### Requirement: Realtime sessions bound writes and detect dead peers

Codex Realtime proxying SHALL bound each write in either direction by thirty seconds; a failed write SHALL end the session as a disconnect with joined pump activity. The upstream socket SHALL be protocol-pinged after thirty seconds without received frames with ten seconds to receive pong, and an unanswered ping while nothing was received and no frame waits for the client SHALL end the session as a disconnect. The downstream socket SHALL receive protocol pings every thirty seconds as intermediary keepalive.

#### Scenario: A realtime client stops reading
- **WHEN** the downstream realtime client stops reading while upstream sends frames
- **THEN** the session SHALL end within the write bound

#### Scenario: The realtime upstream goes silent and stops answering
- **WHEN** the upstream sends nothing and does not answer a ping
- **THEN** the session SHALL end as a realtime disconnect within the ping bound

### Requirement: Operation requests resolve client identity like Responses

Compact, file, transcription, control and realtime operation requests SHALL resolve the client IP through the configured trusted proxies exactly as Responses requests do, and SHALL derive the user-agent group from the User-Agent product token rather than a client-supplied group header.

#### Scenario: Compact behind a trusted proxy
- **WHEN** a compact request arrives through a trusted proxy with a forwarded client address
- **THEN** the operation SHALL be attributed to the forwarded client address

### Requirement: Native chat completions render incomplete Responses terminals

Streaming `/v1/chat/completions` over a Responses source SHALL render `response.incomplete` as a final chunk with `finish_reason` `length`, or `content_filter` when that is the incomplete reason, followed by optional usage and `[DONE]`; every chunk SHALL carry `created` and `model`. Accounting of the attempt SHALL be unchanged.

#### Scenario: Output limit reached after partial content
- **WHEN** a Responses source ends a chat-translated stream with response.incomplete for max_output_tokens
- **THEN** the client SHALL receive the partial content, a `length` finish reason and `[DONE]` instead of an error chunk

### Requirement: Provider-side conversation state binds a request to its account

A Responses request whose top-level `conversation` is present and is neither `null` nor an empty string SHALL be account-bound in the same way as a request referencing an account-owned file. Such a request MUST NOT be replayed on another account, MUST NOT be rebuilt from local history without its `conversation`, and its successful response SHALL be stored as an account-bound continuation so later `previous_response_id` turns are not replayed across accounts either.

#### Scenario: Quota refusal of a conversation-bound request
- **WHEN** a request carrying `conversation` is refused for quota by its first account before any visible output
- **THEN** the proxy SHALL return HTTP 409 `quota_failover_requires_context`
- **AND** no second provider attempt SHALL be made and no request without `conversation` SHALL be sent

#### Scenario: Follow-up of a conversation-bound response
- **WHEN** a later request continues a stored conversation-bound response with `previous_response_id` and its owner refuses quota
- **THEN** the proxy SHALL report the truthful continuation failure instead of replaying local history on another account

### Requirement: A response terminal follows one durable finalization

After an upstream Responses attempt returns, the runtime SHALL persist its settlement or retained uncertain usage, its account outcome with any continuation quota mark, and its continuation with session and turn-state aliases in one durable commit. A settlement failure SHALL persist none of them. An account-outcome failure SHALL keep the settlement and skip ownership publication. A continuation or alias failure SHALL keep the settlement and the account outcome. The buffered terminal event SHALL be delivered to the client only after that commit; when the outcome or ownership could not be persisted the client SHALL receive the error instead of the terminal, so no client observes a response whose `previous_response_id` is unknown.

#### Scenario: A successful streamed response completes
- **WHEN** a streamed attempt completes with known usage and a session identity
- **THEN** settlement, the healthy account outcome, the response continuation and its session alias SHALL be committed together before the terminal event is sent

#### Scenario: Ownership persistence fails after settlement
- **WHEN** the continuation cannot be stored after a successful settlement
- **THEN** the settlement SHALL remain finalized exactly once
- **AND** the client SHALL receive an error rather than the terminal event

#### Scenario: Settlement cannot be persisted
- **WHEN** the settlement write fails
- **THEN** no account outcome, continuation or alias SHALL be persisted and the client SHALL receive `usage_settlement_failed`

### Requirement: Queued durable writes are not starved by other transactions

Every runtime SQLite mutation SHALL be executed by the grouped batch writer; only schema migration and the offline legacy import MAY open a direct writer transaction. The batch execution deadline SHALL start after the writer connection is obtained, so time spent waiting behind another transaction MUST NOT fail queued reservations or settlements. Dashboard reports and request-log listing SHALL use a separate bounded read pool so analytics cannot occupy the proxy's read connections.

#### Scenario: A queued reservation waits behind another transaction
- **WHEN** another transaction holds the writer connection longer than the batch execution deadline
- **THEN** a queued reservation SHALL execute after the connection is released and succeed

#### Scenario: Many concurrent dashboard reports run
- **WHEN** dashboard analytics saturate their read pool
- **THEN** proxy authentication, selection and continuation reads SHALL still use free connections of the main read pool

### Requirement: Conversation aliases share their owner's stored context

Session and turn-state alias rows SHALL reference the stored context of the response they point to instead of storing a copy. Looking up an alias SHALL return that response's context while it is retained. Continuation record and byte bounds SHALL count each stored context once and SHALL be enforced without scanning all continuations on every save.

#### Scenario: A session alias is saved with its response
- **WHEN** a response with a session identity is remembered
- **THEN** the alias row SHALL hold no encrypted context of its own
- **AND** a lookup through the alias SHALL return the response's context

#### Scenario: Upgrading a database with copied alias contexts
- **WHEN** schema 33 is upgraded
- **THEN** existing alias rows SHALL keep their own context and the bounds counters SHALL equal a full recount

### Requirement: First-run dashboard setup always requires a one-time bootstrap token

In standard dashboard authentication mode, password setup SHALL require a bootstrap token for every request, independent of socket peer, `Host` and forwarded headers. `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`, when set, MUST contain 32 to 1024 printable characters without spaces or `serve` SHALL refuse to start. When no dashboard password exists and no unconsumed operator token is configured, startup SHALL write a random token to `<data-dir>/dashboard-bootstrap-token` with mode 0600 and log the file path but never the token. A successful setup SHALL invalidate the token used: the generated file is removed and the SHA-256 of an operator token is persisted so that value is refused after restarts. Removing the dashboard password SHALL publish a new generated token. Setup attempts SHALL count against the `bootstrap` authentication failure scope. The session response SHALL report whether a token is required and whether it comes from the environment or the generated file.

#### Scenario: Loopback setup without a token is refused
- **WHEN** a setup request arrives from `127.0.0.1` with `Host: 127.0.0.1:2455` and no forwarded headers but without a bootstrap token
- **THEN** the response is 403 `invalid_bootstrap_token` and no password is stored

#### Scenario: A generated token works exactly once
- **WHEN** the server starts without a password and without an operator token
- **THEN** a 0600 token file exists in the data directory, its path is logged and its value is not
- **AND** setup with that token succeeds and removes the file
- **AND** after the password is removed a different token is published and the previous one is refused

#### Scenario: A short operator token is rejected at start
- **WHEN** `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` has fewer than 32 characters
- **THEN** `serve` exits with a configuration error that does not echo the token

### Requirement: Keyless proxy access requires an explicit client network

While API-key authentication is disabled, a protected proxy route without a Bearer key SHALL use the local proxy principal only when the resolved client address belongs to a configured `--unauthenticated-client` network. The resolved client SHALL be the raw peer when no forwarded header is present and the peer is not a trusted proxy, or the trusted-proxy-resolved client when the peer is a trusted proxy. A trusted proxy without forwarded client identity, and forwarded headers from an untrusted peer, MUST NOT qualify. Socket or `Host` locality MUST NOT grant keyless access. `serve` SHALL refuse unauthenticated-client networks that overlap a trusted-proxy network and SHALL log a warning when such a network contains loopback addresses. Required-capability requests, usage self-service and Realtime SHALL keep requiring a real key. The dashboard SHALL display a warning on every page while API-key authentication is disabled.

#### Scenario: Loopback without an explicit network requires a key
- **WHEN** API-key authentication is disabled and a loopback client calls `/v1/models` without a key and no unauthenticated-client network is configured
- **THEN** the response is 401

#### Scenario: Proxied client inside the network
- **WHEN** a trusted proxy at `127.0.0.1` forwards client `10.1.2.3` and `10.0.0.0/8` is an unauthenticated-client network
- **THEN** the request proceeds without a key
- **AND** the same request without `X-Forwarded-For`, or with a forwarded client outside the network, or from an untrusted peer, is rejected with 401

### Requirement: Proxy trust and secure cookies are explicit operator configuration

The first request carrying forwarded client headers from an untrusted loopback peer SHALL produce one WARN log naming the peer and the `--trusted-proxy` remedy. `--public-origin` SHALL accept only an `https://host[:port]` origin without path, query or credentials. When it is set, or the connection uses TLS, dashboard cookies SHALL be `Secure`, the administrator cookie SHALL be named `__Host-codex_lb_dashboard_session` with `Path=/` and no `Domain`, and responses SHALL include `Strict-Transport-Security`. The public origin SHALL be trusted by cross-origin protection. Dashboard and report cookies SHALL use `SameSite=Strict`. The shipped systemd unit SHALL pass `--trusted-proxy 127.0.0.1/32 --trusted-proxy ::1/128`.

#### Scenario: Secure mode cookie
- **WHEN** the server runs with `--public-origin https://lb.example` and an administrator completes setup over plain HTTP from the proxy
- **THEN** the cookie is `__Host-codex_lb_dashboard_session`, `Secure`, `HttpOnly`, `SameSite=Strict`, and the response carries HSTS

#### Scenario: Untrusted forwarding is reported once
- **WHEN** two requests with `X-Forwarded-For` arrive from a loopback peer that is not a trusted proxy
- **THEN** exactly one warning is logged

### Requirement: Authentication endpoints bound request bodies and password hashing

Requests to `/api/dashboard-auth/*` and `/api/key-reports/session` SHALL be limited to 8 KiB of body, including bodies without a declared length, and SHALL answer 413 `request_too_large` above it. Password hashing and comparison SHALL run outside the authentication state lock with at most four concurrent operations; a request that finds no free slot SHALL immediately receive 429 `authentication_busy` with `Retry-After: 1` and MUST NOT spend its attempt budget.

#### Scenario: Oversized login body
- **WHEN** a login request carries a 9 KiB body
- **THEN** the response is 413 and no password hash is computed

#### Scenario: Saturated hashing fails fast
- **WHEN** all hashing slots are busy
- **THEN** a login receives 429 with `Retry-After: 1` and session state reads still succeed

### Requirement: Authentication failures are limited per client and per installation

For each scope (`password`, `totp`, `bootstrap`) the system SHALL allow 8 attempts per minute per client, grouping IPv6 clients by /64 and IPv4 clients by address. The system SHALL also count failures per scope for the whole installation: after 10 failures without a 15-minute quiet period, attempts from every client SHALL be refused until `min(2^(n−10) seconds, 15 minutes)` after the last failure, where `n` is the failure count, answered as 429 `login_rate_limited` with `Retry-After`. A success SHALL reset only the caller's per-client window. Rejected attempts SHALL be logged at WARN with scope reason and client address and MUST NOT log submitted secrets.

#### Scenario: Failures from many addresses lock the installation
- **WHEN** 10 wrong passwords arrive from 10 different client addresses
- **THEN** the correct password from an eleventh address receives 429 with `Retry-After: 1`
- **AND** one more failure after that delay raises the next `Retry-After` to 2

#### Scenario: IPv6 prefix grouping
- **WHEN** 8 attempts arrive from different addresses in one IPv6 /64
- **THEN** the next attempt from that /64 is refused while another /64 is not

### Requirement: A configured TOTP is required at every sign-in

When a TOTP secret is configured, a dashboard session SHALL be fully authenticated only after TOTP verification, regardless of `totp_required_on_login`. Password-only sessions SHALL expire after at most 5 minutes. The settings API SHALL report `totpRequiredOnLogin` as the configured state and SHALL ignore attempts to change it. The TOTP settings screen SHALL state that a code is required at every sign-in while TOTP is configured and SHALL NOT offer a toggle.

#### Scenario: Password-only session after TOTP setup
- **WHEN** TOTP was confirmed and an administrator signs in with the password only
- **THEN** `/api/settings` answers 401 until `/api/dashboard-auth/totp/verify` succeeds
- **AND** `GET /api/settings` reports `totpRequiredOnLogin: true` even after a settings update sends `false`

### Requirement: Dashboard sessions are revocable and expire when idle

Dashboard session tokens SHALL carry a random session id, the installation session epoch, an absolute expiry and an idle deadline; tokens of the earlier format SHALL be rejected. `POST /api/dashboard-auth/logout` SHALL revoke the presented session until its absolute expiry. `POST /api/dashboard-auth/logout-all` SHALL require a fully authenticated session and SHALL advance the epoch so every previously issued session is rejected. A session without an authenticated request for 12 hours SHALL be rejected; authenticated requests SHALL extend the idle deadline at most every 10 minutes without extending the absolute expiry. The absolute lifetime SHALL be the configured dashboard session lifetime (default 24 hours) capped at 30 days for every client. The dashboard SHALL offer "sign out everywhere" in its session settings.

#### Scenario: Logout revokes a copied cookie
- **WHEN** a session cookie is copied and the session then logs out
- **THEN** the copied cookie receives 401 on `/api/settings`

#### Scenario: Sign out everywhere
- **WHEN** two sessions exist and one calls `logout-all`
- **THEN** both receive 401 afterwards

#### Scenario: Idle session
- **WHEN** 12 hours pass without an authenticated request
- **THEN** the session is rejected although its absolute expiry has not passed

### Requirement: Model-source destinations are public unless explicitly allowed

Without `--allow-private-model-sources`, creating a model source or changing its base URL to `localhost`, a `.localhost` name, or a literal loopback, private, link-local, carrier-grade NAT, multicast, unspecified, reserved or IPv4-embedding IPv6 address SHALL return 422. Without that flag every model-source connection SHALL resolve its destination and dial only checked public unicast addresses; a name resolving to any non-public address SHALL be refused before a connection is made. When an environment HTTP proxy is used, the destination name SHALL be checked before the request is handed to the proxy and the proxy endpoint SHALL remain dialable. Redirect responses from model sources SHALL NOT be followed. Subscription, OAuth and release traffic SHALL keep its existing path.

#### Scenario: Metadata address rejected at creation
- **WHEN** an administrator creates a source with base URL `http://169.254.169.254/latest`
- **THEN** the response is 422 and nothing is stored

#### Scenario: Name resolving to loopback refused at dial
- **WHEN** a stored source's host name resolves to `127.0.0.1`
- **THEN** the request fails before any connection to the loopback address

#### Scenario: Self-hosted source with the opt-in
- **WHEN** `--allow-private-model-sources` is set
- **THEN** a source at `http://127.0.0.1:11434/v1` can be created and used

### Requirement: Dashboard responses carry a content security policy

Dashboard and admin API responses SHALL include a Content-Security-Policy that allows scripts, connections, fonts and styles only from the same origin (inline styles allowed), images from the same origin plus `data:` and `blob:`, and sets `object-src 'none'`, `base-uri 'none'`, `form-action 'self'` and `frame-ancestors 'none'`. Proxy routes SHALL carry `default-src 'none'; frame-ancestors 'none'; sandbox`.

#### Scenario: Policy headers
- **WHEN** a browser loads `/` or `/api/settings`
- **THEN** the response carries the dashboard policy
- **AND** `/v1/models` carries the sandbox policy

### Requirement: Sensitive administrator actions require the current password

In standard dashboard authentication mode, `POST /api/accounts/{id}/export/auth`, `POST /api/runtime/updates/apply` and `POST /api/runtime/updates/rollback` SHALL require `currentPassword` in the JSON body. The password SHALL be verified under the `password` failure scope; a missing or wrong value SHALL return 403 `reauthentication_required` without performing the action and without ending the session. Trusted-header and disabled modes SHALL NOT require it. The dashboard SHALL prompt for the password before these actions.

#### Scenario: Export without the password
- **WHEN** a fully authenticated administrator exports account tokens without `currentPassword`
- **THEN** the response is 403 `reauthentication_required` and contains no tokens

### Requirement: Disabled dashboard authentication is limited to loopback listeners

`--dashboard-auth-mode=disabled` SHALL be accepted only with a loopback `--listen` address and SHALL log a startup warning that every client reaching the listener is an administrator.

#### Scenario: Disabled mode on all interfaces
- **WHEN** `serve` is started with `--dashboard-auth-mode disabled --listen 0.0.0.0:2455`
- **THEN** it exits with a configuration error

### Requirement: Error archives redact common credential formats

Error-archive redaction SHALL mask, in addition to bearer values, OpenAI-style keys and JWTs, AWS access key IDs, Google API keys, GitHub and Slack tokens, PEM private-key blocks and URL user-info credentials, while preserving surrounding text.

#### Scenario: Free-text credentials
- **WHEN** an archived diagnostic message contains an AWS access key ID and `https://user:secret@db.example/x`
- **THEN** the stored message contains `[redacted]` in their place and keeps `https://[redacted]@db.example/x`

### Requirement: Self-update verifies signed release checksums

When the running binary embeds at least one trusted ed25519 release public key, the updater SHALL download `SHA256SUMS.sig` and SHALL refuse the update unless the base64 signature over the exact `SHA256SUMS` bytes verifies with an embedded key; this check SHALL complete before the archive is downloaded or extracted, and a refused update SHALL leave the staging directory empty. When no key is embedded, the updater SHALL keep checksum-only verification so earlier releases remain installable. The release workflow SHALL sign `SHA256SUMS` with the PKCS#8 PEM ed25519 key from the `RELEASE_SIGNING_KEY` secret, publish `SHA256SUMS.sig`, and fail when the build embeds trusted keys but the secret is missing or does not match an embedded key. Key rotation SHALL be possible by embedding several keys.

#### Scenario: Signature matrix
- **WHEN** a build embeds a trusted key and a release offers a valid signature, no signature, a signature by another key, or a modified `SHA256SUMS`
- **THEN** only the valid signature produces an extracted binary and no other case downloads the archive

#### Scenario: Transition release
- **WHEN** a build embeds no key
- **THEN** a release with only `SHA256SUMS` is installed after its checksum matches and no signature is requested

### Requirement: Quota history stays bounded and is read per current series

The runtime SHALL store at most one quota-history row per account window and five-minute UTC bucket: an observation in the latest row's bucket with the same reset time and window length SHALL update that row, while a later bucket, an older observation or a changed reset time or window length SHALL insert a row. Current quota state SHALL remain unchanged by this rule. Dashboard overview and projections SHALL read, for each current account window, every retained observation in the six-hour pace horizon plus the 64 newest observations of the lookback, through the series index and without a global row limit; the read MUST NOT fail because of the number of accounts.

#### Scenario: Frequent polling does not multiply history rows
- **WHEN** an account window is polled every minute without a reset
- **THEN** history contains one row per five-minute bucket whose values equal the latest observation in that bucket

#### Scenario: A reset inside a bucket is preserved
- **WHEN** the reset time changes between two polls in the same five-minute bucket
- **THEN** both the pre-reset and the post-reset observations remain in history

#### Scenario: Many accounts
- **WHEN** 200 or more accounts each have a day of one-minute history
- **THEN** the dashboard overview and projections complete without an observation-limit error

### Requirement: Online backups are verified snapshots without the encryption key

The runtime SHALL create database backups without stopping request handling by running `VACUUM INTO` through a dedicated connection that cannot modify the source, writing to a temporary file in the destination directory. Before publication the snapshot SHALL be flushed to stable storage and SHALL pass `PRAGMA quick_check`, carry the application id and a supported schema version, and decrypt its runtime identity probe and every stored credential with the installation key. Publication SHALL be an atomic rename followed by a directory flush; a failed attempt MUST remove its temporary file and MUST NOT remove an existing backup. Snapshots SHALL preserve the implicit rowids used as reservation fences. The encryption key MUST NOT be written to the backups directory or included in a downloadable snapshot, and every backup listing SHALL state that the key must be stored separately.

#### Scenario: Backup while requests are being settled
- **WHEN** a backup runs while reservations are created and settled
- **THEN** the published snapshot passes quick_check and opens as a runtime database with the same key
- **AND** request handling continues during the snapshot

#### Scenario: Verification fails
- **WHEN** the snapshot does not pass quick_check or cannot be decrypted with the installation key
- **THEN** no backup file is published and the attempt is reported as failed

#### Scenario: Downloaded snapshot without the key
- **WHEN** an administrator downloads a backup
- **THEN** the file contains no encryption key and the listing warns that the key is stored separately

### Requirement: Backups are scheduled, rotated and bounded by free space

The backup schedule SHALL persist an interval of 0–168 hours (0 disables scheduled backups) and a keep count of 1–30, defaulting to every 24 hours and 3 backups, and changes SHALL apply without restart. A scheduled backup SHALL start when the newest backup is older than the interval, checked periodically after a startup delay. After a successful backup, backups beyond the newest keep count SHALL be removed; skipped or failed attempts MUST NOT remove backups. Only one backup SHALL run at a time; a concurrent request SHALL be rejected as busy. A backup SHALL be refused when the estimated snapshot would leave less than 5 % of the filesystem free, bounded to at least 256 MiB and at most 2 GiB. The last attempt's time, trigger and outcome SHALL be visible to the administrator.

#### Scenario: Rotation after a scheduled backup
- **WHEN** a scheduled backup succeeds with keep count 3 and four older backups exist
- **THEN** exactly the three newest backups remain

#### Scenario: Not enough disk space
- **WHEN** the estimated snapshot would leave less than the free-space reserve
- **THEN** no snapshot is written, existing backups remain and the outcome reports insufficient space

### Requirement: Backup administration is administrator-only

`GET /api/backups`, `POST /api/backups`, `PUT /api/backups/schedule` and `GET /api/backups/{name}` SHALL require an authenticated dashboard administrator and SHALL keep the existing cross-origin protection for unsafe methods. The download SHALL reject a request whose `Sec-Fetch-Site` header is present and is neither `same-origin` nor `none`, SHALL accept only names of the backup file pattern, SHALL serve only files inside the backups directory and SHALL respond with an attachment and `Cache-Control: no-store`. Proxy API keys MUST NOT access these routes.

#### Scenario: Unauthorized or cross-site access
- **WHEN** an unauthenticated caller, a proxy API key or a cross-site page requests a backup
- **THEN** the request is rejected without reading the file

#### Scenario: Traversal attempt
- **WHEN** the requested backup name contains a path or does not match the backup pattern
- **THEN** the request is rejected as not found

### Requirement: A CLI backup works for cron while the service runs

`codex-lb backup --data-dir DIR --output PATH` SHALL write a verified snapshot of DIR's database to the absolute PATH, or to a standard backup name inside PATH when it is an existing directory, while the service is running or stopped. The command MUST NOT take the service lock, migrate or write the source database, or overwrite an existing file, and SHALL print the written path and size as JSON.

#### Scenario: Cron backup of a running service
- **WHEN** the command runs against the data directory of a running service
- **THEN** it writes a verified snapshot and the service continues to own its database

### Requirement: Retention keeps up with operational growth

Retention SHALL delete in batches of at most 500 rows per batch-writer job and continue until a batch is short or a 20-second cycle budget is spent. Quota history SHALL be retained for 45 days when no override is stored (an explicit 0 disables it) and observations older than one day SHALL be thinned to the newest per account window, UTC hour, reset time and window length; the newest observation of a series MUST NOT be deleted. Request events MUST NOT be deleted unless request-log retention is configured. Finished automation runs SHALL be kept for the newest 100 cycles of each job, and pending or running runs and each job's latest cycle MUST NOT be deleted. Limit warm-up attempts SHALL be kept for the newest 100 per account; only attempts whose reset time is more than eight days old MAY be deleted and the newest attempt of an account MUST NOT be deleted.

#### Scenario: A large backlog
- **WHEN** 100 000 quota-history rows are older than the retention cutoff
- **THEN** one retention cycle deletes them without holding the writer for longer than one bounded batch

#### Scenario: Default retention on upgrade
- **WHEN** no retention override is stored
- **THEN** quota history older than 45 days is removed and request events are kept

### Requirement: Settled reservation pruning preserves fences

A usage reservation SHALL be deleted only when it is settled, was last updated more than seven days ago, is not referenced by a usage event, an account outcome or a continuation, and is not the reservation with the largest rowid. Reservations of request events removed by request-log retention SHALL be deleted in the same transaction under the same conditions. Reserved or reconciliation-pending reservations MUST NOT be deleted, and a reservation created after pruning MUST receive a larger rowid than every deleted reservation.

#### Scenario: Fences survive pruning
- **WHEN** settled reservations older than seven days are pruned
- **THEN** account outcomes, continuations and affinity versions keep resolving and comparing as before
- **AND** a new reservation's rowid is larger than every deleted rowid

### Requirement: WAL size and planner statistics are maintained

The writer connection SHALL set `journal_size_limit` to 64 MiB. A dedicated maintenance connection SHALL run a passive WAL checkpoint every minute and a truncating checkpoint with a busy timeout of at most one second when no frames remain uncheckpointed and the WAL file is not empty; a busy result SHALL be skipped until the next cycle. `PRAGMA optimize` with a bounded analysis limit SHALL run through the batch writer once a day and when the store closes; a failure MUST NOT prevent closing.

#### Scenario: WAL growth during long reads
- **WHEN** long readers prevented checkpoints and then finish
- **THEN** the next maintenance cycle checkpoints and the WAL file no longer exceeds the size limit

### Requirement: Reports and request logs use bounded queries

Admin report responses SHALL be computed with a number of database statements independent of the number of days, and their totals, daily sums, breakdowns, distinct conversation and account counts and comparisons SHALL equal an evaluation over the retained raw events and folded history for every filter, time zone and window. Daily median TTFT, queue latency and tokens per second SHALL come from fixed log-linear histograms and be within 1/64 of the exact median plus rounding; costs SHALL be summed as integer microdollars before conversion. Request-log pages SHALL be ordered through an index on `(requested_at, request_id)`, and the request-log API SHALL accept an optional opaque keyset `cursor`, returned as `nextCursor` when more rows exist, that returns rows strictly after the last returned row in the existing descending order, while offset paging remains supported and the total, conversation aggregate and filter options ignore the cursor. Open reservations SHALL use a partial index on reserved rows. Pending request-log entries, reservations still reserved, flagged for reconciliation and without a usage event, SHALL be filtered, counted, grouped into filter options and paged through a covering partial index on flagged reserved reservations, reading at most the first `offset + limit + 1` of them for a page, and their entries, filter options, totals and cursors SHALL equal an evaluation over every reservation.

#### Scenario: Daylight-saving week
- **WHEN** a report window spans a daylight-saving transition in the selected time zone
- **THEN** daily sums equal the raw evaluation and daily medians are within the histogram bound

#### Scenario: Keyset page
- **WHEN** a client passes a page's `nextCursor` as `cursor`
- **THEN** the response equals the next offset page without scanning the skipped rows

#### Scenario: Interrupted usage awaiting reconciliation
- **WHEN** about 74 thousand of 1.6 million reservations are flagged for reconciliation and the request-log options and first page are requested
- **THEN** the pending entries are read from the pending index, reservation rows only for the entries shown, and the entries, options, total and cursor equal the evaluation over every reservation

### Requirement: Storage bookkeeping is atomic and bounded

Deleting an automation job SHALL delete the job and its runs in one transaction and SHALL return any failure. Settling or releasing a reservation whose limit was lowered by repricing SHALL clamp the limit at zero instead of skipping the adjustment. Error-archive and resource-owner bounds SHALL be enforced from counters maintained in the same transaction as each insert or delete, evicting the oldest rows in the previous order. Opening a store with a relative filesystem path SHALL resolve it to an absolute path.

#### Scenario: Release after a downward reprice
- **WHEN** a reservation's limit consumption was reduced below its reserved amount and the reservation is released
- **THEN** the limit's current value becomes zero rather than keeping the reserved amount

### Requirement: Request bodies share a byte-weighted memory budget

Every proxy route that reads a request body — Responses over HTTP and WebSocket, Chat Completions, embeddings, compact, Codex control, file creation, transcription, realtime call creation and public warmup — SHALL charge that body to one request-body budget counted in bytes and shared by all routes, under the authenticated key, as its bytes arrive and before retaining them, so that no retained byte is uncharged. A declared Content-Length above the route maximum SHALL be rejected with 413, and a declared length the free budget cannot hold SHALL be refused, both before any byte is read; a declared length MUST NOT be reserved, so a request that declares a length and sends nothing holds nothing. A gzip body SHALL be charged and bounded by its decoded bytes. Past the route maximum a body SHALL receive 413 even when the budget is full: the byte that proves it is too large is read without charge. The charge SHALL be held until the request finishes using the body and MUST be released exactly once on success, validation failure, upstream failure, client cancellation, disconnect and mid-read rejection.

A request body MUST keep arriving: when no byte arrives for 10 seconds, or the body is still incomplete 2 minutes after reading started (the server ReadTimeout), the request SHALL end with 408 `request_body_timeout` and release its charge. A body that keeps arriving more slowly is not cut short, and the deadline MUST NOT outlive the body into the response.

The budget SHALL default to one eighth of the sizing memory and never less than 64 MiB (128 MiB at exactly 1 GiB; about 120 MiB on a VPS reporting 960 MiB). One API key SHALL hold at most its share of the budget (50% by default, never less than the smaller of the budget and the 32 MiB maximum body); keyless local traffic is one internal principal bounded only by the whole budget. `--body-memory-budget <size>` (at least 32 MiB) and `--body-memory-key-share <percent>` (1–100, where 100 disables the per-key bound) SHALL override the defaults; invalid values MUST fail startup naming the flag.

When the budget or the key's share cannot hold a body, the runtime SHALL return the existing 503 `local_capacity_exceeded` envelope with Retry-After without dispatching upstream, reserving key usage or changing account health. This local pressure MUST NOT be classified as provider quota or authorize failover. The budget counts request-body bytes; parsing, prepared payloads, history and outputs have additional workload-dependent costs, so it MUST NOT be presented as a hard process-memory limit. `GET /api/runtime/capacity` SHALL report the budget, the per-key bound, the retained bytes and the rejection count, and the dashboard capacity card SHALL show retained bytes against the budget.

#### Scenario: A burst of large bodies exceeds the budget
- **WHEN** clients send more concurrent large compact or Responses bodies than the budget can hold
- **THEN** the bodies that fit are processed normally
- **AND** each excess request receives 503 `local_capacity_exceeded` with Retry-After before any upstream dispatch
- **AND** once the requests finish the retained body bytes return to zero

#### Scenario: A chunked or compressed body grows past the free budget
- **WHEN** a body without a declared length, or a gzip body, decodes to more bytes than the budget has free but no more than the route maximum
- **THEN** reading stops at the budget, the request receives 503 `local_capacity_exceeded` and its partial charge is released

#### Scenario: Admitted requests fail independently
- **WHEN** one admitted request fails upstream, another is cancelled by its client and a third fails validation while a fourth is still streaming
- **THEN** the first three release their charges exactly once and the fourth keeps its own charge until it ends

#### Scenario: One key reaches its share
- **WHEN** one API key holds its share of the budget and sends another large body
- **THEN** that request receives 503 `local_capacity_exceeded`
- **AND** a request from another key that fits the remaining budget is served

#### Scenario: WebSocket and HTTP bodies share the budget
- **WHEN** HTTP bodies hold the budget and a client sends a large response.create on an admitted WebSocket
- **THEN** only that message receives local capacity and the socket stays usable

#### Scenario: Declared bodies that never arrive hold nothing
- **WHEN** clients of two keys send request heads declaring 32 MiB bodies and no body bytes
- **THEN** the budget holds nothing for them and other requests from both keys are served

#### Scenario: An upload stops arriving
- **WHEN** a client sends part of a declared body and then nothing for 10 seconds
- **THEN** the request ends with 408 `request_body_timeout` and its charge is released
- **AND** a client that keeps sending slowly is served and its streamed response is not cut by the body deadline

#### Scenario: An oversized body on a small budget
- **WHEN** the key's free share is no larger than the 32 MiB maximum and a gzip or chunked body exceeds the maximum
- **THEN** the request receives 413 `request_too_large` without Retry-After and is not counted as a budget refusal

### Requirement: Writes serialize across processes of one installation
Every write transaction of the writer and maintenance connections SHALL begin `IMMEDIATE`, so concurrent writers of two worker processes wait on the busy timeout instead of failing a read-then-write transaction with a stale snapshot.

#### Scenario: Two workers settle requests at once
- **WHEN** two worker processes reserve and settle usage concurrently on one database
- **THEN** every transaction commits and no settlement fails because the other process wrote first

### Requirement: Interrupted reservations belong to dead processes
A new usage reservation SHALL record the instance id of the process that created it. At startup a worker SHALL mark as needing reconciliation only `reserved` rows whose owner is not a live worker of the installation; rows without an owner SHALL count as not alive. When a worker exits, the active worker SHALL apply the same rule.

#### Scenario: A new worker starts beside a serving one
- **WHEN** a worker starts while the previous worker has reservations in flight
- **THEN** those reservations stay unflagged and settle normally

#### Scenario: The previous worker is killed mid-request
- **WHEN** a retiring worker exits with an unsettled reservation
- **THEN** that reservation is marked as interrupted usage

### Requirement: Older code may open an additively migrated database
Each migration SHALL be classified as additive or not. Migrating SHALL record the lowest code schema that may operate on the database. Code whose schema is older than the database but not older than that floor SHALL open it without migrating or rewriting the schema version; otherwise it SHALL refuse the database as before.

#### Scenario: A newer additive schema
- **WHEN** a schema-41 binary opens a schema-42 database whose recorded floor is 40
- **THEN** it opens and serves without changing the schema version

#### Scenario: A newer breaking schema
- **WHEN** the recorded floor is above the binary's schema
- **THEN** the binary refuses the database

### Requirement: A lost token-refresh race uses the winner's token
When an OAuth refresh fails permanently but the stored refresh token of the same account generation changed meanwhile, the refresh SHALL return the stored current access token and SHALL NOT mark the account for re-authentication.

#### Scenario: Two processes refresh one account
- **WHEN** two workers refresh the same account token at the same moment and the provider rejects the second as reused
- **THEN** the second request continues with the token the first worker stored

### Requirement: Analytics read incremental usage rollups

The dashboard overview, admin reports, key reports, key usage and trends, request-log filter options and totals, and the conversation list and details SHALL be computed from hour-bucket rollups combined, in one read snapshot, with journal rows not yet folded, events after the fold cursor and raw events of partial hours, so their cost is bounded by the rollup rows of the window rather than by the number of events. When the unfolded tail or journal exceeds its bound, or the rollup layout, status, cursor anchor or import watermark does not match, they SHALL use the raw-event evaluation, which SHALL return identical results; request-log search and conversation filters, and conversation reads while removals of conversation events are pending, SHALL use it too.

#### Scenario: Seven-day dashboard on production volume
- **WHEN** the dashboard overview is requested for seven days with about 200 thousand events in that window
- **THEN** it reads rollup rows of the window and the raw events of at most four partial hours

#### Scenario: Retention removed conversation events
- **WHEN** journal rows that remove events of a conversation are not folded yet
- **THEN** the conversation list and details come from the raw evaluation, with exact first and last requests

#### Scenario: Folder behind after an upgrade
- **WHEN** existing events have not been backfilled yet
- **THEN** analytics responses come from the raw evaluation and equal what the rollups return once folded

### Requirement: Usage rollups stay consistent with every writer

A background folder run only by the active worker SHALL fold events after its cursor in bounded batches through the batch writer, each batch atomic with its cursor. Updates and deletions of folded events SHALL be journaled by triggers in the same transaction by every worker version, including repricing, retention and account history deletion. Inserts by any worker, including an older one during an update overlap, SHALL be folded after their commit. A cursor anchor mismatch, a changed import watermark, a negative measure or a verifier mismatch SHALL make the folder rebuild the rollups while reads use the raw evaluation. Migration 42 SHALL create only empty tables, triggers and the state row.

#### Scenario: Old worker writes during an overlap
- **WHEN** a previous worker settles requests and runs retention while the active worker folds
- **THEN** analytics responses equal the raw evaluation after every step

#### Scenario: Repricing folded history
- **WHEN** a model price change reprices events already folded
- **THEN** the cost delta is journaled in the repricing transaction and reports reflect it immediately

### Requirement: Unreconciled usage is released after one hour

A reservation flagged for reconciliation (interrupted owner or unreported usage) SHALL stay reserved for one hour after its creation, during which the offline reconciliation command can still settle it. After that the active worker SHALL release it without charging through the ordinary release settlement, refunding limit deltas only within their current window and recording no usage event, in bounded batches during its maintenance pass. Reservations that are not flagged SHALL never be released this way.

#### Scenario: A backlog of interrupted usage
- **WHEN** a database holds flagged reservations older than one hour
- **THEN** maintenance releases them in batches, they disappear from pending request-log entries, and reports and key usage totals do not change

#### Scenario: A request still in flight
- **WHEN** a reservation older than one hour is not flagged because its request is still running
- **THEN** it stays reserved and is settled by its request

### Requirement: Admin pages poll once and read the shell status from that poll

Every administrator page SHALL issue at most one periodic request: its page endpoint, or `GET /api/dashboard/status` every 60 seconds while visible on a page without one. `GET /api/dashboard/status` SHALL return the shell status: `ready` (the readiness of `/health/ready`), `lastSyncAt` (the newest quota observation of non-deleted ChatGPT accounts), `usageFoldedAt` and `rollupsUsable` (the usage rollup state), `reauthRequiredAccounts`, `availableResetCredits` (from the in-memory reset-credit snapshot), `showResetCreditBadges`, `apiKeyAuthEnabled` and the routing settings shown in the status bar. The shell status SHALL be computed from in-memory state and a fixed number of batch reads, independent of the number of accounts and keys. A page endpoint that carries the shell status SHALL return it as `shell`; the header badge, the API-key authentication warning and the status bar SHALL read it from that response or from `GET /api/dashboard/status` and SHALL NOT poll `/api/accounts`, `/api/settings` or `/health/ready`.

#### Scenario: Overview carries the shell status

- **WHEN** an administrator keeps the Overview page open
- **THEN** the only periodic request is `GET /api/overview`, whose `shell` updates the header badge and the status bar
- **AND** no request is made to `/api/accounts` or `/health/ready`

#### Scenario: Page without its own poll

- **WHEN** an administrator keeps the Settings page open
- **THEN** `GET /api/dashboard/status` is requested every 60 seconds while the tab is visible

#### Scenario: Status cost does not grow with the installation

- **WHEN** the shell status is computed for 9 or for 100 accounts
- **THEN** the same number of SQL statements is executed

### Requirement: Global search finds keys, groups and accounts

`GET /api/directory` SHALL return the non-deleted, non-internal API keys (id, name, prefix, group id, status `active`, `disabled` or `expired`), the account groups with their non-deleted member account count and non-deleted key count, and the non-deleted accounts (id, label as alias or else e-mail, e-mail, status, kind) in a number of statements independent of their count. The dashboard search, opened with Ctrl+K or Cmd+K, SHALL match keys by name and prefix, groups by name and accounts by alias, e-mail and id case-insensitively, SHALL show at most 20 results grouped by kind and SHALL navigate to the entity's page; an input shaped like a request id SHALL offer to open `/requests?request_id=<id>`. The directory SHALL be cached for 5 minutes and invalidated by key, group and account mutations.

#### Scenario: Prefix match

- **WHEN** the operator types the first characters of a key prefix
- **THEN** that key is listed under Keys and selecting it opens the key

#### Scenario: Deleted keys are not listed

- **WHEN** a key is deleted or is an internal key
- **THEN** it is absent from `GET /api/directory` and from the group's key count

### Requirement: Replaced dashboard routes redirect

`/` and `/dashboard` SHALL redirect to `/overview`, `/apis` SHALL redirect to `/keys` with its query string, `/groups` SHALL open the Groups page, `/groups?selected=<id>` SHALL open that group's detail sheet, and `/settings#account-groups` SHALL redirect to `/groups`, carrying its `group` parameter as `selected`. The navigation SHALL list Overview, Groups, Accounts, Keys, Reports, Automations and Settings in that order, and the Accounts entry SHALL carry a badge with the number of accounts needing re-login, or otherwise the available reset credits when reset-credit badges are enabled.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows `/keys?selected=key_1`

#### Scenario: Old group link

- **WHEN** an operator opens `/settings?group=grp_1#account-groups`
- **THEN** the dashboard shows `/groups?selected=grp_1` with that group's sheet open

### Requirement: The overview summarizes pool, groups, alerts and attention keys in one bounded read

`GET /api/overview?timezone=<IANA>` (default UTC; an unknown zone SHALL return 400 `invalid_timezone`) SHALL return the pool summary, one row per account group, a row for ungrouped keys, the alerts and at most 10 keys needing attention for the requested zone in one response, using a number of SQL statements independent of the number of keys, groups and accounts. Spend SHALL be estimated cost in USD for the current local day and for each of the 7 local days ending today, read from the usage rollups combined with the journal, the unfolded tail and raw partial hours; while the rollups are usable the overview SHALL NOT read raw usage events beyond those sources. Group spend SHALL be the sum over the group's current non-deleted, non-internal member keys; the ungrouped row SHALL sum the keys without a group and SHALL use the whole account pool. Pool spend, requests and error rates SHALL include all reported traffic. While the rollups are unusable the overview SHALL compute today's values and the error windows from raw events of at most the last 26 hours only and SHALL return null 7-day trends and a null spend at the same time yesterday.

#### Scenario: Key moved between groups

- **WHEN** a key that spent yesterday in group A now belongs to group B
- **THEN** its spend of every day counts in group B and not in group A

#### Scenario: Statement count at scale

- **WHEN** the overview is computed for 30 and for 300 keys with the same number of days
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Rollups catching up

- **WHEN** the rollups are not usable after an upgrade
- **THEN** today's spend equals the raw evaluation of today's events and every 7-day trend is null

### Requirement: The overview page has fixed periods and one poll

The Overview page SHALL render the alerts, the pool strip, the groups table and the keys needing attention in that order, SHALL have no period picker and SHALL label every metric with its fixed period (today, last hour, 7 days, current 5 h or weekly window). It SHALL poll only `GET /api/overview`, every 30 seconds while the tab is visible and on focus, and SHALL keep showing the previous data with a stale label and a retry action when a refetch fails. Expanding a group row SHALL show its problem accounts (at most 10) and its top keys by spend today (at most 5) from the same response.

#### Scenario: Failed refetch

- **WHEN** a refetch of the overview fails after data was shown
- **THEN** the previous data stays visible with a "stale since" label and a retry action

#### Scenario: Rollups unusable

- **WHEN** the response reports that the rollups are not usable
- **THEN** the page shows that usage analytics are catching up and hides 7-day trends

### Requirement: Group window health is capacity-weighted and projected from account burn

Pool and group window health SHALL be capacity-weighted over non-deleted ChatGPT accounts with a current observation of the window: remaining percent is the sum of remaining credits over the sum of known capacity, coverage SHALL be reported, and an unknown window SHALL be null, never zero. An account in several groups SHALL count fully in each group and once in the pool. The burn of an account SHALL be the positive used-percent deltas within one reset window over the trailing at most 5 hours of quota history, times capacity over 100, per hour, and SHALL be unknown with fewer than two observations. The projected exhaustion SHALL follow the runway $R(t) = \sum_a R_a - B\,(t - t_0) + \sum_{a:\ t_0 < \rho_a \le t} \min(C_a, C_a - R_a + b_a(\rho_a - t_0))$ over the accounts with known burn, and a group SHALL be marked as running out before reset only when the runway reaches zero before the latest reset of its covered accounts; no projection SHALL be made when the burn is unknown or zero. Account health SHALL be `reauth`, `paused`, `off` (deactivated), `limited` (rate limited, quota exceeded, or a window at 0 % without usable purchased credits), `low` (a window at or below 10 % remaining) or `ok`.

#### Scenario: A reset arrives before exhaustion

- **WHEN** an account of the group resets its window before the pooled remaining reaches zero
- **THEN** the projection adds back that account's used credits at its reset time

#### Scenario: No burn

- **WHEN** no account of the group has two observations in the last 5 hours
- **THEN** the window has no projected exhaustion and is not marked as running out

### Requirement: Group rows are ordered by attention

Group rows, including the ungrouped row, SHALL be ordered by attention score (2 with a critical alert, 1 with a warning alert, else 0), then by the number of problem accounts, then by member keys at or above 90 % of a limit, then by spend today, then by name. Limit fill of a key SHALL be the highest `current / max` over its active limits with expired windows counted as zero without changing storage; group fill SHALL be the highest member fill with the counts of members at or above 90 % and at or above 100 %.

#### Scenario: Critical group first

- **WHEN** one group has an account needing re-login and another spends more today
- **THEN** the group with the account needing re-login is listed first

### Requirement: Overview alerts follow fixed rules and are never stored

The overview SHALL evaluate exactly these rules on every read and SHALL NOT persist, acknowledge or send alerts:

| Rule | Subject | Warning | Critical | Action |
|---|---|---|---|---|
| `account_reauth` | account | — | status `reauth_required` | re-login |
| `quota_runway` | pool and each group | runs out before reset in the 5 h or weekly window | exhaustion within 1 h (5 h window) or 12 h (weekly), or remaining ≤ 5 % | open the group, or Accounts for the pool |
| `key_limit` | key | an active limit ≥ 90 % | an active limit ≥ 100 % | edit limits, or the group's limits for a grouped key |
| `error_spike` | pool | last hour: error rate ≥ 5 %, ≥ 20 errors and ≥ 3 × the trailing 24 h rate (floor 1 %) | last hour: error rate ≥ 20 % with ≥ 20 errors | open the failed requests of the last hour |
| `key_error_spike` | key | last hour: ≥ 20 errors and error rate ≥ 25 % | — | open the key's failed requests |
| `key_expiry` | key | an active key expires within 7 days | within 24 hours | extend the expiry by 30 days |
| `quota_data_stale` | pool | newest quota observation older than 10 min while ChatGPT accounts exist | older than 60 min | open Accounts |

Errors SHALL exclude `cancelled` requests and the `client_disconnected` error code; the last hour SHALL be the previous whole hour plus the current partial hour and the trailing window the 24 hours before it. The ungrouped row SHALL raise no `quota_runway` alert of its own. Every alert SHALL have a stable id `rule:subjectType:subjectId:window` and exactly one action that the page performs or navigates to without a request other than the action's own. Alerts SHALL be sorted critical first, then by the rule order of the table, then by value, and more than five subjects of one rule and severity SHALL collapse into one summary alert whose action opens the filtered list.

#### Scenario: Limit boundary

- **WHEN** a key's fullest active limit is at 89.9 %, 90 % or 100 %
- **THEN** it raises no alert, a warning or a critical `key_limit` alert respectively

#### Scenario: Many keys near their limits

- **WHEN** six keys are at or above 90 % of a limit
- **THEN** one summary warning with count 6 replaces their six alerts

### Requirement: Keys needing attention are ranked and actionable

The overview SHALL list at most 10 active, non-deleted, non-internal keys with at least one reason — `limit_blocked` (an active limit ≥ 100 %, critical), `limit_near` (≥ 90 %, warning), `error_spike` (the `key_error_spike` rule), `expiring` (within 7 days, critical within 24 hours) or `spend_spike` (spend today ≥ 2 × the average of the previous 7 local days and ≥ 10 USD) — ranked by highest severity, then reason order, then the reason's value, then spend today, and SHALL report how many keys have a reason in total. The primary action of the top reason SHALL be available inline; Extend SHALL set `expiresAt` to 30 days after the later of now and the current expiry and Disable SHALL set `isActive=false`, each after a confirmation and through `PATCH /api/api-keys/{id}`, after which the overview, the directory and the shell status are refetched. `GET /api/api-keys/{id}` SHALL return one key for the edit dialog.

#### Scenario: Extend an expiring key

- **WHEN** the operator confirms Extend on a key expiring in two days
- **THEN** the dashboard sends `PATCH /api/api-keys/{id}` with `expiresAt` 30 days after that expiry and refetches the overview

#### Scenario: Spend spike floor

- **WHEN** a key spends 8 USD today after spending 1 USD per day
- **THEN** it has no `spend_spike` reason

### Requirement: The Requests page holds the request log and conversations

The request log and the conversation list SHALL live on a Requests page with one period picker shared by both views (1 h, 24 h default, 7 d, 30 d), a Live toggle that is off by default, and no polling unless Live is on (30 seconds, first page only). Filter options SHALL be fetched only for the selected timeframe and only when the filter panel opens; key and account names SHALL come from the directory. The URL parameters `timeframe`, `status`, `api_key_id`, `account_id`, `request_id` and `conversation_id` SHALL prefill the filters. `GET /api/request-logs` and its options SHALL accept `timeframe=30d`, and `GET /api/conversations` SHALL accept `timeframe=1h` and `timeframe=24h`, in addition to their existing values.

#### Scenario: Default view

- **WHEN** an operator opens `/requests`
- **THEN** the request log of the last 24 hours is requested once and no further request follows while Live is off

#### Scenario: Alert link

- **WHEN** the operator follows the error-spike alert
- **THEN** the Requests page opens with the error status and the last hour selected

### Requirement: Public key-usage contracts stay unchanged

`GET /v1/usage`, `GET /api/codex/usage` and `GET /v1/usage/reports` SHALL keep their request parameters, response fields, authentication, caching headers and response encoding unchanged by the dashboard redesign.

#### Scenario: Existing client tools

- **WHEN** a key holder's tool calls `GET /v1/usage` after the upgrade
- **THEN** it receives the same fields with the same authentication and caching as before

### Requirement: Replaced admin endpoints are deprecated for one release

An admin endpoint that the embedded dashboard stops calling SHALL stay unchanged in the release that ships its replacement and SHALL be removed no earlier than the following release; release notes SHALL list each deprecation and removal. `GET /api/dashboard/overview` SHALL be deprecated by the overview. `GET /api/dashboard/projections` SHALL stay as the source of the Groups page pool runway card and SHALL NOT be removed while that card uses it.

#### Scenario: Tab opened before the update

- **WHEN** a browser tab loaded before the update still polls `GET /api/dashboard/overview`
- **THEN** the endpoint answers as before until the release after the one that deprecated it

#### Scenario: Pool runway card

- **WHEN** the Groups page loads
- **THEN** it requests `GET /api/dashboard/projections`, which answers as before

### Requirement: New admin endpoints follow the admin routing conventions

New administrator endpoints SHALL be registered behind administrator authentication and JSON compression with the existing trailing-slash conventions, and new key-owner endpoints SHALL live under `/api/key-reports/` so that the report cookie path covers them.

#### Scenario: Anonymous request

- **WHEN** `GET /api/overview` or `GET /api/directory` is requested without an administrator session
- **THEN** the response is 401 and no data is returned

### Requirement: Page endpoints meet a latency budget and report server time

With usable rollups, the main endpoint of each dashboard page SHALL answer in under 300 ms of server time, warm, on a synthetic store with 300 keys, 50 groups, 100 accounts and at least 1.5 million events over 30 days, and on a copy of production data. Page endpoints SHALL send `Server-Timing: app;dur=<ms>`, which proxy routes SHALL NOT send. While the rollups are usable, no request issued on page load SHALL read raw usage events outside the rollup journal, the unfolded tail and partial hours, except recent-request reads bounded by their limit and the request-log page bounded by its timeframe.

#### Scenario: Overview at target scale

- **WHEN** `GET /api/overview` is measured warm on the synthetic store at target scale
- **THEN** its server time is under 300 ms and the response carries `Server-Timing`

### Requirement: Reports for API-key users resolve a selection of groups and keys in one bounded read

`GET /api/reports/keys` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts only the repeatable `group_id` and `api_key_id` parameters, `ungrouped=1`, `start_date` and `end_date` (`YYYY-MM-DD`, default the 7 local days ending today) and `timezone` (IANA, default UTC). It SHALL resolve, in one analytics read snapshot, the selected groups to their current non-deleted, non-internal member keys and unite them with the explicitly selected keys of that kind and, with `ungrouped=1`, the keys without a group; an empty selection SHALL select every non-deleted, non-internal key, and unknown ids SHALL select nothing. It SHALL return the period with the previous period of the same length, the selection with its resolved key count, the totals (requests, input, output, cached input and reasoning tokens, estimated cost in USD, errors, cancelled), the previous period's totals or null when the previous period has no requests, the number of keys with requests, one row per group of the resolved keys and one for the ungrouped keys with key counts, measures and cost share, one row per resolved key with its group, status, measures, cost share, last use and fullest current limit, and one entry per local day with the day's totals and the measures of up to 8 series ranked by cost plus `other`: groups when the selection names more than one group (counting the ungrouped keys as one) or is empty and the resolved keys belong to more than one group, otherwise keys. Every measure of a key and local day SHALL equal the raw evaluation of the reported events and folded legacy hours of that key and day, cost up to the final rounding to six decimals; keys count in their current group for every day. While the rollups are usable the endpoint SHALL read key slices, folded legacy hours, the journal, the unfolded tail and raw partial hours only, with a number of SQL statements independent of the number of keys, groups and days. A period longer than 731 days, an inverted or malformed date SHALL return 400 `invalid_report_date_range`; an unknown parameter, a blank or longer than 256 characters id, more than 200 group ids or more than 1000 key ids or another `ungrouped` value SHALL return 400 `invalid_report_filters`; an unknown zone SHALL return 400 `invalid_timezone`; a selection whose resolved keys times days exceed 500 000 SHALL return 400 `report_too_large`.

#### Scenario: Group selected after a key moved

- **WHEN** a key that spent last week in group A now belongs to group B and the report selects group B
- **THEN** its spend of every day of the period counts in the report and in group B

#### Scenario: Ungrouped keys and pruned days

- **WHEN** the report selects only the ungrouped keys for a period whose first days were pruned into folded legacy hours
- **THEN** the per-key and per-day measures equal the raw evaluation of the ungrouped keys' events and legacy hours of each local day

#### Scenario: Statement count at scale

- **WHEN** the report is computed for 30 and for 300 keys over the same period
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Several groups

- **WHEN** the report selects three groups
- **THEN** the daily series are those groups ranked by cost, with `other` only when more than 8 series have requests

### Requirement: Key usage export is a per-day CSV that matches the report

`GET /api/reports/keys/export.csv` SHALL accept the parameters of `GET /api/reports/keys` with the same validation and SHALL answer `text/csv; charset=utf-8` with `Content-Disposition: attachment`, the header `date,group_id,group_name,key_id,key_name,requests,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,errors,cancelled` and one row per local day and resolved key with requests, ordered by date, group name with ungrouped keys last, key name and key id. `reasoning_tokens` SHALL be empty when unknown, `cost_usd` SHALL carry six decimals and text cells starting with `=`, `+`, `-`, `@`, tab or carriage return SHALL be prefixed with an apostrophe. The rows SHALL be computed in one analytics read snapshot that is closed before the first row is written, and their sums SHALL equal the totals of `GET /api/reports/keys` for the same parameters.

#### Scenario: Sums match the report

- **WHEN** an operator exports the CSV for the parameters of a report
- **THEN** the sums of every numeric column equal the report's totals, cost up to the six-decimal rounding of each row

#### Scenario: Idle keys

- **WHEN** a selected key has no requests on a day
- **THEN** the CSV has no row for that key and day

### Requirement: The Reports page reports on key users by selection and period

The Reports page SHALL show the report of `GET /api/reports/keys` for a selection of groups and keys chosen in a searchable picker over the directory (every key when nothing is selected) and one period picker (Today, Yesterday, 7 days by default, 30 days, 90 days, This month, Last month, or custom dates up to 730 days ending no later than today), SHALL encode the selection and the period in its URL so that a copied link reopens the same report, SHALL offer Export CSV for the same parameters, and per key an Open action to the key page and a Copy owner link action whose link opens the key sign-in with the report period and carries no credential. It SHALL load model, user-agent and latency details from `GET /api/reports` only when the operator expands them, filtered by the resolved keys when at most 50 are selected, unfiltered for every key, and otherwise SHALL ask the operator to narrow the selection. It SHALL NOT poll on an interval and SHALL refetch on window focus only when the period includes today and the data are older than 60 seconds. It SHALL NOT request `/api/accounts` or `/api/request-logs/options`.

#### Scenario: Shared link

- **WHEN** an operator copies the link of a report on two groups for the last 30 days and opens it in another tab
- **THEN** the same two groups and the last 30 days are selected and one `GET /api/reports/keys` request is sent

#### Scenario: Traffic details on demand

- **WHEN** the Reports page loads
- **THEN** no `GET /api/reports` request is sent until the operator expands the traffic details

#### Scenario: Idle tab

- **WHEN** a Reports page for the last 7 days stays open and visible
- **THEN** no further report request is sent until the window regains focus at least 60 seconds after the last response

### Requirement: The key page reads one key's period in one bounded read

`GET /api/api-keys/{id}/page` (administrator) and `GET /api/key-reports/page` (key owner) SHALL accept `start_date` and `end_date` (`YYYY-MM-DD`, inclusive local days, default the 7 days ending today, at most 730 days) and `timezone` (IANA, default UTC; an unknown zone SHALL return 400 `invalid_timezone`; an invalid range SHALL return 400 `invalid_report_date_range`) and SHALL return, for that key and period: the key's properties and status, its current limits, the period summary (requests, input, output, cached and reasoning tokens, cost, errors, cancelled, TTFT p50 and p95, total latency p50 and p95) with the requests, tokens and cost of the previous period of equal length, one row per local day (requests, tokens, cost, errors, the top five models of the period plus the rest, TTFT p50 and p95, total latency p50 and p95, queue latency p95, tokens per second p50), one row per model (requests, tokens, cost, share, TTFT p50), errors by code (count, share, last seen hour), user-agent groups (requests, cost, share) and the first 50 recent requests with a `nextCursor`. Usage SHALL be read from the usage rollups for whole hours combined with raw partial hours, the unfolded tail, the journal and folded legacy hours, in one analytics snapshot, with a number of SQL statements independent of the period length; while the rollups are unusable the same values SHALL be computed from raw events of the key. Percentiles SHALL use the rollup histograms' interpolated quantile and their documented relative bound, also on the raw path. Folded legacy hours carry no latency, user-agent groups or error codes; the response SHALL report from when these details are complete. Reading a key page MUST NOT mutate limit counters, reservations or any other stored state.

#### Scenario: Statement count does not grow with the period

- **WHEN** the key page is read for 7 days and for 90 days of the same key
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Rollups and raw events agree

- **WHEN** the key page usage is read in Europe/Prague or Asia/Kolkata while the folder stopped at a random cursor or journal rows are pending
- **THEN** every day, model, user-agent, account and error-code row equals the evaluation of the key's raw events

#### Scenario: Pruned days

- **WHEN** the period includes days whose events were pruned into legacy hourly rows
- **THEN** their requests, tokens and cost are included and the response states that latency, user agents and error codes are complete only from the first unpruned instant

### Requirement: Key limits show their reset and projected exhaustion

Each active limit of the key page SHALL show its type, window, model filter, current and maximum values, percent and reset time. An expired window SHALL show zero usage and the advanced reset without writing storage. The page SHALL project the exhaustion time from the key's consumption of the limit's measure in the last 3 hours (tokens or cost, restricted to the limit's model filter), and SHALL report it only when that pace reaches the maximum before the reset; a credits limit, a zero pace or a limit already at its maximum SHALL have no projection.

#### Scenario: Runs out before reset

- **WHEN** a daily cost limit is at 80 % with 6 hours to its reset and the last 3 hours spent 10 % of the maximum
- **THEN** the projected exhaustion is 6 hours from now, when the remaining 20 % is spent at that pace, which is not after the reset

#### Scenario: Lasts until reset

- **WHEN** the same pace would reach the maximum only after the reset
- **THEN** no projected exhaustion is reported

### Requirement: Key recent requests page through the key index

`GET /api/api-keys/{id}/requests` and `GET /api/key-reports/requests` SHALL accept `cursor` (an opaque keyset cursor from a previous `nextCursor`), `limit` (1 to 100, default 50) and `errors_only` (`true` or `false`) and SHALL return `{items, nextCursor}` for the key, newest first in `(requested_at, request_id)` order, read through `idx_usage_events_key_time` and merged with the key's pending entries of the request log. Cursor pages SHALL equal the corresponding offset pages. With `errors_only=true` the read SHALL examine at most 5 000 of the key's rows per call and return the failed ones (status other than success, completed or cancelled), with a `nextCursor` that continues after the last examined row when the bound was reached.

#### Scenario: Load more

- **WHEN** the dashboard passes a page's `nextCursor` as `cursor`
- **THEN** the next rows equal the following offset page without scanning the skipped rows

#### Scenario: Rare errors

- **WHEN** a key has one failed request among 20 000 successful ones
- **THEN** each errors-only call reads at most 5 000 rows and returns a cursor until the failed request is found

### Requirement: The key page is one dashboard component for the administrator and the owner

The administrator page `/keys/:id` and the key owner's page after key sign-in SHALL be one React component with an administrator and an owner mode. It SHALL show, in order: the header (name, prefix, status, group, created, last used, expiry), one period picker (Today, 7 days by default, 30 days, 90 days, custom up to 730 days) reflected as `from` and `to` in the URL, the current limits with the pool or group remaining, the period KPIs with the previous-period comparison, usage by day and model, errors by code, latency per day, user agents, recent requests with an errors-only toggle and "load more", and the key properties; the administrator mode adds the actions Edit, Edit limits or Open group limits, Move to group, Disable or Enable, Regenerate, Reset period (for a grouped key, naming the whole group), Delete and Copy owner link, the account, client IP and conversation of each request with its details, cost by account and lifetime totals labelled "Lifetime". The page SHALL poll its page endpoint every 60 seconds while visible only when the period includes today. The owner mode SHALL NOT mount the administrator navigation or issue administrator requests, SHALL end the session on a 401 like the key report and SHALL keep showing the previous data with a retryable error on other failures.

#### Scenario: Historical period does not poll

- **WHEN** the operator selects a period that ends yesterday
- **THEN** the page endpoint is requested once and not polled

#### Scenario: Owner mode

- **WHEN** a key owner signs in with the key
- **THEN** the key page renders without the administrator navigation, actions, account columns, cost by account or lifetime totals, and no `/api/` request outside `/api/key-reports/` is made

### Requirement: Owner key page data is bound to the authenticated key and redacted

The owner routes SHALL bind every section to the key of the report session, SHALL accept only `start_date`, `end_date` and `timezone` (page) and `cursor`, `limit` and `errors_only` (requests), each at most once, and SHALL reject any other parameter with 400 `invalid_report_filters`. Owner responses SHALL omit account ids, e-mails and plan types, client IPs, full user-agent strings, conversation ids, error messages, model-source ids, cost by account, lifetime totals, the shell status and assigned account ids (only their counts remain), and SHALL include request ids, error codes and user-agent groups. A grouped owner key SHALL keep the same-group mini-list of the key report with the same safe fields, derived from the key's current membership in the read snapshot; a key that is disabled, expired or deleted by the time of that snapshot SHALL be rejected as unauthenticated.

#### Scenario: Key A cannot read key B

- **WHEN** an owner signed in with key A passes key B's id, account ids or any other selector as a parameter
- **THEN** the request is rejected with 400 and no data of key B is returned

#### Scenario: Omitted fields

- **WHEN** an owner reads the key page and its requests
- **THEN** the JSON contains no `accountId`, `clientIp`, `conversationId`, `useragent`, `errorMessage`, `byAccount`, `lifetime`, `shell` or `assignedAccounts` fields

### Requirement: Owner key page shows the upstream remaining only when visible

The pool or group remaining section (5-hour and weekly remaining with next reset and projected exhaustion, and the purchased-credit summary) SHALL appear in owner scope only when upstream quotas are visible to the key: `HideUpstreamQuotaFromKeys` is off and the key's usage sections include `account_pool_usage`. For an ungrouped key it SHALL cover the whole ChatGPT account pool, for a grouped key the group's non-deleted ChatGPT accounts; only aggregate values and counts SHALL be exposed.

#### Scenario: Hidden upstream quotas

- **WHEN** `HideUpstreamQuotaFromKeys` is on, or the key's usage sections omit `account_pool_usage`
- **THEN** the owner page response has `pool: null` for grouped and ungrouped keys, while the administrator page still shows it

### Requirement: Owner key page routes keep the key-report session rules

`GET /api/key-reports/page` and `GET /api/key-reports/requests` SHALL authenticate only with the report-session cookie, SHALL revalidate the key's active, expiry, internal status and credential fingerprint on every read, SHALL apply the proxy IP allowlist, SHALL send `Cache-Control: no-store` and `Vary: Cookie`, SHALL be gzip-compressed when the client accepts it, SHALL send `Server-Timing: app;dur=<ms>`, SHALL accept no mutation method and MUST NOT consume key quota, change limit counters or reservations or create administrator sessions.

#### Scenario: Key rotated mid-session

- **WHEN** the key of an open owner page is regenerated, disabled or deleted
- **THEN** the next page or requests read answers 401 and the dashboard ends the report session

#### Scenario: Firewall

- **WHEN** a client outside the proxy IP allowlist reads the owner page
- **THEN** the request is denied with 403 `ip_forbidden`

### Requirement: Owner links keep the period through key sign-in

The dashboard entry SHALL keep the `from` and `to` query parameters through key sign-in and SHALL open the owner key page for that period; when they are present the key sign-in method SHALL be preselected. A link copied from the administrator key page or from Reports SHALL be the dashboard origin with only `from` and `to` and SHALL NOT contain or grant any credential: the owner still signs in with the key.

#### Scenario: Link from the administrator

- **WHEN** the operator copies the owner link of a key for 2026-10-01 to 2026-10-28 and the owner opens it and signs in with the key
- **THEN** the owner page shows 2026-10-01 to 2026-10-28

### Requirement: Key links open the key page

Key links from the global search, the overview and the Keys page SHALL open `/keys/<id>`; `/keys?selected=<id>` and `/apis?selected=<id>` SHALL redirect to `/keys/<id>`, `/apis` with other parameters SHALL redirect to `/keys` with them, and `/keys` without a selection SHALL show the Keys page.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows the key page `/keys/key_1`

#### Scenario: Old list bookmark

- **WHEN** an operator opens `/apis?group=grp_1`
- **THEN** the dashboard shows the Keys page `/keys?group=grp_1` filtered to that group

### Requirement: Endpoints replaced by the key page are deprecated for one release

`GET /api/api-keys/{id}/trends`, `GET /api/api-keys/{id}/usage-7d` and `GET /api/key-reports/reports` SHALL stay unchanged in the release that ships the key page, which the embedded dashboard no longer calls, and SHALL be removed no earlier than the following release; the release notes SHALL list the deprecation.

#### Scenario: Tab opened before the update

- **WHEN** a key report tab loaded before the update still requests `GET /api/key-reports/reports`
- **THEN** it answers as before

### Requirement: The keys list filters, sorts and pages keys on the server in one bounded read

`GET /api/keys` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts, each at most once, `q` (at most 256 characters, matched case-insensitively as a substring of the key name or prefix), `group` (a group id or `ungrouped`), `status` (`active`, `disabled` or `expired`), `near_limit` (`1`, `true`, `0` or `false`), `expiring_days` and `inactive_days` (whole days from 1 to 365), `sort` (`name` by default, `created`, `spend_today`, `spend_7d`, `spend_30d`, `limit_fill` or `last_used`), `order` (`asc` or `desc`; by default `asc` for `name` and `desc` for every other sort), `offset` (from 0, default 0), `limit` (1 to 100, default 50) and `timezone` (IANA, default UTC). Any other or repeated parameter and any invalid value SHALL return 400 `invalid_key_filters`; an unknown zone SHALL return 400 `invalid_timezone`. The response SHALL carry the shell status, the number of matching keys before paging as `total`, the applied `offset` and `limit`, and one row per key of the page with its id, name, prefix, group id and group name, status, expiry, last use, creation, the estimated spend in USD of the current local day, of the 7 local days ending today and of the 30 local days ending today, the fullest current limit with its percent and reset, and the type, window, model filter and maximum of each active limit. Only non-deleted, non-internal keys SHALL be listed, and the filters SHALL combine with AND. Rows SHALL be ordered by the sort key and then by id ascending; keys without a value for the sort key (no limit, never used, unknown period spend) SHALL follow the keys with a value in either order. The server SHALL read the keys with their active limits, the group names and the per-key spend of the 30 local days ending today in one analytics snapshot, the spend from the key slices of the usage rollups with folded legacy hours, the journal, the unfolded tail and raw partial hours, then filter, sort and page in memory, with a number of SQL statements independent of the number of keys; while the rollups are unusable it SHALL read the raw events of the current local day only and return the 7- and 30-day spend as null.

#### Scenario: Statement count does not grow with the keys

- **WHEN** the keys list is read for 30 and for 300 keys
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Ties keep a stable order across pages

- **WHEN** several keys have the same spend today and the list is sorted by spend today and read page by page
- **THEN** the tied keys are ordered by id and every key appears on exactly one page

#### Scenario: Paging total

- **WHEN** 120 keys match and the second page of 50 is requested
- **THEN** the response has `total` 120, `offset` 50 and 50 rows

#### Scenario: Rollups unusable

- **WHEN** the rollups are not usable after an upgrade
- **THEN** the spend today of every key equals the raw evaluation of today's events and its 7- and 30-day spend are null

### Requirement: Key list filters have exact boundaries

For the keys list and its matching ids, status `disabled` SHALL mean the key is disabled, `expired` an enabled key with `expires_at ≤ now` and `active` an enabled key that has not expired; "near limit" SHALL mean an active key whose fullest active limit, with an expired window counted as zero, is at or above 90 %; "expiring within N days" SHALL mean `now < expires_at ≤ now + N × 24 h`; "inactive for N days" SHALL mean `last_used_at` absent or earlier than `now − N × 24 h`. Deleted keys and the internal keys SHALL never be listed or counted.

#### Scenario: Inactive boundary

- **WHEN** one key was last used exactly 7 days ago, one 7 days and one millisecond ago and one never
- **THEN** `inactive_days=7` lists the last two

#### Scenario: Expiry boundary

- **WHEN** one key expires exactly 7 days from now, one a millisecond later and one expired a moment ago
- **THEN** `expiring_days=7` lists only the first and `status=expired` lists only the last

#### Scenario: Deleted and internal keys

- **WHEN** a key is deleted
- **THEN** neither it nor the internal keys appear in any filter or in `total`

### Requirement: Select all matching returns the ids of the matching keys

`GET /api/keys/ids` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts the filter, `sort`, `order` and `timezone` parameters of `GET /api/keys`, not `offset` and `limit`, with the same validation. It SHALL return `{ids, total}`: the ids of the matching keys in list order, at most the first 1 000, and the number of matching keys.

#### Scenario: More than a thousand matches

- **WHEN** 1 200 keys match
- **THEN** the response lists the first 1 000 ids in list order and `total` is 1 200

### Requirement: Bulk key actions apply in short atomic jobs with per-key results

`POST /api/keys/bulk` SHALL be an administrator endpoint that accepts `{ids, action, groupId, limits}`: 1 to 1 000 distinct non-empty ids of at most 256 characters, `action` one of `disable`, `enable`, `move_group` and `set_limits`, `groupId` (required for `move_group` and only there: a group id, or null for no group) and `limits` (required for `set_limits` and only there: the key limit rules, where an empty list removes every limit). A malformed JSON body SHALL return 400 `invalid_request`, any other invalid body 400 `invalid_bulk_request` and invalid limit rules 422 `invalid_limit`. A valid request SHALL return 200 `{results: [{id, ok, code}]}` with one result per id in request order, where `code` is present only for a failed key. Unknown, deleted and internal ids SHALL fail with `not_found`. `disable` and `enable` SHALL run in write jobs of at most 25 keys, and `move_group` and `set_limits` in one write job per key, because saving a key may backfill its new limit windows; every job SHALL be atomic, a failed job SHALL fail only its own keys and SHALL NOT undo earlier jobs, and later jobs SHALL still run unless the request is cancelled, after which the remaining keys fail with `not_applied`. `move_group` SHALL set the key's group and apply the target group's limits, and enable the account-assignment scope for a group, exactly as saving the key with that group does; leaving a group SHALL keep the key's last effective limits, and an unknown target group SHALL fail with `invalid_group`. `set_limits` SHALL replace the limits of an ungrouped key exactly as saving the key with those limits does and SHALL fail with `key_in_group` for a grouped key. A job SHALL change only the active state, the group with its implied account-assignment scope or the limits of a key, and SHALL NOT overwrite its other fields or its last use.

#### Scenario: Failure in the middle of a move

- **WHEN** five keys are moved to a group and the third key's job fails
- **THEN** the first two keys stay moved with the group's limits, the third reports its failure code and the last two are moved

#### Scenario: Limits for a grouped key

- **WHEN** `set_limits` is applied to one ungrouped and one grouped key
- **THEN** the ungrouped key has the new limits and the grouped key fails with `key_in_group` and keeps its group limits

#### Scenario: Unknown ids

- **WHEN** the ids include an unknown id, a deleted key and an internal key
- **THEN** each of them fails with `not_found` in its request position and the other keys are changed

### Requirement: The Keys page manages hundreds of keys

`/keys` SHALL show the Keys page: a toolbar with the search, the group filter (any, a group of the directory or ungrouped), the status filter, near limit, expiring within 7 days and inactive for 7, 30 or 90 days, Create key and the number of matching keys; a table of at most 50 rows per page with a sticky header, a checkbox per row and one for the page, and the columns name with prefix, group, status, spend today, 7 days and 30 days, fullest limit with reset, limits, expiry, last use and creation, sortable by name, the three spend columns, fullest limit, last use and creation; previous and next page with "page n of m". A row click SHALL open `/keys/<id>` and a row menu SHALL offer Edit, Disable or Enable, Regenerate and Delete. Search, filters, sort and page SHALL live in the URL under the parameter names of `GET /api/keys`, so that `/keys?near_limit=1`, `/keys?expiring_days=7` and `/keys?group=<id|ungrouped>` open those filters. The page SHALL request `GET /api/keys` for its current page only, every 60 seconds while visible, SHALL take the shell status from that response and SHALL NOT request `GET /api/api-keys/`. The selection SHALL survive polls, sorting and page changes until it is cleared, and "Select all n matching" SHALL select the ids returned by `GET /api/keys/ids`. With keys selected, a bulk bar SHALL offer Disable, Enable, Move to group (including no group), Set limits for ungrouped keys and Clear; every bulk action SHALL ask for a confirmation that names the action and the number of keys, SHALL list the keys that failed with their reason, and SHALL refresh the keys list, the directory and the overview afterwards. While the first page loads the table SHALL show a skeleton with a usable toolbar; a failed refetch SHALL keep the previous page with a retryable error; an empty result SHALL offer to reset the filters, or to create the first key when no key exists.

#### Scenario: Overview link

- **WHEN** the operator follows the overview's link to keys near their limit
- **THEN** `/keys?near_limit=1` opens with the near-limit filter set and requests `GET /api/keys` with `near_limit=1`

#### Scenario: Selection across a poll and a page change

- **WHEN** the operator selects two keys, the page is polled and the operator opens the next page and selects one more
- **THEN** the bulk bar counts three selected keys

#### Scenario: Bulk confirmation

- **WHEN** the operator chooses Disable with 12 keys selected
- **THEN** a confirmation naming the action and 12 keys is shown, and only after confirming one `POST /api/keys/bulk` is sent

### Requirement: The key list endpoint is deprecated for one release

`GET /api/api-keys/` SHALL stay unchanged in the release that ships the Keys page, whose embedded dashboard pages no longer call it, and SHALL be removed no earlier than the following release; the release notes SHALL list the deprecation.

#### Scenario: Tab opened before the update

- **WHEN** a dashboard tab loaded before the update still requests `GET /api/api-keys/`
- **THEN** it answers as before

### Requirement: The groups summary reads every group's membership, limits, spend and projections in one bounded read

`GET /api/groups/summary?timezone=<IANA>` (default UTC; an unknown zone SHALL return 400 `invalid_timezone`) SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and returns the shell status, a pool row, one row per account group plus the ungrouped row, and the non-deleted accounts. The group rows and the ungrouped row SHALL be computed by the same functions as the overview's group rows and SHALL equal them for the same data and instant, in the same order: account health counts, capacity-weighted 5 h and weekly windows with next reset and projected exhaustion, limit fill, key counts, spend today, 7-day spend and trend, problem accounts, top keys by spend today and attention. Each row SHALL add its 30-day spend, the estimated cost of the 30 local days ending today summed over the row's current non-deleted, non-internal member keys; its limit templates (type, window, model filter and maximum, empty for the ungrouped row); its member account ids among the non-deleted accounts (empty for the ungrouped row); and at most 20 member keys ranked by 7-day spend, then spend today, then name, with status, spend today, 7 d and 30 d, the fullest limit percent and last use. The pool row SHALL cover every ChatGPT account and every non-deleted, non-internal key, SHALL take spend today, 7 d, 30 d and the 7-day trend from all reported traffic as the overview's pool summary does, SHALL take its attention from the alerts that concern the whole pool and its list of member keys by 7-day spend SHALL be empty. Each account, ChatGPT or external, SHALL carry its id, label (alias, else e-mail), e-mail, kind, status, health and 5 h and weekly remaining percent. The summary SHALL read one entity snapshot that includes the group limit templates in one statement and one analytics snapshot, with a number of SQL statements independent of the number of groups, keys and accounts; while the rollups are usable it SHALL NOT read raw usage events beyond the journal, the unfolded tail and partial hours, and while they are unusable every 7-day and 30-day spend SHALL be null.

#### Scenario: Rows match the overview

- **WHEN** the groups summary and the overview are computed for the same data, zone and instant
- **THEN** every group row and the ungrouped row of the summary equal the overview's rows in the same order

#### Scenario: Thirty-day spend

- **WHEN** a member key of a group spent 2 USD twenty local days ago and 1 USD today
- **THEN** the group's 30-day spend includes 3 USD from that key and its 7-day spend only 1 USD

#### Scenario: Statement count at scale

- **WHEN** the summary is computed for 5 groups, 30 keys and 10 accounts and for 50 groups, 300 keys and 100 accounts
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Latency at target scale

- **WHEN** `GET /api/groups/summary` is measured warm on the synthetic store with 50 groups, 300 keys, 100 accounts and 1.5 million events over 30 days
- **THEN** its server time is under 300 ms

### Requirement: The Groups page manages groups from one table and a detail sheet

The Groups page at `/groups` SHALL show a toolbar with a name search and Create group, the pool row, a pool runway card built from `GET /api/dashboard/projections` (weekly credit headroom, runway, next relief and, when the pool runs dry before relief, the throttle-to and add-Pro recommendations), and one table row per group plus the ungrouped row with the columns name and attention, accounts (count and health bar), limit templates, keys active/total, limit fill (highest percent and keys at or above 90 %), spend today, 7 d and 30 d, 5 h and weekly remaining with next reset, and projected exhaustion before reset. Every column SHALL be sortable and the default order SHALL be the summary's order. Opening a group, by its row or through `/groups?selected=<id>`, SHALL show a detail sheet with the group's name, its members as an account multi-select that shows each account's status and remaining, its limit templates in the limit-rules editor, its top keys by 7-day spend (at most 20) with a link to `/keys?group=<id>`, Reset period behind the existing confirmation and Delete behind a confirmation. Save SHALL send one `PUT /api/account-groups/{id}` carrying the name, the members and the limits, so that membership and limit templates change atomically and member keys are re-synced as before; Delete SHALL be refused while keys reference the group. Create group SHALL use `POST /api/account-groups/`. The page SHALL poll only `GET /api/groups/summary`, every 60 seconds while the tab is visible, SHALL request the projections once per visit and SHALL keep an open sheet's unsaved draft across polls. Loading SHALL show a table skeleton, a failed refetch SHALL keep the previous data with a retry action, and without groups the page SHALL state that ungrouped keys use the whole pool and offer Create group.

#### Scenario: Edit members and limits

- **WHEN** the operator adds two accounts and a weekly cost limit in a group's sheet and saves
- **THEN** one `PUT /api/account-groups/{id}` carries the new members and limits and the summary is requested again

#### Scenario: Draft survives a poll

- **WHEN** the summary poll returns while the sheet has unsaved member changes
- **THEN** the sheet still shows the unsaved selection

#### Scenario: Group with keys

- **WHEN** the operator confirms Delete on a group that keys still reference
- **THEN** the group is kept and the refusal is shown

#### Scenario: One periodic request

- **WHEN** the Groups page stays open and visible
- **THEN** the only periodic request is `GET /api/groups/summary` every 60 seconds and `GET /api/dashboard/projections` is requested once

### Requirement: Settings keeps only installation-wide settings

Once the Groups and Keys pages exist, the Settings page SHALL NOT contain the API key section (key table, create, edit, delete, regenerate) or the account-group section, SHALL keep the API key authentication toggle and the upstream-quota privacy toggle in an API-key access section, and SHALL NOT offer the dashboard refresh-rate and account burn-projection appearance settings, which no page reads.

#### Scenario: Open Settings

- **WHEN** an operator opens the Settings page
- **THEN** no API key table or account-group list is shown
- **AND** the API key authentication and upstream-quota privacy toggles are shown and save `apiKeyAuthEnabled` and `hideUpstreamQuotaFromApiKeys` through `PUT /api/settings`

### Requirement: The account list is read in a fixed number of statements

`GET /api/accounts` SHALL be built from one batch read of the non-deleted accounts with their current quotas, purchased-credit status, additional quotas, group memberships, lifetime usage totals, quota refusal times and token ciphertexts, the last 7 days of usage per account, the settings and the shell status, with a number of SQL statements independent of the number of accounts. It SHALL keep every field it returned before with the same name, type and value, and SHALL add per account `groups` (the ids of the account's groups in id order), `usage7d` (`requests`, `totalTokens` as input plus output tokens, and `costUsd` over the 7 × 24 hours ending at the read, counting the requests the reports count) and `resetCreditsObservedAt` (when the in-memory reset-credit snapshot of the account was taken, or null), and at the top level `shell` (the shell status). Usage of the last 7 days SHALL be read from the account usage rollups (UTC-day rows for whole days, hour rows at the edges) combined with raw partial hours, the unfolded tail, the journal and folded legacy hours, and while the rollups are unusable from raw events of the window, with identical results. The response SHALL decrypt only the access, refresh and ID token ciphertexts whose state and expiry it returns, SHALL NOT read or decrypt external API keys, SHALL send `Server-Timing` and MUST NOT call a provider.

#### Scenario: Statement count does not grow with the installation

- **WHEN** `GET /api/accounts` is served for 9 and for 100 accounts
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Existing fields are unchanged

- **WHEN** an account has quotas in every window, an expired primary window, purchased credits, additional quotas, a quota refusal, tokens with and without expiry, an unreadable token, no stored credential, an alias or none, and a reset-credit snapshot
- **THEN** every field the per-account reads produced is present with the same value, and `groups`, `usage7d`, `resetCreditsObservedAt` and `shell` are added

#### Scenario: Rollups and raw events agree

- **WHEN** the last 7 days are read while the rollups are folded, the folder stopped at a random cursor, or journal rows are pending
- **THEN** every account's `usage7d` equals the evaluation of the raw events and folded legacy hours of the same window

#### Scenario: No provider request

- **WHEN** `GET /api/accounts` is served
- **THEN** no request reaches the ChatGPT usage, reset-credit or token endpoints

### Requirement: The Accounts page manages accounts from one table

The Accounts page SHALL show every account in one table with a toolbar: search by alias, e-mail or account id (case-insensitive), a status filter, a group filter (every group and Ungrouped), a sort select (status severity by default, next reset soonest or latest, 5 h remaining, weekly remaining, most reset credits, name ascending or descending) and Add account, which opens the OAuth or import chooser. Each row SHALL show the label (alias, else e-mail) with the e-mail, the plan with the subscription countdowns, the status with its reason, the group names, the 5 h, weekly and (when any account has one) monthly remaining with their reset times, purchased credits, the reset credits of the in-memory snapshot, the warm-up state, an inline priority select (burn first, normal, preserve), inline Re-login for accounts needing re-login or deactivated and Pause or Resume for the others, and a menu with Probe, Export auth, Redeem reset credit (when the snapshot has credits) and Delete. Status severity SHALL order re-login required, deactivated, quota exceeded, rate limited, paused, then active. Accounts needing operator recovery (re-login required or deactivated) SHALL NOT offer the priority select, Pause or Resume, and Probe SHALL be disabled for them and for paused accounts. Opening a row SHALL show the account's detail sheet; `/accounts?selected=<id>` SHALL open it directly. The page SHALL poll only `GET /api/accounts`, every 30 seconds while visible, and SHALL take the shell status from that response; while loading it SHALL show a table skeleton, on an error a retryable inline error that keeps the previous rows, and without accounts "No accounts" with Add account.

#### Scenario: Problems sort first

- **WHEN** the Accounts page opens with an active, a paused, a rate-limited and a re-login-required account
- **THEN** the re-login-required account is the first row and the active account the last

#### Scenario: Filter by group

- **WHEN** the operator selects a group in the group filter
- **THEN** only the accounts in that group are listed, and Ungrouped lists the accounts without a group

#### Scenario: One poll

- **WHEN** the operator keeps the Accounts page open
- **THEN** the only periodic request is `GET /api/accounts`, whose `shell` updates the header badge and the status bar

### Requirement: Browsing accounts never calls a provider

Opening the Accounts page or an account's detail sheet SHALL NOT issue a request that calls a provider. The detail sheet SHALL show the reset credits of the in-memory snapshot as "available N as of HH:MM" with the snapshot time and SHALL request `GET /api/accounts/{id}/usage-reset-credits` only when the operator presses Check now; provider refreshes SHALL otherwise happen only on Probe and Re-login. The 7-day quota chart (`GET /api/accounts/{id}/trends`) SHALL be requested only while the sheet is open and refetched every 5 minutes while it stays open.

#### Scenario: Selection does not refresh from the provider

- **WHEN** the operator opens the Accounts page and then two accounts' detail sheets
- **THEN** no `usage-reset-credits` request is made

#### Scenario: Check now

- **WHEN** the operator presses Check now in a detail sheet
- **THEN** exactly one `GET /api/accounts/{id}/usage-reset-credits` is requested and its count is shown with the time of the check

### Requirement: Account usage figures carry their period

Every usage figure on the Accounts page SHALL carry an explicit period label: request, token and cost figures SHALL be labelled "Last 7 days" (from `usage7d`) or "Lifetime" (from `requestUsage`), and quota figures SHALL name their window with its reset time.

#### Scenario: Usage labels

- **WHEN** the detail sheet of an account with usage opens
- **THEN** it shows the requests, tokens and cost of "Last 7 days" and of "Lifetime", and no unlabelled usage total

### Requirement: Account row actions update the row in place

Priority, Pause or Resume and Re-login SHALL be available inline in the accounts table. After a successful priority, pause, resume, alias, warm-up or trusted-access mutation the row SHALL show the new value without reloading the page, and the list SHALL then be refetched; Re-login SHALL open the OAuth flow targeted at the account and refetch the list when it completes.

#### Scenario: Inline priority

- **WHEN** the operator selects Preserve in a row's priority select
- **THEN** `PUT /api/accounts/{id}/routing-policy` is sent and the row shows Preserve without a page reload

#### Scenario: Inline pause

- **WHEN** the operator presses Pause in an active account's row
- **THEN** `POST /api/accounts/{id}/pause` is sent and the row shows the paused status with Resume

### Requirement: Image generation routes use the subscription images endpoint

The runtime SHALL accept `POST /backend-api/codex/images/generations`, `POST /backend-api/codex/images/edits`, `POST /v1/images/generations` and `POST /v1/images/edits` from an authenticated API key, with or without one trailing slash. It SHALL forward the validated JSON request body to `/images/generations` or `/images/edits` under the configured ChatGPT Codex base URL of one ChatGPT subscription account, and SHALL return the upstream status and JSON body without rewriting them. `/v1/images/edits` SHALL also accept OpenAI multipart, with `image` and `image[]` file parts, at most one `mask` file part and text fields. It SHALL convert that form to the JSON shape, with each file as a base64 `data:` URL in `images[].image_url` or `mask.image_url`, and numeric and boolean fields as JSON numbers and booleans.

Before any reservation or dispatch, a request SHALL be rejected with 400 `invalid_request_error` naming the parameter in each of these cases:
- it is not a JSON object;
- its `prompt` is missing, empty or longer than 32000 characters;
- its `model` is neither absent nor a string naming a priced `gpt-image-` model;
- `n` is not an integer from 1 to 10;
- `stream` is `true`;
- an edit has no `images`, more than 16 `images`, or an entry without a non-empty `image_url` or `file_id` string.

An absent `model` SHALL become `gpt-image-2`. An enforced key model SHALL replace the image model only when the enforced model is itself an image model. A key with allowed models SHALL receive 403 `model_not_allowed` for an image model outside them. Bodies SHALL be charged to the shared request-body budget with the 32 MiB route maximum. The `model`, `quality`, `size`, `background` and other generation fields SHALL be forwarded as sent, because the subscription endpoint decides which of them it honors.

Accounts SHALL be selected by the shared balancer among the key's eligible ChatGPT accounts without model-catalog filtering. An edit that references any `file_id` SHALL be sent only to that file's recorded owner account. Unknown or conflicting owners SHALL return 409, and the edit SHALL never be retried on another account.

The API-key reservation SHALL be taken before dispatch. It SHALL be settled exactly once from the response `usage` at the image model's token rates. A successful response without at least one `data` entry carrying `b64_json` SHALL return 502. A successful response without usage SHALL return 502 and keep its reservation for reconciliation. Image requests SHALL NOT change account health or quota state, whether they succeed or are refused.

An upstream HTTP 429 usage-limit refusal that arrives before any output or usage SHALL be settled as a failed, uncharged attempt. That account SHALL then be excluded from image routing for 10 minutes, and the same request SHALL be sent to another eligible account, trying at most four accounts per request. Any other failure, and any response that carried output or usage, SHALL NOT be retried on another account.

Usage events SHALL record request kind `image_generation` or `image_edit` with the effective public image model. Error diagnostics of image requests SHALL NOT store `images` or `mask` payloads.

#### Scenario: Codex image generation through codex-lb
- **WHEN** Codex posts `{"prompt":"…","model":"gpt-image-2","background":"opaque","quality":"auto","size":"auto"}` to `/backend-api/codex/images/generations` with a valid key
- **THEN** one eligible ChatGPT account receives the same body at `/backend-api/codex/images/generations` under its access token and account header
- **AND** the client receives the upstream 200 body with `data[0].b64_json` unchanged
- **AND** one `image_generation` usage event with model `gpt-image-2` and the response token usage is settled once, and the account's health is unchanged

#### Scenario: Multipart image edit on the OpenAI-compatible route
- **WHEN** a client posts multipart `image`, `mask`, `prompt` and `n=1` to `/v1/images/edits`
- **THEN** the upstream receives JSON `images[0].image_url` and `mask.image_url` as base64 data URLs with the same content types, `prompt` as a string and `n` as a number

#### Scenario: Invalid image requests never dispatch
- **WHEN** a request lacks `prompt`, names a non-image `model`, asks for `stream: true`, or sends an edit without images
- **THEN** the client receives 400 `invalid_request_error` with the parameter, and no reservation, account selection or upstream call happens

#### Scenario: Key model policy applies to image models
- **WHEN** a key's allowed models do not include `gpt-image-2`
- **THEN** an image request for `gpt-image-2` receives 403 `model_not_allowed` before dispatch
- **AND** a key whose enforced model is a text model keeps the requested image model

#### Scenario: An exhausted image limit moves the request to another account
- **WHEN** the first selected account answers 429 `usage_limit_reached` before producing an image and a second eligible account exists
- **THEN** the same body is sent to the second account and its image is returned
- **AND** the first attempt is settled uncharged, both accounts keep their health and quota state, and the first account receives no image request for 10 minutes

#### Scenario: A file-pinned edit stays on its owner
- **WHEN** an edit references a `file_id` created through codex-lb on one account and that account refuses with 429
- **THEN** the refusal is returned to the client and no other account receives the edit
