# Go runtime: implementation and operational context

- Last edited with skill pack: `0.2.2`

The requirements are in [go-runtime/spec.md](specs/go-runtime/spec.md).
The selected implementation and offline verification are complete. This is not
approval for a production cutover or a claim about an unseen production snapshot.

## Build and local configuration

`make web-deps build` builds React assets and embeds them into `bin/codex-lb`.
The runtime does not invoke Node, Python, Rust, or the external relay. The current
command supports Unix systems; Linux is the systemd/container delivery target.
Timezone data is embedded. TLS trust still uses the operating system's CA store.

`make test-web-contract` runs the existing frontend schemas against a fresh
temporary Go installation: password session, empty pages, group/key CRUD,
external source CRUD, translated Responses against a loopback stub, actual
key accounting, and error-only archives. It creates no subscription account and
does not contact a real provider. The temporary process/data are removed when
the test finishes; no installed server is started or stopped.

The default listener is `127.0.0.1:2455`. `--data-dir` (or `CODEX_LB_DATA_DIR`)
selects an absolute private directory; without an override it is
`os.UserConfigDir()/codex-lb`, independent of the shell's working directory.
No `env.local` is required. Remote first-password setup may use
`CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`; credentials are not command-line flags.
Repeat `--trusted-proxy CIDR` only for known reverse proxies.

`--dashboard-auth-mode trusted_header` accepts exactly one nonempty identity
header from a trusted socket peer, with password sessions as a fallback.
`--dashboard-auth-header` selects that header; reserved transport/identity
headers are rejected. The default mode is `standard`; `disabled` is an explicit
installation choice, never a consequence of missing settings. CSRF protection
remains enabled in every mode.

The proxy firewall allowlist persists across restarts/imports and applies to
all proxy-facing routes, including files and transcription. Its single-process
snapshot changes only after a successful database mutation; no per-IP cache or
multi-replica invalidation machinery is needed. A trusted firewall proxy must
supply a complete valid X-Forwarded-For or Forwarded chain; singleton vendor
headers cannot authorize an IP. Dashboard routes remain behind admin auth, not
the proxy allowlist, so the operator can repair a mistaken allowlist.

The data directory contains `codex-lb.sqlite3` and `encryption.key`, with private
directory/file permissions. Startup locks the actual database inode and verifies
schema, stored credentials, and a durable key fingerprint/probe before opening
the HTTP listener. A missing or changed key is an error, not permission to create
a replacement for existing data. A second process is refused by the kernel lock.

## Model prices and historical costs

Settings includes **Model prices** for Codex tariffs. The displayed unit is USD
per million tokens; persistence and accounting use integer microdollars. Saving
a new price or an override updates the model's retained request history, not
only future admissions. For example, a new model's requests previously recorded
at zero cost become priced from their saved input/output/cached tokens and tier,
without contacting the provider or restarting the server.

The price update and cost changes commit together. Reports, account/key totals
and attributable current cost-limit charges change by the same relevant deltas;
token counts do not change. Repeating the same save does not charge twice, and
late in-flight settlement resolves the committed price. A manual key-limit
reset remains a reset even when older request history is repriced.

The result reports recalculated requests and the net cost adjustment. Fully
covered imported hourly buckets can be updated from their retained raw rows;
incomplete buckets or older undimensioned totals are reported as partial rather
than assigned invented per-request costs. If an active monetary limit lacks
enough charge provenance for safe reconciliation, the operation returns
`price_reconciliation_required` without committing the new tariff or partial
financial writes. A price-only edit cannot resolve an ambiguous old ledger.

Restoring a bundled tariff also recalculates eligible history. Removing a
custom-only tariff preserves existing historical amounts; the model becomes
unpriced, not free. External provider-source tariffs remain source-scoped and
are not overwritten by these global Codex edits.

The frontend contract test exercises price CRUD, restart persistence and source
isolation against a temporary real binary. Financial regressions also exercise
zero-cost history, aliases, folded imports, pending settlement, repeated saves,
group-rule updates and manual resets. Desktop and narrow-screen editing were
checked in a temporary browser/server without production data or provider calls.

## Offline import

The importer accepts a checkpointed COPY of legacy SQLite and its matching
private Fernet key. It rejects an existing destination directory and retains
the full original snapshot as a private sidecar in the destination.

