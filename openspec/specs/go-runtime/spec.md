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

The Go implementation SHALL provide the selected account/OAuth, many-to-many group, API-key, per-key limit, model, quota, price, report, admin-security, firewall, Codex Responses/file/image/voice, warmup/scheduled-ping and reset-credit functionality before production cutover. It SHALL NOT provide upstream install telemetry, multi-replica runtime coordination, the quota phase planner, guest dashboard access, PostgreSQL runtime, proxy pools, or a separate public Images API. Binary/systemd and optional ordinary Docker delivery SHALL replace Helm/Kubernetes, Nix and distroless-specific delivery. Local diagnostics and usage statistics MUST remain available independently of removed telemetry.

#### Scenario: Images used by Codex are retained
- **WHEN** Codex submits supported image input or uses its built-in image generation workflow
- **THEN** the proxy preserves that capability on a capable provider
- **AND** this does not require exposing `/v1/images/generations` or `/v1/images/edits`

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
- **THEN** its session response identifies whether this request requires a bootstrap token using the same trusted-identity policy as password setup
- **AND** direct local setup does not display a remote-blocked warning or request an unnecessary token
- **AND** the dashboard remains gated until setup completes, and remote or forwarded requests cannot bypass bootstrap verification by spoofing local headers

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

### Requirement: Soft affinity is optional, scoped and operator-manageable

New unowned requests SHALL apply key-scoped typed locality hints without weakening account/model/group/security scope, atomic capacity selection or hard ownership. The existing dashboard settings SHALL control automatic sticky locality, prompt-cache TTL and split primary/secondary pressure thresholds, survive restart/import and affect selection. The legacy single threshold SHALL remain a primary-threshold alias. A healthy eligible soft preference MAY move for pressure or local capacity; this MUST NOT authorize replay or transfer of an already-owned thread. Dashboard affinity list, filtering, paging, sorting, single/batch/filtered deletion and stale purge SHALL use the existing admin authentication/CSRF and UI contracts. Deleting cache/locality mappings MUST NOT delete response, file, voice or capability-lineage ownership. Unscoped legacy hints MUST NOT be promoted to authenticated key-scoped ownership; the original private snapshot SHALL retain them.

#### Scenario: Soft preference yields to available capacity without changing a hard owner
- **WHEN** a new request's permitted soft-preferred account is locally full and a permitted alternate has room
- **THEN** account selection and lease acquisition SHALL atomically select the alternate
- **AND** a request with a confirmed previous/session/turn/file owner SHALL remain constrained to that owner instead

#### Scenario: Operator clears stale cache hints
- **WHEN** the administrator purges stale prompt-cache affinities
- **THEN** only expired prompt-cache mappings SHALL be deleted
- **AND** durable locality kinds and active hard-owner aliases SHALL remain unchanged

#### Scenario: Locality persistence fails after reservation
- **WHEN** saving a soft mapping fails before provider dispatch
- **THEN** the unused reservation SHALL be settled without inventing usage or calling the provider
- **AND** a lost compare-and-set race SHALL NOT retry the provider request or overwrite the winning mapping

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

Dashboard overview/projections SHALL compute the selected read-only weekly-credit pace from fresh persisted quota observations, including saved UTC working weekdays and smoothing interval. Missing samples SHALL remain distinguishable from zero burn, reset/window changes SHALL not be smoothed together, and stale/ineligible observations SHALL not inflate usable capacity. The report SHALL preserve its existing nullable forecast, confidence, reset-event and key-attribution contracts without controlling routing or initiating provider work. Settings SHALL survive restart and import and affect the actual report, not merely be accepted by the settings API.

#### Scenario: Saved weekday and smoothing settings reach the report
- **WHEN** the administrator saves working weekdays or a supported smoothing interval and reloads the runtime
- **THEN** overview and projections SHALL apply those saved values to the same persisted observations without fetching provider data

#### Scenario: There is no fresh usable weekly observation
- **WHEN** all candidate observations are stale, invalid or operator-ineligible
- **THEN** weeklyCreditPace SHALL be null rather than showing fictional available credits or zero consumption

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

Saving a model price SHALL recalculate historical costs for that model from retained token usage and request-tier/context data, including requests previously recorded at zero cost. Reports, account/key aggregates and affected current cost/credit limit consumption SHALL remain consistent without double-counting or changing token counts. Repricing MUST NOT issue provider requests. Missing historical detail MUST be reported explicitly rather than assigned invented costs. An in-flight settlement MUST NOT reintroduce an obsolete price after a completed price update. Removing an override SHALL restore an existing bundled rate; removing a custom-only model price SHALL leave that model explicitly unpriced rather than free.

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
- **AND** a failed reconciliation rolls back both the tariff and financial changes

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
The target deployment is 1vCPU/1GB with a preferred128MiB service memory budget
and predominantly long-lived streams; higher memory use is authorized when needed
and SHALL be measured and reported. Verification SHALL include single-Go-processor
runs, peakRSS and larger request context rather than only tiny short requests.
At least256 active streams SHALL be tested without counting queued requests as
active. Additional concurrency MUST NOT weaken accounting/authentication.
Configuration/data files MAY be reorganized only
where measurement supports the change and recovery/data-safety contracts remain.

The default global active-stream bound SHALL support 256 concurrent requests,
retain a bounded 128-request queue and its 15-second timeout, and preserve all
per-account recovery/create/stream and configured source caps. Raising the
global bound MUST NOT authorize scope bypass or weaken cancellation cleanup.
The production HTTP transport MUST allow at least 256 concurrent connections to
one upstream host, so its HTTP/1.1 connection queue does not reduce this bound.
The upstream WebSocket session store SHALL support 256 independent live sessions
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
