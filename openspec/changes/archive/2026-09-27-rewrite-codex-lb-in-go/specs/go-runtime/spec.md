## ADDED Requirements

### Requirement: Preserve the legacy implementation during in-repository rewrite

The rewrite SHALL keep the existing Git repository and preserve the old implementation under `legacy/`, including current uncommitted changes. Relocation MUST NOT overwrite an existing destination, discard source files, expose ignored secrets/build data to version control, or change the running installation. Required licenses and attribution MUST be preserved. The new server SHALL run as a Go binary without invoking Python, Rust or an external codex-relay process.

#### Scenario: Modified files are relocated
- **WHEN** the legacy source tree is moved
- **THEN** modified and untracked implementation files retain their contents
- **AND** `.git` and active planning instructions remain available
- **AND** credentials, databases and build caches remain excluded from commits and binary assets

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
- **AND** purchased credits remain distinct from calculated subscription credits and do not bypass exhausted primary windows
- **AND** a model's canonical additional quota uses its own fresh windows and registry restrictions for new admissions, without falling back to standard windows when required evidence is absent
- **AND** the established-owner continuation exception remains unchanged
- **AND** absent additional-quota metadata preserves prior samples, an explicit empty list clears them, and older samples cannot replace newer values

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

The server SHALL support configured ChatGPT and external API accounts, including Z.AI-compatible Chat Completions and OpenAI-compatible Responses sources. Its integrated adapter SHALL preserve supported tool calls/results, reasoning, streaming, usage, model mappings and continuation state. Unsupported capabilities MUST be rejected explicitly before dispatch rather than dropped or invented. Provider credentials, account scopes and custom pricing MUST remain isolated. Cross-provider switching MUST NOT occur implicitly solely because another provider has available quota.

#### Scenario: Codex tool round trip through a Chat Completions source
- **WHEN** Codex receives a translated tool call and sends its matching output on the next turn
- **THEN** the adapter reconstructs valid provider history and returns correctly correlated Responses events
- **AND** the exchange does not require an external relay process

#### Scenario: Direct Chat Completions obey declared model capabilities
- **WHEN** a direct source-routed Chat request requires streaming, tools, images or active reasoning that its configured model does not support
- **THEN** the server rejects it with HTTP 400 before upstream dispatch and releases its unused budget without an uncertain usage reservation
- **AND** explicit reasoning effort must be supported by model metadata, while empty tools and disabled controls alone do not require capabilities

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