Example with placeholder paths (not a deployment action):

```sh
bin/codex-lb import-legacy --source /backup/legacy-copy.sqlite --source-key /backup/encryption.key --data-dir /srv/new-codex-lb --dry-run
```

`--dry-run` performs the import in a temporary private directory, then removes
that temporary copy. It does not modify the requested destination. Omitting
`--dry-run` creates the new installation; an unsuccessful real import leaves
the destination for inspection and does not alter its source. Import output
contains counts, not tokens or request content. Unsettled reservations and
required removed proxy bindings are preserved for explicit reconciliation.

### Interrupted accounting

After acquiring the exclusive installation lock, startup marks interrupted
reservations for reconciliation without releasing held key budget. It does not
infer zero usage from a timeout or process crash. The offline `reconcile-usage`
command uses the same lock, so it cannot alter a running server's reservations.

```sh
bin/codex-lb reconcile-usage --data-dir /srv/new-codex-lb
bin/codex-lb reconcile-usage --data-dir /srv/new-codex-lb --reservation req_example --settlement /backup/confirmed-usage.json
```

A settlement is a small JSON object such as
`{"status":"success","usage":{"inputTokens":100,"outputTokens":20,"costMicrodollars":500}}`.
Use provider-confirmed amounts, not the reservation estimate. Status may be
`success` or `error`; an error may still consume usage. Optional `accountId`
may identify a legacy reservation's missing owner but cannot change a known
owner. An explicit `--release` instead of `--settlement` is for a reservation
the operator has confirmed should not consume usage. Repeating a completed
reconciliation cannot charge it twice. Unknown usage remains reserved; neither
startup nor time-based cleanup automatically forgives it.

## Shutdown and delivery

Request-log retention is disabled by default. The dashboard's nullable override
is persisted and imported; nonzero values must be 30–3650 days. Hourly maintenance
folds and prunes at most 1000 raw records per pass while preserving totals and
hourly aggregates. The separate encrypted error-content archive retains bounded,
redacted diagnostics for seven days (64 MiB/10,000 records maximum). Its admin
API exposes virtual date groups, not filesystem paths. Successful payloads are
not archived. Arbitrary secrets in user prose cannot be reliably auto-detected,
so error contents remain administrator-only data.

External source timeout and concurrency settings apply across active operations.
An absent source timeout uses the legacy ten-minute default. A full configured
source limit rejects locally without dispatch or quota-failure classification;
cancellation and timeout release the slot. All credential-bearing HTTP clients
must reject redirects rather than forwarding tokens or replaying bodies.

Native Chat Completions now uses the existing bounded SSE parser. `[DONE]`
ends both native and translated Chat streams without waiting for the provider
to close its HTTP body. A finish reason alone is not a terminal substitute
unless that source explicitly allows missing `[DONE]`. Usage received before
a later native stream failure is retained; missing/malformed usage is not
reported as a successful response by default. Key model/reasoning/tier rules
also apply before native dispatch, without dropping unrelated provider fields.
Dispatched native calls without trustworthy usage retain their reservation with
the existing durable reconciliation flag; stale cleanup cannot release it as
unused. Explicitly confirmed zero usage remains distinct from missing usage.
The source's missing-usage opt-in does not override a key's monetary/token limits.
Native HTTP streaming now buffers only terminal chunks (up to 16 frames/64 KiB)
until the service has settled or retained accounting. A late failure emits a
sanitized error frame, not a success DONE marker. Pending reservations now appear
in the existing request log with `reconciliation_required` status and unknown
usage/cost/latency, never reservation estimates or fabricated zero-cost events.
Pagination, filters and facets include them in a consistent read snapshot;
live unflagged reservations remain hidden. Settlement or explicit release
removes the pending projection without adding to totals twice. The existing
offline reconciliation command remains the repair path. Backend route/SQLite
tests, seven actual-binary frontend contracts and a synthetic browser check
cover visibility, null values, details, filters and admin authentication.

Client-triggered HTTP compaction now uses the existing compact operation with
one admission and settlement and produces the expected Codex SSE sequence.
WebSocket trigger requests retain normal subscription Responses forwarding;
generic `/v1/responses` is not redirected. Trigger shape, owner and billed-error
regressions pass. Standalone and trigger compact now share model/tier catalog
filtering and lossless-first historical trimming, including mandatory Lite/state
anchors, typed tool occurrences, bounded historical side-effect priority and
exact marker/framing accounting. Optional history is removed before file-owner
resolution. Compact now uses common Responses key-policy preparation, including
enforced/allowed reasoning, global Fast Mode prohibition and scalar-input
budgeting. New compact selection shares the stateful Responses selector after
capacity is acquired: routing strategy, pricing and separate quota windows are
not reimplemented as a first-eligible fallback. Established compact owners remain
strictly pinned without telemetry-only denial. Offline route regressions cover
single-account strategy, separate Spark quota, missing telemetry, owner retention
and exhaustion during the capacity wait, with one admission and no billed
selection refusals. Compact now retries only classified provider quota refusal,
after attempt accounting and outcome persistence, on an untried permitted
subscription under one capacity lease and deadline. Durable history restores
tool-output deltas and avoids duplicating full resends; without it, only an
unchanged self-contained resend retaining prior output is accepted. Encrypted
summaries, files, unknown owner state, trimmed anchored context, ordinary 429,
transport failures, operator blocks and accounting failures cannot authorize a
retry. Original continuation quota marks are scoped to both key and captured
account and the settled attempt's reservation sequence. A newer generation on
the same account ignores the old quota mark; current-generation refusal still
permits failover. Compact passes the reservation identity through its existing
billed-call boundary, without a new database migration. Successful retry stores
the new response/logical-session owner without
forwarding old turn/session headers. Canonical/equivalent HTTP and synthesized
trigger route tests exercise these rules against a local provider, including
candidate exhaustion and no reprobe after a durable quota flag.

Compact and paid external transcription now use the same retained-reservation
uncertainty decision as Native Chat/embeddings. Missing/partial/invalid usage,
disconnect, cancellation, panic and ambiguous upstream errors no longer become
free completed attempts. Only confirmed values settle; both valid zero and
reported usage on an error response are preserved. Subscription transcription
keeps its zero-token contract only after a valid successful transcript. External
minute pricing requires a confirmed finite duration and token pricing requires
token counters; one billing unit cannot substitute for the other. The existing
reconciliation flag prevents stale release, and failed compact cannot publish
a new owner or successful trigger SSE. Real local HTTP/multipart regressions
cover these cases and held-limit behavior. Canonical/trailing-slash audio routes
share handlers; no schema or production data was changed.
The shared provider-call boundary now returns sanitized failures for panics
instead of rethrowing arbitrary provider text into HTTP panic logs; a route
regression confirms the uncertain reservation survives. Native Chat's explicit
missing-usage allowance still works for unmetered keys, but cannot write a
finalized-success account outcome for its retained reservation. Metered keys
continue to reject that response rather than bypass their limits.

Ordinary Responses and translated Chat now carry both usage validity and report
presence through HTTP, SSE and WebSocket boundaries, including rejected WebSocket
handshakes. A quota error with `usage: {"input_tokens": 2}` is not a free rejection:
it holds the existing reservation and cannot trigger a hidden replay or token
refresh. Counter decoding cannot erase the independently valid quota code.
Confirmed billed/partially delivered quota failures still establish fenced
account/owner refusal after accounting; held refusal evidence survives explicit
later reconciliation. A plain pre-output quota rejection without billing, or
with valid zero counters, keeps its safe failover behavior. Real loopback route
and transport tests exercise partial/invalid/overflow reports, same-owner
continuation, multiple refused candidates, and confirmed-zero auth recovery.
No new ledger or repair mechanism was introduced for these cases.

The adjacent direct native Chat/embeddings path now preserves reported charges
on HTTP refusals and stream error chunks as well. Partial/invalid/overflow or
inconsistent-total reports retain the reservation rather than becoming a zero
4xx event. Shared operation parsing recognizes a valid error envelope even on
HTTP 200 and does not reclassify unusable counters as known merely because token
fields exist. Ancillary auth refresh cannot resend billed or uncertain token/
duration reports; a token-only confirmed-zero 401 can still recover once.
Malformed subsequent billing invalidates the prior stream total, while a later
delivery failure keeps already-confirmed counters. Route tests use real
loopback adapters and inspect the ledger, not only helper return values.

Admin/scheduled probes now reserve in the same ledger through a private warmup
principal, hidden from API-key lists and bearer authentication and separated from
keyless client traffic. It cannot be edited or reset through client-key APIs.
No client key budget is charged. Settled and pending request logs retain the
keyless `warmup` identity; pending usage survives restart/stale-release and uses
the existing explicit reconciliation path. Probe errors do not change operator
pause or conversation owners, and a settlement failure cannot be hidden behind
a quota sentinel. Focused SQLite/application and authenticated loopback probe
regressions, including cancellation cleanup, have passed with the race detector.
The separate public warmup API below uses the client's own key budget instead.

Responses Lite now derives its HTTP/compact header or WebSocket frame marker
from the actual body. Direct downstream WebSockets retain one connection-local
accepted Lite model/response ID for matching incremental frames; hidden retry
preludes never seed trust. Quota replay retains neutral Lite tools and answered
typed tool history, removes previous-owner state, and derives the new signal
again. Offline HTTP/compact/WebSocket and real synthetic quota-failover tests
exercise these paths without real provider calls. No new global cache or schema
was added for Lite continuity.

Direct source-routed native Chat now checks streaming, tool definitions/intent/
history, vision parts and active reasoning/effort against model capabilities
before dispatch. Unsupported requests return 400 and release the unused budget.
Provider-level checks cover enabled/disabled controls and permitted dispatch;
an authenticated HTTP regression checks zero upstream calls and no uncertain
reservations on refusals.

V22 persists/imports purchased-credit metadata, monthly free-plan quota and
canonical additional quota windows with freshness. Missing additional data does
not erase an earlier snapshot; an explicit empty list does. New model-specific
admissions use their own windows and cooldown with plan/tier/key/group checks;
established owners still bypass telemetry-only refusal. The additional-policy
dashboard write already uses the shared narrow versioned settings payload;
its component regression now asserts the exact policy-only payload plus the
loaded version. The `ultra` client-plane effort now becomes `max` only in the
outgoing provider body, across subscription/external Responses HTTP/WebSocket,
compact and translated/native Chat. Capability checks accept advertised
wire-equivalent max/ultra without enabling unsupported reasoning. Shared wire
builders also normalize explicit body overrides; summary, Lite context and
unrelated fields remain intact. Route regressions preserve the effective
client-plane effort in existing request-log fields and keep key allowlists
distinct from wire aliases. Configuration and original request bytes are not
rewritten. The subscription builder also maps effective `minimal` to the first
usable non-minimal effort in current catalog order, falling back to `low` when
the catalog has none. Responses HTTP/WebSocket, compact and warmup share this
wire behavior without extra requests. External sources declaring `minimal`
retain it, including source model slugs that shadow subscription models. Native
explicit effort aliases also pass the existing shared key
policy before selection/reservation; forcing an effort updates all supplied
effort fields, and an allowlist cannot be bypassed through `thinking` or
`reasoning` aliases. Unrelated native fields and disabled thinking controls are
preserved. Provider plan/identity refresh now writes metadata and quota in one
transaction, guarded by the captured identity/credentials and fetch ordering.
Workspace-less paid-to-free requires two free observations; a matching explicit
workspace permits immediate confirmation. The first ambiguous free result does
not publish quota. Reauthentication clears pending observations; routine token
rotation does not clear them, though rotation during a fetch can defer that
fetch's write until the next poll. V23 adds the observation checkpoint to existing
V22 installations. Imported pending confirmation counts are intentionally not
trusted, so two new observations may be needed after cutover. Targeted SQLite,
application and actual account-route tests cover downgrade, stale fetches and
operator state; final aggregate checks remain required. Private-backend reasoning
and selected settings parity are still incomplete.

Routing strategy checks found and corrected ordering differences in the Go
selector: fill-first now prefers primary then long-window usage, sequential
drain prefers lower configured capacity, and reset drain uses reset-day buckets
with remaining quota before precise timestamps. Stable drain ties use the
legacy account-ID hash rather than last-selection recency. Explicit manual
burn/preserve priority remains authoritative; generic earlier-reset preference
does not override drain or round-robin's own ordering. Relative availability
now uses dashboard-persisted power/top-K with defaults 2/5, bounded validation,
optimistic settings versions, V24 migration and legacy import. Its comparator
uses one clock snapshot. Focused ranking, actual fill-first HTTP, settings
route/restart/import and migration tests pass. Per-account capacity is described
below; soft affinity and its dashboard controls still require implementation.

Quota polling may clear an older quota-derived block only after an elapsed
window is confirmed reset and all previously known governing windows have
fresh available samples. The account-outcome checkpoint prevents that fetch
from overwriting a newer upstream refusal; operator-blocked states stay
blocked. No paid probe is involved. Accounts awaiting an explicit egress
decision are excluded from usage/reset-credit polling as well as model traffic.

Legacy warmup attempts are imported in the same transaction as account data.
Their account/window/reset tuples and timestamps survive restart; pending rows
remain claimed, preventing another paid attempt after cutover. No legacy error
message text is needed for deduplication. Unknown attempt states fail the import
instead of silently releasing the claim.

Manual reset credits now preserve both existing dashboard contracts: the
rate-limit route selects and pins a credit, while the usage-reset route lets
upstream select it. Either-route retries retain the original request shape.
The durable pin includes the original provider-outcome checkpoint; old pins
without one cannot acquire reset authority retroactively. Successful and
already-redeemed receipts refresh usage without a paid warmup and can recover
an account before natural expiry, but never clear a newer refusal or operator
block. The response includes before/after values. Cache epochs fence older
fetches after any consume attempt, including a lost response. Ineligible/missing
cached reads return null and evict stale data. Focused race-enabled application,
SQLite, authenticated route and synthetic upstream tests cover these paths,
zero available count with a stale item, malformed snapshots and reopened pins.
V25 persists the manual badge/expiry display preferences with defaults enabled
and imports explicit legacy values. The unsupported automatic-redemption switch
and request field have been removed from the dashboard. This does not spend
credits or change the manual consume routes.

V26 persists/imports `warmupModel` for the public `/v1/warmup` body/URL modes.
Normal/strict/force now use real bearer authentication, live key/group/model
scope, a maximum of five pinned calls and separate per-account outcomes.
Force bypasses only primary telemetry, not account state. Public calls use the
key's existing budget/settlement journal, correcting the legacy limit bypass;
admin/scheduled probes remain keyless. No warmup call creates or moves a session
owner. Probe retains its separate default/explicit model behavior.

V27 preserves environment-backed create/stream/recovery/fair-share limits,
including omitted/null/zero distinctions. A new database pins its first startup
environment values, whereas upgrades inherit through nullable overrides; an
explicit null restores inheritance in either case. Selection and capacity reservation
are atomic within the Go process. Ordinary Responses release create capacity
at the first real upstream event even for a nonstreaming client; stream capacity
lasts until that attempt returns. Compact/public warmup use create capacity;
admin/scheduled warmups share the same limiter. A new compact can select a free
alternative before dispatch. A confirmed owner cannot migrate merely because
its account is busy; recovery reserve is not available to forged identity or
synthetic traffic. HTTP capacity refusal is local 429 with Retry-After and does
not update account quota or health.

Compact billing regressions cover charged and malformed quota refusals. A
known charge settles once without hidden replay; partial/invalid billing remains
pending, while independently decoded quota proof still fences account/owner
state. The common ancillary accounting path no longer bypasses reconciliation
merely because the response also carries a quota error.

SSE keepalives start only after the first admitted/reserved dispatch, so waiting
past the keepalive interval cannot turn a local capacity refusal into HTTP 200.
Already-dispatched slow Responses and compact-trigger calls retain keepalives;
errors after response start still use the existing SSE error frame.

V28 persists/imports weekly working days in the existing CSV API format and
smoothing intervals. Overview and projections now consume those values, using
fresh canonical quota windows, reset-aware rates, UTC working days and the
existing nullable pace DTO. The current usage poller runs once per minute;
the freshness calculation uses that interval with the legacy five-minute floor.
Queries preserve all six-hour samples plus the 64-row tail; above 100000 rows
they report an explicit error rather than silently truncating. Seven-day demand
includes the observation before the boundary, and two-hour key attribution
excludes synthetic traffic. Calendar windows above 32 days or future observations
are excluded; a simulation hitting its work bound returns no pace instead of
a falsely safe partial forecast. Enterprise capacity agrees with the key and
overview estimates. This remains read-only reporting, not a quota planner.

V29 adds persisted cache-affinity TTL (default1800, first-boot environment seed)
and split primary/secondary thresholds (95/100), including the legacy primary
alias and canonical precedence. Key-scoped typed `affinity_bindings` are separate
from correctness continuations. A current reserved key/account attempt supplies
the CAS generation, so stale refresh/rebind cannot overwrite deletion or a newer
generation. Authenticated list/filter/sort/page, single/batch/filtered delete and
stale purge match the existing dashboard contract. The existing maintenance loop
prunes bounded stale prompt-cache batches; an account invalidation trigger removes
locality hints without deleting response/file/voice owners.

UC2.6 now connects those settings to Responses HTTP/WebSocket and compact
selection. Explicit cache keys work independently of automatic sticky hints;
thread/session/client hints and bounded fingerprints are key-scoped hashes.
The existing atomic account admission uses only currently eligible preferences.
CAS storage runs after reservation and before dispatch, outside the capacity lock,
and never runs for a hard owner or synthetic warmup. A failed write settles the
unused reservation without starting SSE or contacting the provider. Locality
storage reuses reservation account checks, including purchased-credit eligibility
and current key/group scope, rather than requiring a raw `active` status.

V30 stores the turn-token forwarding discriminator on existing fenced
continuations. Process/thread tuples have separate hard aliases; bare-session
clients remain compatible. Header aliases normalize at ingress. Independent
previous/session/turn/file proofs cannot silently disagree. Proxy-generated WS
tokens stay local, and unknown explicit tokens without an owner fail before
dispatch. Only successful accounted replies publish new aliases. For example,
at 0% thread A stays on its original account while sibling B can select another;
after A receives an upstream quota refusal its successful replay updates A's
client alias, but the old upstream token is never sent to the replacement.
Thread-only reconnects also retain REQUIRED capability lineage.

The affinity/identity integration passed full Go tests, full race tests and vet;
the embedded dashboard builds and nine frontend contracts pass against the new
Go binary, including affinity settings and real list/delete DTOs. Focused cases
cover compact capacity spillover, late completion, zero-quota owners, quota replay,
key isolation, header aliases, unknown/conflicting tokens and restart fencing.
The active change passes strict OpenSpec validation and the design validates all
19 use cases. Repository-wide `openspec validate --specs --strict` separately
reports nine unchanged legacy specs lacking SHALL/MUST in some requirements;
these are not new runtime failures. No running installation was replaced.

Before the affinity/identity extension, full Go/race/vet passed; weekly-bound,
first-boot override and SSE admission changes passed their focused race checks
and vet. The 149 frontend files/1202 tests passed (existing mock/React warnings
remain), and eight schema/API contracts passed against that earlier rebuilt static
binary. All provider traffic used loopback stubs and temporary data; no installed
server, private database or deployment was changed.

UC2.8 now applies explicit transport selection before per-key/global HTTP policy.
Auto subscription routing consumes catalog preference, keeps native Codex HTTP,
images and oversized requests on HTTP, and limits smart WS to explicit continuity
signals. Dedicated subscription WS and external-source protocols remain distinct.
The only automatic WS-to-HTTP fallback is a proven pre-create handshake refusal
(eligible 426 or identified Cloudflare challenge), on the same owner/reservation.
Lite wire data is normalized again for HTTP, and later auth refresh retains that
transport. Permission/quota errors, network failures, post-create errors, billed
or uncertain refusals, explicit WS and REQUIRED cannot use this fallback. Offline
route tests cover policy precedence, catalog input, protected fallbacks and the
single accounting event. The subscription adapter also preserves the scoped
session header and four request-local Codex compatibility headers. Every reused
WS frame receives its own metadata, with body values authoritative; fresh replay
does not reintroduce discarded old headers, and external sources receive none of
this subscription-only projection. Actual loopback HTTP/WS tests cover reuse,
body precedence, arbitrary-header exclusion and replay.

UC4.2 is implemented with V31 account incarnations and durable deletion choices.
Deletion hides the account, erases credentials and revokes key/group/source
assignments without opening empty scopes or refunding spent limits. Existing
maintenance resumes bounded history cleanup after restart: retained history is
orphan-attributed, while explicit removal subtracts attributable raw/folded report
totals without erasing financial receipts. Reimport waits for cleanup and starts a
new incarnation with no old grants, owners or cached usage; mandatory egress denial
survives. Delayed reservations still settle once under their original deletion
policy, but token/usage/catalog jobs, owners, diagnostics, quota recovery and warmup
claims cannot mutate the new incarnation. Reused upstream sockets also distinguish
incarnations even if imported token bytes are unchanged. Reset redemption keeps
its original request-ID uniqueness across deletion, so a reused identifier cannot
select a second credit. Pending OAuth callbacks cannot overwrite a later import.
External source deletion shares the same credential/access revocation boundary.
Group, key and scoped-automation saves reject deleted members transactionally,
so a stale settings write cannot restore assignments while the identity is deleted.

Legacy imports now preserve unfinished deletion and its history choice, including
intentionally empty credentials, instead of rejecting or reviving that account.
Older snapshots without the choice retain history. Synthetic import fixtures
verify unchanged source hashes, closed scopes, preserved key spend and completion
through the same restartable cleanup. Full Go tests/race passed for the integrated
incarnation changes; the later metadata/import/receipt guards passed focused race
checks and the full Go suite. Vet and the dashboard build pass. All 1211 frontend
tests passed, including nine schema/API contracts against the rebuilt static
binary; those nine contracts passed again after the final backend changes.

First-run dashboard state now distinguishes a required password setup from a
required remote bootstrap token. Both the session DTO and setup handler share
the existing trusted-identity decision; direct loopback shows password setup
without a remote-blocked warning, while remote spoofing remains refused. The
updated screen and dialog were checked in an offline browser at desktop and
mobile sizes. Settings writes use explicit patches plus the loaded version,
not a replay of unrelated legacy fields or schema-generated defaults.

`/health/ready` checks local storage after successful initialization. SIGTERM
closes new admissions, including new turns on existing WebSockets. Active work
gets `--shutdown-grace` (default 30 seconds); remaining requests are cancelled,
and handler/accounting lifetimes are joined before background services and
SQLite close. Failure to join does not silently close the database under a
still-running finalizer. Request-body reads, upstream connections, and forced
cleanup have separate bounds; the whole SSE response has no short HTTP write
deadline.

The sample systemd unit uses a fixed private StateDirectory, an optional
environment file and restart-on-failure. `systemd-analyze verify` passed against
a temporary installation root containing the built binary; the service has not
been installed or started by systemd on this machine. The ordinary Dockerfile
successfully built assets and a static Go binary. Its disposable test container
ran as UID 10001 with a read-only root and no external network, reached readiness,
retained its configured password across stop/start and exited on SIGTERM. The
test container, anonymous data volume and validation image were then removed.
Legacy/private data was excluded from the build context. This validates the
delivery recipe, not completion of selected runtime parity or authorization to
replace a running installation.

The reproducible small HTTP comparison is `scripts/compare-runtime.py`. It uses
fresh temporary data, a loopback-only Chat Completions stub, eight warmup calls,
128 measured calls at concurrency eight and alternating runtime order. It does
not load private environment files or contact providers. The initial three-run
Go medians were 24.47 MiB RSS after load and 430 ms CPU per 128 calls, versus
220.74 MiB and 1070 ms for legacy, but Go latency was worse (349.43 ms p50 versus
115.73 ms). Inspection identified rollback-journal commit I/O; validated Go
databases now use WAL with `synchronous=FULL`, without relaxing durability. In
three follow-up runs Go p50 was 94–105 ms, throughput 73–84 requests/s, CPU
250–320 ms and RSS about 24 MiB. Host load affected the parallel legacy samples,
so these are limited offline measurements, not a production throughput promise.
The 2026-09-27 rebuilt binary (before subsequent native-refusal-only hardening)
was then compared in three alternating runs per
workload, each with 128 requests at concurrency eight. Median JSON results were
24.78 MiB RSS / 280 ms CPU / 95.79 ms request p50 for Go, versus 220.88 MiB /
1080 ms / 119.74 ms for legacy. For paced SSE, Go used 25.30 MiB and 370 ms CPU,
versus 222.62 MiB and 1380 ms, but its first-token p50 was slower: 40.85 ms versus
14.68 ms, with completion p50 172.55 ms versus 115.12 ms. This is a measured
streaming latency gap, not evidence that Go is faster on every path; its cause
has not yet been isolated. The tests do not justify relaxing accounting or
durability to reduce that gap.

Cancellation was measured until the stub observed the upstream socket close,
not just until the client closed: p50 40.06 ms for Go and 39.93 ms for legacy.
Those values include the stub's 20 ms pacing and TCP close detection. Go's
stream/cancel FD count remained 11 before and after each load. Startup readiness
was about 76 ms for the fresh Go installation versus 6.5–6.9 seconds for the
median legacy trials; this does not include real provider/OAuth initialization.
All temporary processes and databases were closed by the benchmark. These are
offline small-workload measurements; final selected-parity checks remain open.

The V31 runtime was measured again after incarnation/transport changes with the
same three alternating 128-request, concurrency-eight runs per workload. Median
JSON results are 25.61 MiB RSS, 320 ms CPU, 101.48 ms request p50 and 76.85 req/s
for Go, versus 220.80 MiB, 1110 ms, 120.11 ms and 65.57 req/s for legacy. Paced SSE
uses 25.77 MiB / 420 ms CPU for Go versus 222.30 MiB / 1330 ms for legacy, but
first-token p50 remains worse (38.48 versus 14.06 ms), as does completion p50
(168.59 versus 114.72 ms). Cancellation reaches the stub in about 40 ms for both;
Go stream/cancel descriptors stay at eleven. These results confirm lower memory
and CPU use, not a universal throughput/latency improvement. The final subsequent
membership-save guard changes only administrator mutation validation, not the
measured request path. No real providers or installed services were used.

UC3.2 now separates live external-source edits from account deletion. V32 adds
`route_revision` to accounts and reservations, incremented when kind, base URL or
Responses/Chat support changes. Dispatch, current-owner checks, late operational
writes, translated history and upstream sockets preserve the captured revision;
A-to-B-to-A edits cannot revive old work. Existing owners are deleted atomically,
not merely expired against a potentially older request timestamp. Financial
settlement retains the original bill. Name/pricing saves preserve current owners
and quota status; route changes preserve operator pause and mandatory egress deny.
Offline HTTP regressions cover authenticated source PATCH while a request waits
before dispatch, during success or before a charged quota refusal. They verify
one original bill, unchanged key accounting and successful fresh-route admission.
The shared provider guard also covers native Chat, embeddings and transcription.

The selected-function check added offline end-to-end `image_generation` and
realtime-call/live-WebSocket tests; neither found a production defect. Dashboard
parity exposed the missing runtime connect-address endpoint used by Windows OAuth
help. It is restored behind admin authentication with the original environment
override, bounded DNS lookup and hostname/loopback fallback. Only address-safe
characters may enter the copyable instruction. The callback listener remains
loopback-only: this helper does not configure remote networking, and a remote
installation still needs an operator-managed tunnel or the existing manual
callback flow. Obsolete quota-planner/proxy-pool text is removed from Advanced
settings descriptions without adding controls.

The CLI runtime/import/reconciliation/shutdown tests and focused SQLite import,
migration, read-only inspection and unknown-database refusal tests passed again
with the V32 work in progress. Together with the already completed disposable
Docker lifecycle and temporary-root systemd validation, this closes the delivery
implementation check (3.1). It does not authorize a production import or cutover;
the final integrated V32 build also passes Go tests/race/vet, frontend build and
all 1211 frontend tests, including nine actual-binary API contracts.

Final V32 measurements repeat all three workloads in three alternating runs
with 128 requests and concurrency eight. Median JSON values for Go/legacy are
25.25/220.73 MiB RSS, 310/1060 ms CPU and 99.35/115.03 ms request p50. Paced SSE
uses 26.14/222.78 MiB and 410/1340 ms CPU, but first-token p50 is 36.63/13.86 ms
and completion p50 156.69/114.35 ms. Cancellation reaches the loopback upstream
in 40.07/39.96 ms. Go streaming/cancellation descriptor counts remain eleven.
Every temporary benchmark process and database is closed by the script. These
results support reduced resource consumption, not universal speed improvement.

An external reverse proxy may switch new admissions to a ready replacement;
existing sockets cannot migrate between processes. An old database snapshot
does not include post-cutover writes, so rollback after new traffic needs an
explicit reconciliation decision, not just selecting an older binary.
