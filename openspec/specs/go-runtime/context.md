# Go runtime: operation and compatibility

- Last edited with skill pack: `0.2.2`

The normative contract is [spec.md](spec.md). The previous Python/Rust runtime
is retained in Git history; its temporary `legacy/` tree was retired with the
administrator's approval for the Go release. Other capability folders
remain legacy references unless explicitly referenced by the Go contract; they
do not re-enable excluded features in the new runtime.

## Distribution and data

`make web-deps build` produces `bin/codex-lb`, a static Go server with the React
dashboard embedded. Linux/systemd is the delivery target; Node/Bun, Python,
Rust and an external relay are not runtime dependencies. The binary embeds
timezone data but uses the operating system CA store for TLS.

The default listener is `127.0.0.1:2455`. `--data-dir` or `CODEX_LB_DATA_DIR`
selects an absolute private directory. Without an override, the platform user
configuration directory is used rather than the current working directory.
No `env.local` is required. Preserve both `codex-lb.sqlite3` and `encryption.key`.
Startup checks schema, key ownership and credentials before readiness, and locks
the actual database inode. One SQLite installation has one running server.

For existing Codex clients, retain `http://HOST:PORT/backend-api/codex` as the
provider base URL. The OpenAI-compatible base `http://HOST:PORT/v1` is also
supported; migration does not require changing one prefix into the other.
The shared runtime router must register the exact Responses paths ahead of the
Realtime call-ID wildcard. Otherwise a native WebSocket handshake to
`/backend-api/codex/responses` is misclassified as call ID `responses` and returns
409 `realtime_call_owner_not_found`. The 2026-10-01 fix changes registration,
not Realtime ownership or conversation/account checks.

`deploy/codex-lb.service` is a Linux systemd template, not an installed service.
It uses a dedicated user and private state directory, restarts failed processes,
and can be enabled by the operator for boot. The optional Dockerfile builds the
same server and keeps data outside the image. Neither delivery method changes
an existing installation as a side effect of building or testing.

## Administrator and proxy access

The dashboard retains password/TOTP access. First-run setup always requires a
one-time bootstrap token: `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` (32+ characters)
or the file `dashboard-bootstrap-token` the server writes to its data directory.
No socket peer or `Host` value is treated as local; keyless proxy access needs an
explicit `--unauthenticated-client` network (see change
`harden-dashboard-trust-and-update-integrity`). Trusted identity headers require
explicitly configured trusted socket peers. CSRF protection remains enabled, including
under the explicitly selected installation authentication modes.

The persisted proxy IP allowlist applies to proxy operations, not to the
administrator's repair interface. Forwarded identity requires a complete valid
chain from a trusted proxy; a spoofed singleton header is not authorization.

Windows OAuth help reads `/api/settings/runtime/connect-address` as an
authenticated informational endpoint. `CODEX_LB_CONNECT_ADDRESS` overrides the
request host when an installation needs a different address in the instruction.
DNS lookup is bounded; loopback gives a placeholder. This does not expose the
callback listener beyond loopback. A remote deployment needs an operator-managed
tunnel or the existing manual callback flow.

## Account import and key-holder reports

Accounts → Add account → Import offers a local file or a manual JSON paste.
Both send the existing `auth_json` multipart file to the same server validator;
there is no second credential API or automatic clipboard permission. For example,
choose Paste JSON, use Ctrl+V and explicitly submit Import. The 1 MiB limit is
shared with file selection. A rejected document stays editable; closing the
dialog or switching modes discards the temporary draft.

Standard Codex fields (`id_token`, `access_token`, `refresh_token`, `account_id`
inside `tokens`) and the older dashboard camelCase aliases are accepted by the
same parser. The three credential values remain required; `account_id` may be
absent/null so the existing claim-derived identity applies. Conflicting non-null
aliases or non-string values reject the document without changing stored accounts
or echoing credentials. For example, the exported `codexAuthJson` can be pasted
back into Import without manually renaming its fields.

The dashboard sign-in screen offers Administrator and API key reports on the same
page. For example, open the dashboard, choose API key reports and enter the existing
key; no administrator password is needed. The former `/key-reports` bookmark now
redirects to this common entry. An existing administrator session continues to
open the admin dashboard; sign out first to choose another sign-in method.
The key holder sees its own totals, comparison, daily rows and model/client
distributions, personal limit usage and a limited same-group summary, without
admin navigation or data queries. The common screen reads
the public authentication-session status to retain the configured admin flow.
The server chooses the
scope from that key and rejects account/key selector overrides. It shares report
calculations, not administrator access or account identity data. Report reads
remain available when generation quota is exhausted.

The personal limit panel reuses the administrator key page's used/maximum rows.
Its counters belong to current limit windows, not the selected report dates,
and include held reservations for unfinished requests. An expired window is
shown as reset without changing stored ledger values. A grouped key also sees
a noninteractive list of its group's keys: name, enabled/expiry state and the
highest personal limit-consumption percentage. Each key has its own counter:
Alice at 25% and Bob at 72% do not consume a shared 97% budget. Deleted/internal
keys and all other groups are excluded. No peer secret, prefix, account
assignment or traffic details are returned.

The group account-quota panel combines upstream subscription usage with capacity
weights, separately for 5-hour, weekly and monthly windows. A Plus account at
100% and Pro at 0% do not average to 50%. Coverage shows how many subscription
accounts have known current quota data; missing/expired observations and unknown
plan capacities do not become invented zeroes. Purchased balances remain
separate. The global upstream privacy setting and the key's account-pool
visibility can hide this panel. Only group aggregates are returned,
never provider account identities or individual balances.

The same group panel also shows aggregate Purchased credits remaining. Finite
known balances are summed once per account, not once per quota window; zero is
known zero, while absent balances stay unknown. Any unlimited account displays
Unlimited. The separate credit-coverage count makes partial totals explicit;
10.5 and 25.25 plus one known zero and two unknown balances display 35.75 with
three of five accounts known. This does not change key budgets or perform credit
purchases/redemptions. Individual account balances remain private.

The browser sends the API key once to `POST /api/key-reports/session`. The server
issues a separate encrypted HttpOnly, host-only, SameSite cookie restricted to
`/api/key-reports`; neither JavaScript storage nor the cookie retains the original
generation credential. `GET /api/key-reports/session` restores the login on reload,
and `GET /api/key-reports/reports` returns the same scoped report as the bearer-only
`/v1/usage/reports` API. For example, log in by key and press F5: the report returns
without asking for the key again. The existing dashboard session lifetime applies
(24 hours by default, with the existing remote cap for longer settings), never
beyond the key's own expiry. Keep the data directory and encryption key across
server updates; no process-local session table or database migration is needed.

Sign out waits for `DELETE /api/key-reports/session` to clear the cookie before
discarding the private query cache and returning to the common login. Revocation,
deletion, regeneration or expiry of the API key hides reports on the next check.
Network/server errors remain retryable and do not discard the session; a failed
logout is not presented as successful. Changing sign-in methods clears abandoned
drafts, and switching is disabled while session creation is pending. As with admin
sessions, logout removes the browser cookie rather than maintaining a denylist of
copied stateless grants; revoke/regenerate the key to invalidate every such grant.

No password or administrator cookie is issued. The report cookie cannot authorize
generation, bearer APIs or admin endpoints. CSRF protection applies to login and
logout, and the proxy allowlist still protects login and report data. Status and
logout remain available when the IP policy changes. Serve remote installations
over HTTPS; Secure cookies follow the trusted-proxy policy or `--public-origin`. This page
does not replace deployment TLS or administrator authentication.

## Routing and accounting

Accounts may belong to multiple groups. A key selects one group and gets its
limit configuration with independent consumption. Leaving a group keeps the
last group limits; an empty scoped group does not mean unrestricted ChatGPT access.
The group picker therefore displays No accounts selected for an empty membership,
including its clear-selection menu item. All accounts remains the label for an
unrestricted direct API-key assignment or an all-account automation, not for an
empty group. For example, clearing a group's sole ChatGPT member leaves its keys
scoped to zero ChatGPT accounts; it does not broaden them to other ChatGPT accounts.

External Model sources have their own key assignment scope. A grouped key with
All model sources can use an enabled, permitted Z.AI source without that source
being a member of the ChatGPT group. Explicit source assignments and model
restrictions still apply to both catalog reads and actual requests, reservations
and continuation ownership. This separation also applies to a group with no
ChatGPT members; it never bypasses credential, account-status or egress checks.

Settings → Account groups → Reset period restarts the group's keys' configured
limit windows after confirmation. For example, a weekly $800 limit showing $300
used returns to zero with a new weekly boundary. The operation includes disabled
member keys, excludes deleted/internal keys and leaves history, provider quotas,
purchased credits and the configured limits unchanged. Old in-flight reservations
still settle in their original accounting windows. An error keeps the dialog open
for retry; no real provider operation is involved. See the reset requirement in
[the Go runtime contract](spec.md).

An All-account key was checked again on 2026-10-01: a fresh Luna request completed
with 11 input and 5 output tokens. Admin/proxy regressions cover omitted and empty
assignment lists, choosing a specific account, returning to All, unrelated edits,
source scopes and closed empty groups. Historical no-account 503 errors exist in
the client log, but their prior policy state is not reconstructible from current
rows. These checks do not claim that historical cause was identified or repaired.

The Groups section of an account card can select several groups and save them
in one update. For example, selecting groups A and B makes the account available
to the existing key scopes of both groups. This updates only the account's
membership rows, not group limits, key consumption or other members. Clearing
all checkboxes removes its memberships without deleting the account. Unknown
groups reject the entire update; failures keep the editable selection, and a
successful save returns to the refreshed server state. Editing each group
individually remains available.

An established permitted owner may continue at reported zero remaining quota
until upstream actually refuses it for quota. New sessions do not get that
exception. Timeout, capacity pressure or a generic HTTP429 does not authorize moving
an owned conversation. Safe quota failover requires sufficient reconstructible
history and must not transfer account-bound files or upstream continuation IDs.
An ambiguous already-executed call is not silently retried.

Provider-side quota resets need not change the reset deadline. Ordinary usage
refresh also recognizes the explicit nested backend pair `rate_limit.allowed=true`
and `limit_reached=false`, provided every governing window is complete and below
100% used. This recovers both legacy blocks without refusal timestamps and accounts
whose restored percentages were already saved by earlier polls. Percentages alone
are not that proof; absent flags preserve the natural-reset path, while explicit
denial prevents ordinary recovery. Newer provider outcomes and operator policy
still win through the existing generation/outcome conditional write. For example,
a blocked weekly account at zero used with an unchanged future reset deadline
becomes active on its next accepted available snapshot, without a paid probe or
another reset-credit redemption. Purchased-credit admission rules are unchanged.

Dashboard account cards show subscription and purchased credits separately.
Subscription credits are the existing plan-based estimate for the observed
primary, weekly or monthly window; they are not the purchased balance. Unknown
data stays `-`, an observed exhausted balance is `0.00`, and an explicit unlimited
purchased-credit flag is displayed as unlimited. Both account and dashboard
summaries read durable metadata, without depending on an in-memory usage cache.

Usable purchased credits cover exhausted included windows, including the primary
window, for new requests as well as existing work. For example, a weekly window
at 100% with 12.5 purchased credits remains eligible within the key's allowed
accounts. An explicit finite zero balance removes this override even if a generic
has-credits flag remains true; unlimited is handled separately. A confirmed
upstream quota refusal outranks credit observations at or before that refusal in
both selection and transactional reservation. Newer usable credit evidence can
restore admission; authentication, operator-disabled states, unresolved rate-limit
blocks and separate model quotas still apply. This follows the Codex credit
continuation rule in [official pricing](https://developers.openai.com/codex/pricing/),
not a promise that a positive balance overrides every OpenAI error.

Unknown billing stays reserved for explicit reconciliation. After an interrupted
process, startup marks those reservations without inventing zero usage or
releasing key budget. `reconcile-usage` uses the same exclusive installation lock;
it is an offline operator tool, not an automatic paid retry.

External Z.AI/OpenAI-compatible sources use the integrated Responses/Chat
adapter. The Z.AI preset allows either protocol in Settings → Advanced settings →
Model sources. For Coding Plan, use `https://api.z.ai/api/v1` with Responses
enabled (Chat disabled for a Responses-only endpoint), or
`https://api.z.ai/api/coding/paas/v4` with Chat enabled. These are base URLs;
do not append `/responses` or `/chat/completions`. See the
[provider endpoint guide](https://docs.z.ai/devpack/quick-start).
The selection never rewrites the URL or credentials; a mismatched endpoint fails
explicitly, without automatic protocol or billing-route fallback. Existing Chat
sources retain their defaults and GLM thinking customization; native Responses
uses the shared Responses adapter without injecting Chat-only fields.

An HTTP-only external source cannot accept Codex's connection-bound WebSocket
prewarm/delta contract. Before reservation or dispatch, its WebSocket requests
receive `503 model_source_requires_http_transport`, matching the original
implementation's client-fallback signal. Codex can then submit full context over
HTTP; it may display a transport fallback notice. For example, GLM-5.3 with
`store:false` must not receive an HTTP tool-output delta referencing an unsaved
response ID. Subscription native WS and explicitly configured external native
WS are unchanged. This prevents new unsupported-prewarm reservations; it does
not declare earlier unknown usage free or resolve old reservations.

Capabilities and custom rates are source-scoped. Editing an endpoint,
provider kind or Responses/Chat protocol advances its routing revision. Existing
work retains its original bill, while old ownership and socket/cache state cannot
revive even if the address is changed back. Ordinary name/price edits keep active
ownership and confirmed quota status. Account deletion has a separate incarnation
and restartable history-cleanup policy; it never refunds consumed key limits.

## Quota observations and probes

Subscription discovery and provider requests share the Codex client version
`0.156.0`. This is protocol compatibility metadata, not the codex-lb release
version. On 2026-09-27, read-only catalog requests on the same account omitted
GPT-6 with `client_version=0.144.0` and included Astra/Sol/Luna with `0.156.0`,
the installed CLI version. Updating only the probe model left this stale identity
in place. The supported default now covers catalog query/User-Agent and provider
Version/User-Agent on both HTTP and WebSocket paths. Settings exposes the
persisted `codexClientVersion`, initially `0.156.0`, so an operator can save a
supported CLI version without rebuilding or restarting. A successful change
signals the existing free catalog poller. New HTTP requests and new WebSocket
handshakes read the saved value; active streams and reused connection-bound
continuations keep their original connection until normal closure. Invalid
versions are rejected and failed setting reads do not use a stale fallback.
Standalone adapters still support explicit constructor overrides. There is no
automatic version download or model substitution. Migration 33 adds one settings
column; an older binary requires the pre-upgrade database backup for rollback.
The [Go runtime contract](spec.md) owns the compatibility requirement.
OpenAI's [model guidance](https://learn.chatgpt.com/docs/models) distinguishes
availability by account and client: a model appearing in a catalog alone does
not establish successful live inference. Local regression upstreams check the
version gate; live verification must not silently issue paid generations.

The Force probe action defaults to `gpt-6-luna` and makes one pinned generation;
it does not retry on a more expensive model. Explicit models in requests,
settings or scheduled jobs are not rewritten. A failed administrative probe can
remain pending reconciliation if its actual usage is unknown. Its internal
principal is separate from user-key limit budgets; this is not evidence of
either a successful free request or a user-key charge.

Unlike the public Responses API, the private subscription backend rejects
`max_output_tokens`. Subscription HTTP/WS/compact normalization omits it while
external-provider requests retain their supported output-limit behavior. The
legacy implementation already filtered this field; its omission in the Go port
made the synthetic probe fail. The upstream fixture now reproduces that rejection.
There is no guaranteed 16-token cap on a subscription probe. Normal key admission
uses at least its existing 2048-output-token estimate for subscription requests,
still clamped to remaining key budget by the ledger; actual reported usage replaces
the reservation. This avoids treating an unsupported one-token hint as a bound.
Neither this repair nor a later successful probe resolves old unknown usage.

The subscription endpoint can also omit Content-Type while returning a complete
SSE stream. The original implementation at commit
`09a140fa9979a908e60acc97232367e0a08ef32c` reads this body after a successful
HTTP status without a MIME gate (`app/core/clients/proxy.py`, direct stream
path lines 4150–4163). The Go port had added a strict MIME requirement and closed
such responses before their first event, losing the terminal usage. A live
capture confirmed an absent header and valid SSE that replayed successfully
through the unchanged Go parser. Only the ChatGPT provider now permits absent
SSE MIME; explicitly incompatible types and external-provider behavior remain
unchanged. Terminal, response ID, stream bounds and usage checks still determine
success. Unlike the original administrative probe's status-only shortcut, the
Go probe continues to require and account for the actual response usage.

Upstream sometimes reports a sole seven-day allowance in `primary_window`.
Its 604800-second duration identifies it as weekly, not five-hour capacity.
For example, 41% used becomes 59% weekly remaining with no primary window.
A fresh complete snapshot replaces the current standard window set; absent or
partial telemetry retains prior evidence. On the first corrected observation,
the old mislabeled weekly primary and its seven-day history labels are repaired
atomically for that account. Observed values and financial records are retained.
This repair uses normal polling, not a paid probe or an in-place schema migration.

## Prices and history

Settings → Model prices allows a new Codex tariff or a bundled-price override
without restarting or rebuilding. Rates are USD per million tokens; accounting
uses integer microdollars. Saving recalculates retained matching history,
including previously unpriced requests, and reconciles applicable current
monetary/credit limits without changing tokens or undoing manual limit resets.
The price and accounting changes commit together.

For example, adding the tariff for a newly released model corrects its old
zero-cost report rows using the saved input, cached-input, output and tier data.
No provider request is made. Missing dimensions in old aggregates are reported
as partial, not fabricated. Insufficient charge provenance can require explicit
reconciliation before the tariff is committed. Removing a custom-only price
leaves the model unpriced rather than free. External-source rates remain separate.

Every request contributes content-free accounting. Diagnostic payload archives
contain errors only and are encrypted, redacted, bounded and administrator-only.
Successful payloads are not diagnostic archives. Bounded operational continuation
history is separate, and archive retention does not remove active ownership.

## Migration and cutover

Import requires a consistent COPY of legacy SQLite and its matching Fernet key.
It rejects an existing destination and preserves the complete source snapshot
privately alongside the imported data. Required removed proxy bindings remain
blocked until the operator chooses an allowed route. The source is not changed.

```sh
bin/codex-lb import-legacy --source /backup/legacy-copy.sqlite --source-key /backup/encryption.key --data-dir /srv/new-codex-lb --dry-run
```

Dry-run uses a disposable private destination. A real import creates the selected
new directory and leaves a failed destination available for inspection. Import
output contains counts, not credentials. PostgreSQL is not a supported runtime
or an implicit SQLite import source.

Building and offline verification are not authorization to replace a server.
Cutover needs a separate operator command, backups and a verified target. A
reverse proxy can preserve the client port while switching new admissions after
readiness, but existing sockets cannot migrate between processes. Drain the old
server and preserve final accounting before retiring its data. Rollback after
new traffic must reconcile new writes; an older binary plus an older snapshot
alone is not a lossless rollback.

## Verification limits

Initial rewrite verification used synthetic temporary installations and loopback
providers, including HTTP/SSE/WebSocket, imported data and frontend API contracts.
Later authorized tests on 2026-09-27 ran Codex CLI 0.156.0 against real Luna and
GLM-5.3 for answers, executed tools and resumed sessions, checking actual usage.
Luna HTTP/native WS and GLM HTTP passed. The external-source fallback repair was
also exercised by the real CLI against a rebuilt isolated instance.

The scoped Codex catalog was also loaded through the documented CLI
`model_catalog_json` setting. Absent optional enum defaults now serialize as null
instead of invalid empty strings, matching the original catalog contract. The
real GLM answer/tool/resume check completed without the missing-metadata warning;
its four upstream requests settled with no uncertain reservations.

Real Luna compaction exposed the retired private `/responses/compact` endpoint
(404). The adapter now follows the original in-band Responses contract with a
terminal compaction trigger, bounded SSE collection and actual usage settlement.
Standalone and HTTP-triggered compact both preserved a random marker on the
next real generation. A separate Codex CLI run auto-compacted once and resumed
successfully, settling all three requests (50,224 tokens / 2,703 microdollars)
without an uncertain reservation. These runs used an isolated application and
ledger with a non-refreshing access-token-only source, not a second OAuth client
copy or a working-service upgrade. Compact/public warmup now hold stream slots
and release create slots on first upstream events; failures after output cannot
be replayed as free quota refusals.

A separate actual HTTP 400 `invalid_request_error` / `unknown_parameter` test
exposed an unnecessarily held ordinary Responses reserve. Complete validation
rejections with no output or billing now release only that unused reservation
as a failed attempt; they do not authorize replay or alter account health. The
real rerun recorded one failed zero-charge request with no reconciliation hold.
Unstructured detail-only errors and in-stream failures remain outside that narrow
classification, and historical uncertain reservations are not automatically reset.

Subsequent live auxiliary checks used the same non-refreshing isolated account.
The Codex JWKS read succeeded. Manual and due-scheduler Luna pings completed with
separate admin accounting, no client-key token charge and no duplicate slot on
scheduler reconstruction; this was not a whole-process restart test. The test job
was removed from its disposable database. A locally synthesized four-second speech
fixture was recognized through both private and v1 transcription routes with no
uncertain reserve. A synthetic PNG also passed registration, signed upload,
finalization and file-backed Luna recognition (48 tokens / 6 microdollars).
Remote deletion of that test file returned 404 and is not confirmed:
`file_000000000b3081f4a0bf5194aedf3850`, `codex-lb-e2e-synthetic-square.png`.
Do not rerun that upload merely to repeat already-passed behavior.

Realtime contract comparison additionally corrected lost call query/MIME/body
information, private failure disclosure and the WebSocket library's implicit
32 KiB read limits. Actual local WebSocket peers verified 128 KiB text/binary
frames, oversize rejection, abnormal close and cancelled-request accounting.
This verifies the transport/ownership contract, not a live WebRTC media call;
SDP and call payloads are excluded from diagnostic archives even on failure.
Control registration also preserves the original GET/POST goal-read methods and
single trailing-slash equivalents, with canonical forwarding and unchanged auth.
Realtime uses strict user-key authentication even in the optional local/CIDR
keyless mode. Otherwise unrelated callers would share the internal local call
namespace; the general Responses keyless mode remains intentionally unchanged.

The first actual call also exposed a missing native protocol header. Realtime
now preserves bounded OpenAI-Alpha/Beta values, removes Responses-only Beta
tokens, and retains the original `/v1/realtime?call_id=...` legacy sideband
alongside v3 live paths. A real native v3 WebRTC peer subsequently completed
call creation, ICE/data-channel setup and authenticated LB sideband attachment:
202 synthetic audio packets sent, 216 received, 27 data-channel and 73 sideband
events. There were no upstream error events or uncertain reservations. This used
a test-only Pion peer, not a browser UI certification; legacy v1/v2 negotiation
has local HTTP/WS contract coverage rather than a separate live media call.
The two content-free operation records do not establish token pricing for media.
SDP, ICE credentials and private failure bodies were not logged. The working
service and its refresh-token ownership stayed unchanged.

The built-in image tool was also invoked through real Luna Responses. It
returned a decodable PNG (666,243 bytes), with 2,404 parent-response tokens and
no uncertain reservation. The private backend returned 1254 by 1254 despite
the requested 1024 by 1024; a check at the outbound transport confirmed that
the LB preserved the requested size, quality and PNG format. The proxy does not
resample provider output. Tool output arrived in output-item events rather than
the empty terminal output array; a terminal-only test observer is insufficient.
This check confirms image delivery, not independent verification of image-tool
pricing beyond the reported parent usage.

Real native Chat checks then found two conversion defects: non-streaming text
was discarded when terminal output was empty, and streaming usage decoded
snake_case provider fields into a camelCase dashboard struct. Non-streaming
subscription replies now retain bounded completed items in index order, reusing
compact's assembly; existing terminal output remains authoritative and ordinary
streams are not buffered. Chat usage explicitly maps the wire fields. The final
Luna check passed JSON Chat, SSE Chat and JSON Responses with the exact synthetic
reply: three requests, 87 tokens, 18 microdollars and no uncertain reserve.

An actual rebuilt binary was also exercised with a synthetic loopback source and
disposable data: a second process was denied ownership of the same SQLite file;
SIGKILL left a 35-token hold that startup marked uncertain without releasing;
the next permitted request completed normally. During SIGTERM drain, an admitted
stream reached its terminal response inside the grace period. The final database
contained two finalized requests and the one original uncertain reservation, with
the exact held-plus-confirmed token total. No real account or working process was
used for this crash/drain test.

Quota continuity has a public-route HTTP and WebSocket regression using real
provider adapters and SQLite with a controlled upstream quota response. A
separate live-GLM CLI check injected one pre-acceptance quota refusal between
tool steps, using two isolated logical source identities backed by the same
authorized GLM credential; the replacement preserved the marker/tool output and
did not receive the previous response anchor. The CLI did not see the injected
quota error. This verifies the application switch with real model/tool traffic,
not natural exhaustion or independent balances of two live subscriptions.

An isolated browser instance verified pasted auth JSON, multi-group account
membership, group-to-key limit propagation, detachment retaining the last limit,
common admin/key sign-in, read-only reports and loss of the in-memory key on
reload. A settled synthetic unpriced historical request was repriced through
the actual price editor from zero to 14,750 microdollars; its request/reservation
costs agreed and its 11,000 token charge stayed unchanged. Browser credentials
and data used only the disposable test instance, with real-provider egress
blocked for that process. The working local service was not replaced.

Final local validation on 2026-09-28 passed the complete Go suite, vet, the full
race suite before the last conversion repair plus focused race regressions for
that repair, all 1245 frontend tests, frontend lint and ten dashboard contract
tests against the freshly rebuilt binary. The first frontend run exposed a stale
mock-endpoint inventory for model prices and runtime connect address; updating
those two entries restored the full run without weakening its assertions.

The current implementation and E2E verification pass is complete within these
stated scopes. Real-provider checks complement local integration/security,
migration and fault tests; they do not certify every future provider behavior,
an unseen production database or VPS capacity. Natural exhaustion of independent
subscriptions and live legacy v1/v2 media were not exercised. Production
migration/deployment is a separate authorized operation, and real limit-reset
operations were excluded. The test PNG's remote deletion remains unconfirmed.

`scripts/compare-runtime.py` compares identical JSON, paced SSE and cancellation
loads against preserved runtimes. The initial rewrite measurements below showed
lower Go RSS/CPU but a streaming-latency regression. Subsequent concurrency work
separates committed readers from the durable writer and groups waiting short
writes without weakening WAL/FULL or acknowledging before commit. Cancellation
and descriptor cleanup are checked independently. Re-measure on the deployment
machine rather than extrapolating offline numbers to provider latency.

Final V32 build, 2026-09-27: three alternating runs per workload, 128 requests,
concurrency eight. Median JSON RSS is 25.25 MiB for Go versus 220.73 MiB for legacy;
CPU is 310 ms versus 1060 ms, and request p50 is 99.35 ms versus 115.03 ms. For paced
SSE, RSS is 26.14 MiB versus 222.78 MiB and CPU 410 ms versus 1340 ms. SSE first-token
p50 is 36.63 ms versus 13.86 ms; completion p50 is 156.69 ms versus 114.35 ms. Fresh
readiness is about 76–102 ms versus 6.5–9 seconds, excluding real-provider initialization.
Cancellation reaches the stub in about 40 ms for both runtimes; Go's streaming
and cancellation descriptor count stays at eleven across each measured batch.

## Independent logical-thread account selection

For requests carrying Thread-Id, explicit prompt-cache affinity includes both
Session_id and Thread-Id inside its existing API-key-scoped hash. For example,
six subagents sharing a process session and prompt-cache key can independently
select their initial accounts rather than inheriting the parent's cache pin.
HTTP, WebSocket and compact use the same classifier. The cache key sent to the
provider is unchanged, and repeated requests within the same scope retain TTL
and cache locality. Clients without Thread-Id keep their previous explicit-cache
behavior, including when automatic Sticky threads are disabled.

New named threads do not read old key-wide cache pins as a fallback. Those rows
need not be deleted or migrated; their existing TTL and anonymous-client use
remain intact. Confirmed response/session/turn/file ownership is resolved before
cache affinity, so already-running threads are not forcibly moved. New cache
keys do not authorize migration, and zero-quota owners still wait for a real
provider quota refusal. An identical session/thread identity is not guessed to
be a new subagent from a role name or changing turn metadata.

Capacity weighted remains probabilistic, not a promise of equal request counts.
Manual burn/preserve policy, earlier-reset preference, model/key/group scope and
account capacity can narrow the eligible pool independently of cache affinity.
No new setting, database schema or client configuration is required. Independent
selection can trade some cross-thread cache reuse for distributing new work.

## WebSocket liveness during long reasoning

Native Codex watches application text frames, not merely protocol ping/pong.
The response prelude intentionally withholds created/in_progress events until
an attempt can safely become visible, so a live but quiet upstream previously
left the Go client connection silent. Native turns now send a ten-second
`codex.keepalive`, or `response.in_progress` with the current real response ID.
A passive observer learns that ID before buffering and clears it between attempts.
These transport messages do not flow back through provider emit, metrics,
diagnostics, billing or retry classification. Generic v1 routes are unchanged.

One writer sequences heartbeats with real response events and never sends a
heartbeat after the terminal. Cancellation/disconnect cancels and joins the
worker; writes retain their thirty-second bound. An actual quota refusal can
still discard its buffered prelude and safely choose another account after a
heartbeat, without charging the refused attempt or carrying its ID forward.

Active upstream WebSockets separately send protocol pings every thirty seconds,
with ten seconds to answer pong. The existing reader continues receiving real
events concurrently. A failed probe closes that connection without replay or
owner migration. Probe goroutines stop and are joined, including when a terminal
arrives while pong is pending. Pong is transport liveness, not model progress:
the existing overall response deadline (two hours by default) is not extended.
For example, long Astra reasoning may complete after more than twenty minutes;
an arbitrary five-minute application-silence cutoff would cancel valid work.

The October 8 investigation also found truncated upstream streams after real
reasoning/tool events. This fix does not claim to eliminate provider/network
disconnects. Once a generation may have executed, its unknown usage remains
reserved for reconciliation and automatic replay is unsafe. Local peers verify
liveness, failure classification and cleanup; no production soak or paid E2E
traffic is implied by those tests. No schema, deployment or client setting change
is required.

## Codex interruption of an active response

Codex sends `response.interrupt` with the active `response_id` and
`mode: discard_partial_items` when new user input interrupts generation. Rejecting
that control as an unknown event broke an ordinary interaction. The Go runtime
now relays it to the exact ChatGPT WebSocket already executing the response.
It does not substitute a local cancellation or invent an accepted acknowledgement.
An explicit subsequent `response.create` continues on the retained owner; no
successor request or account migration is generated by the interruption itself.

For example, `response.interrupt` for `resp-1` can receive
`response.interrupt.accepted`, `response.output_item.interrupted`, then
`response.incomplete` with `reason: interrupted` and actual usage. Those upstream
events pass through, and the existing ledger settles the response once. A foreign
or late ID receives a structured `response.interrupt.failed` without cancelling
another response. External model sources do not advertise this private Codex
control protocol and receive no interruption frame.

If terminal usage arrives before the acknowledgement, the socket stays leased
for at most five more seconds while waiting for the matching acknowledgement.
Missing or invalid acknowledgement retires only that socket and retains known
usage. A queued but unsent interruption is rejected before releasing the socket,
so the next request cannot inherit it. Existing context cancellation and
`response.cancel` cleanup are unchanged. No database or client configuration
change is needed. Loopback route/provider and upstream race tests cover the
contract; real ChatGPT traffic was not used to validate this change.

## Concurrent streaming and memory sizing

The global active-stream limit, per-host HTTP connection limit and live upstream
WebSocket session limit are 512 as of go-v1.0.8. The request queue remains bounded at 128 with a
15-second timeout; account/source capacity and fair-share controls remain in
force. The subscription test uses 40 eligible accounts rather than pretending
that one account's quota/capacity can serve every client.

Independent full-context Responses sharing the same logical session use separate
exclusive upstream connections when the matching connection is busy. For example,
four subagents sharing Session_id and Thread-Id can run four generations at once
without mixing events. Sequential previous-response work still reuses its exact
connection; a request with previous_response_id does not fall back to an arbitrary
session sibling. The 512 upstream bound counts physical connections, not logical
sessions, and only idle connections may be evicted.

An exact continuation still has a bounded wait when its own socket is busy. If
that socket closes or its retained response changes while waiting, the proxy
detects the loss before sending response.create. Existing safe same-account
history reconstruction can recover the request; opaque or incomplete tool history
cannot be fabricated, and an already dispatched request is never blindly retried.
Capacity is not quota proof and cannot move an active thread to another account.

Client WebSockets have a separate 4096-connection budget using the existing
global semaphore, with no new lower per-key socket ceiling. For example, 256
chats with ten connections each fit within the pool; the exact client/Guardian
pool size is not assumed. Slots are acquired only after auth and released after
failed upgrades/disconnect/drain. Existing key/account generation controls and
120-second idle cleanup remain unchanged.

Idle readers still occupy neither request-body budget nor generation admission.
To keep a larger empty-connection pool from multiplying queued body memory, each
reader first accepts only a 4 KiB prefix (plus one size-detection byte). Larger
messages are charged by bytes to the request-body budget shared with every HTTP
proxy route (releases up to go-v1.1.1 counted 1280 permits instead),
allowing the current and one staged next message through reading, queueing and
request handling. The existing 32 MiB per-message validation remains. Short
controls can still cancel work when the budget is full. Budget exhaustion
discards only the excess message under that same size limit and reports local
capacity without closing the socket or cancelling its active turn. Failed reads
and oversized messages still close the connection. This pressure is not provider
quota proof and cannot authorize replay. The budget counts body bytes, not total
RSS: buffer growth, parsing, history and outputs have additional
workload-dependent costs. Active-generation and upstream-socket limits are
unchanged. Large contexts still require the sizing checks below.

An October 9 isolated run of the socket-capacity change held 4096 idle native
WebSockets on one synthetic key beside 256 HTTP/SSE Responses streams,
each with a 32 KiB message-array input and 1000 paced output chunks. Only the
server process was pinned to one CPU. Idle RSS was 258.84 MiB; peak RSS was
419.16 MiB. All 256 upstream streams overlapped for 20.42 seconds, using 22.77%
of one CPU during that overlap. TTFT p50/p95/p99 was 1156/1336/1343 ms. Errors
were zero, returned content and key accounting matched, all idle sockets stayed
usable, and server descriptors returned from a peak of 4621 to the baseline 13
after closing clients. Idle CPU was 0.67% over the three-second observation;
short snapshots are not an indefinite idle-CPU guarantee. Go retained about 420 MiB RSS after
disconnect, so descriptor cleanup is not immediate memory return to the OS.
This is a loopback SSE load with idle WebSockets, not a measurement of 256 active
native WebSocket generations, paid provider traffic, TLS, or a VPS latency SLA.
That early SSE fixture reused a response ID; subsequent native trials use a
unique response ID per request so independent continuation state is exercised.

The administrator also requested a 512-generation experiment. An ignored local
Go build overlay raises application admission, per-host HTTP connections and
upstream WebSocket sessions to 512 for the initial experiment. The administrator
subsequently approved promoting those same defaults into go-v1.0.8. The existing harness supports native WebSockets on both client and
stub-provider legs, with 3584 additional idle clients, four metered keys, unique
response IDs and a single CPU assigned to the server. The input sizes below are
bytes of text per request, not token counts or a guarantee for other workloads.

At 32 KiB input, 512 native generations overlapped for 20.85 seconds with zero
errors and verified content/accounting: peak RSS 450.30 MiB, steady CPU 52.56%,
TTFT p50/p95/p99 2176/2479/2503 ms. At 256 KiB, all 512 also completed correctly
and overlapped for 20.95 seconds, but peak RSS was 748.86 MiB (about 785 MB),
exceeding the requested budget; steady CPU was 51.16%, with TTFT
10325/12026/12105 ms. Idle downstream sockets remained usable. The 512 upstream
sockets retained after completion are intentional continuation state, not leaked
client connections; the harness accounts for them separately and verifies their
closure when the temporary runtime stops. Half of one CPU under this synthetic
streaming rate is not negligible usage for a 1-vCPU deployment.

The 1 MiB-input burst did not pass: with corrected control-frame handling and
a 60-second benchmark client connection timeout, only 508 of the requested 512
streams reached the overlap barrier. Four requests returned connection_error;
the other 508 then returned the fixture's benchmark_overlap failure after its
90-second gate, so they are not 508 independent provider failures. Peak server
RSS was 2898.31 MiB (about 3.04 GB). No success latency/accounting claim is made
for that failed trial; the exact cause of the four connection failures was not
localized. Earlier large-input trials were discarded because the stub did not
answer Ping during streaming, and its first control pump used select(), which
cannot handle descriptor numbers over 1023. The corrected poll()-based stub
passed a separate 45-second native streaming test and cancellation checks.
The worst-case trial does not establish a 500–600 MB deployment guarantee for
large inputs. Promotion to 512 was subsequently authorized after the mixed trials
below; the known large-context limitation remains documented.

The follow-up mixed-input experiment addresses that worst-case limitation rather
than treating 512 simultaneous 1 MiB uploads as ordinary traffic. On October 9,
two isolated native-WS trials used 410 requests of 32 KiB, 92 of 256 KiB and 10 of
1 MiB (approximately 80/18/2%), shuffled with seed 0. Starts were spread over
20 seconds; each response emitted 2250 paced chunks (about 45 seconds). There
was no overlap barrier: requests started and completed progressively. The same
experimental 512-capacity binary, one pinned server CPU, four metered keys and
3584 additional idle WebSockets were used, without GOMEMLIMIT or service changes.
This profile is an explicit test assumption, not measured production traffic.

Both runs reached 512 genuinely active generations, overlapping for 27.42 and
27.36 seconds, with zero errors and verified content/key accounting. Peak RSS
was 472.55 and 462.14 MiB (496 and 485 MB), within the 500–600 MB budget for this
profile. Steady forwarding used 52.73% and 52.45% of one CPU at approximately
50 events per second per stream. TTFT p50 was 21.10/21.38 ms, p95 163.67/169.24 ms,
and p99 262.54/238.40 ms, measured from each actual request start, excluding its
scheduled ramp delay. The 1 MiB subgroup's p95 was 380.53/324.92 ms. All standby
sockets survived; active streams returned to zero and retained upstream sockets
closed on temporary-runtime shutdown. CPU usage is still material on a 1-vCPU
host. Two short local trials do not establish a production soak-test result,
real OpenAI account capacity, TLS costs or a universal memory guarantee. The
go-v1.0.8 generation default is 512 following explicit administrator approval;
neither the experiment nor release publication deploys a running installation.

Reproduce the mixed profile using `scripts/compare-runtime.py` with the same
experimental binary, `--runs 2 --requests 512 --concurrency 512
--require-active 512 --workload stream --protocol responses
--upstream-protocol responses --client-websocket --input-array
--prompt-mix 32768:410,262144:92,1048576:10 --ramp-seconds 20 --keys 4
--idle-websockets 3584 --metered --stream-chunks 2250 --single-cpu`.

The actual static go-v1.0.8 amd64 release binary repeated that mixed workload:
512 active for 27.49 seconds, zero errors, peak RSS 466.04 MiB (489 MB), steady
CPU 53.44%, TTFT p50/p95/p99 23.77/160.58/252.34 ms and verified content/usage.
The harness starts versioned binaries with `--self-update=false` so process
measurements include the serving worker instead of only its update launcher;
this affects temporary tests only, not shipped defaults or running services.

The optimized short-request comparison at 32 clients lowers median TTFT from
434 to 68 ms against the preserved equally durable Go baseline. Larger inputs
need a separate capacity check: on one pinned CPU, 256 message-array Responses
streams emitting 50 events/s each remained active for over 20 seconds with
approximately 95 MiB RSS for tiny inputs, 147 MiB for 32 KiB inputs, and 368 MiB
for 256 KiB inputs. Steady forwarding consumed about 21–24% of that CPU in the
synthetic workload. All keys were accounted correctly, and no upstream streams
remained afterwards.

At 1 MiB per request, 256 simultaneously submitted contexts peaked around
1.1–1.3 GiB, down from about 2.1 GiB before context-copy optimization. That burst
still takes substantial parsing/admission CPU: TTFT was about 16–18 seconds,
not the small-request result. A 1 GiB VPS is not an appropriate promise for that
workload; validate a larger machine, starting around 2 GiB, if such bursts are
required. The administrator clarified the current service memory budget as
500–600 MB on October 9, replacing the earlier preferred 128 MiB target. Do not
count queued work as active streams. No memory-limit setting was forced into
the server/service: an undersized Go soft limit can cause GC pressure too.

Large payload fields share immutable validated wire storage; normalization of
duplicate/case-variant keys, security markers, key policies, replay and full
input validation remain enforced. SQLite already stores durable state on disk;
moving it into another file spool was not needed for this measured bottleneck.

For commands, comparison stages and limitations, see the
[concurrent-streaming change notes](../../changes/archive/2026-09-27-optimize-go-concurrent-streaming/notes.md).

## Host-adaptive capacity (go-v1.1.0)

Limits follow the host instead of fixed 1 GB constants. At startup the runtime reads effective GOMAXPROCS, the smallest cgroup memory limit along its cgroup path (or `MemTotal`) and the descriptor limit, then picks a memory tier: a 1 GB-class host keeps the measured 512 generations / 128 queue / 4096 client WebSockets, a 4 GB host gets 2048 / 512 / 16384, and SQLite readers grow with CPUs. The Go soft memory limit defaults to 75% of usable memory. Overrides: `--memory-limit`, `--max-generations`, `--max-client-websockets`; `GOMEMLIMIT` is honored. The plan is logged at startup and shown in Settings → Runtime capacity. Byte-weighted body budgets per route are the planned next guard for very large contexts. Details: [adapt-capacity-to-host](../../changes/archive/2026-10-28-adapt-capacity-to-host/context.md).

## Operational observability (go-v1.1.0)

Every response carries `X-Request-Id`; failures log one JSON line with the stable `error_code` (successes at `--log-level debug`). Background loops recover panics and log failures by class with deduplication. `--metrics-listen 127.0.0.1:9464` serves Prometheus metrics and pprof on a separate unauthenticated listener; the public listener returns 404 for `/metrics` and `/debug`. A replaced bootstrap binary is adopted in either direction, so an emergency downgrade by binary replacement works. Details and examples: [observe-runtime-operations](../../changes/archive/2026-10-28-observe-runtime-operations/context.md).

## Updates across schema versions (go-v1.0.11)

The updater accepts an equal or newer schema. The pre-switch backup is taken before the candidate starts; if a migrating candidate fails after starting, the backup replaces the database (its WAL discarded) before the previous version restarts. Rollback and boot recovery still require an equal schema, so after a migrating update the dashboard explains that returning to the older release needs that backup. See [allow-schema-migrating-updates](../../changes/archive/2026-10-28-allow-schema-migrating-updates/proposal.md).

## Online backups and data growth (go-v1.1.0)

A verified online snapshot (`VACUUM INTO`, `quick_check`, schema and key validation) is taken daily by default, three are kept in `<data-dir>/backups/`, and a backup is skipped when it would leave less than 5% free disk. `codex-lb backup --data-dir … --output …` works while the service runs. `encryption.key` is never included; keep it separately. Retention runs in bounded batches until it catches up; quota history keeps 45 days by default; financial `usage_events` are kept unless the operator sets a retention. The WAL is capped at 64 MiB and checkpointed every minute. Runbook: [storage-growth-backups](../../changes/archive/2026-10-28-storage-growth-backups/context.md).

## Dashboard trust model (go-v1.1.0)

No request is trusted for being "local". First setup needs a one-time bootstrap token (environment or `<data-dir>/dashboard-bootstrap-token`), keyless proxy access needs an explicit `--unauthenticated-client` network resolved through `--trusted-proxy`, Secure cookies and HSTS follow `--public-origin`, a configured TOTP is always required, sessions are revocable with a 12-hour idle timeout and a 30-day cap, and model sources may not reach private addresses without `--allow-private-model-sources`. Operator migration notes: [harden-dashboard-trust-and-update-integrity](../../changes/archive/2026-10-28-harden-dashboard-trust-and-update-integrity/context.md).

## Request-body memory budget

Request bodies on every proxy route — Responses over HTTP and WebSocket, Chat Completions, embeddings, compact, Codex control, files, transcription, realtime call creation and public warmup — share one budget counted in bytes, replacing the HTTP body-reader count and the WebSocket large-body permits (audit NET-14, SEC-4). A declared length is checked against the free budget (503) and the route maximum (413) before reading but not reserved; every body is charged as its bytes arrive and the charge lasts until the request ends. A body must keep arriving: 10 s without a byte, or 2 minutes in total, ends the request with 408 `request_body_timeout` and releases the charge. Past the route maximum a body gets 413 even when the budget is full. The budget is one eighth of sizing memory (128 MiB at exactly 1 GiB, about 120 MiB on a VPS reporting 960 MiB, ~486 MiB on a 4 GB VPS) with a 50% per-key share; `--body-memory-budget` and `--body-memory-key-share` override it. Exhaustion returns 503 `local_capacity_exceeded` with Retry-After. Measured peak RSS is about 6× the retained body bytes, so a full budget reaches roughly the 75% Go soft limit: a 200 × 4 MiB burst on the 1 GiB profile peaked at about 800 MiB instead of 2624 MiB, and a 400 × 4 MiB burst on the 4 GiB profile at about 3.1 GiB instead of 5244 MiB. At exactly 1 GiB the 512 × 256 KiB capacity profile still fits; a real 1 GB VPS admits about 480–491 of those 512 at once. The budget is per process: two overlapping workers during an update would each have a full budget. Details and tuning: [budget-request-body-memory](../../changes/archive/2026-10-28-budget-request-body-memory/context.md) and [bound-stalled-request-bodies](../../changes/archive/2026-10-28-bound-stalled-request-bodies/context.md).

## Image generation routes (go-v1.6.0)

Codex's built-in `$imagegen` posts JSON to `{base_url}/images/generations` and `/images/edits`. Behind codex-lb these are `/backend-api/codex/images/*`; OpenAI-compatible clients use `/v1/images/*`, where multipart edits are also accepted and converted to JSON data URLs.

Each request is forwarded unchanged to the ChatGPT Codex images endpoint of one subscription account, chosen by the shared balancer without model-catalog filtering. A `file_id` edit stays on the file's owner account. The key is billed from the response usage at the image model's token rates.

The image limit is separate from the account's quota (`x-codex-active-limit: imagegen_premium`, 1440 minutes on Pro), so image outcomes never change account health. A 429 image-limit refusal before output cools the account down for images for 10 minutes and moves the request to another account, trying at most four accounts.

Probes on 2026-10-29 showed that the subscription ignores `model`, `quality`, `size` and reasoning fields. It always returns 1254×1254 at `low`, and ChatGPT and Codex already use Images 2.5. Choosing GPT‑Image‑2.5 Sunburst or Flare, quality or size needs an OpenAI API source, which is a follow-up.

For Codex to show the tool, the provider needs `requires_openai_auth = true` with a ChatGPT sign-in, or `requires_openai_auth = false` with a non-empty `x-openai-actor-authorization` header. Details and probe evidence: [restore-image-generation-routes](../../changes/archive/2026-10-29-restore-image-generation-routes/context.md).

Since go-v1.7.0 the image routes relay `stream: true` answers as they are. A generation streams `keepalive` events and one `image_generation.completed` event with the image and usage, but no draft frames: the subscription ignores `partial_images`. An edit answers JSON. A final 429 carries the upstream reset headers. On Responses with the hosted `image_generation` tool, a 429 whose `X-Codex-Active-Limit` names an image limit is returned as 429 `usage_limit_reached` with those headers, without marking the account or replaying the request. Details: [image-streams-and-limit-headers](../../changes/archive/2026-10-30-image-streams-and-limit-headers/context.md).

## Grok video generation (go-v1.8.0, web sign-in go-v1.9.0, weekly pool go-v1.10.0)

Video generation is a separate function. Grok accounts form their own pool in `video_accounts`, separate from `accounts`, so no proxy route, report or key limit sees them. A job's cost is kept on its own row and consumes no API-key limit.

**Adding an account.** On the **Video generation** page press **Add account**, open xAI's page, check the code and approve with the Grok account. Keep the dialog open until it closes itself. Signing in to an account already in the pool renews its session, and an account in **Needs a new sign-in** comes back the same way. Deleting an account revokes its refresh token; an access token already issued stays valid until it expires, up to 6 hours. A sign-in lives in the worker's memory, so a sign-in in progress during an update must be started again.

**Using it.** Add the API key on the page; until then it gets 403 `video_access_denied`.

```bash
curl -sS -X POST https://<host>/video/v1/generations -H "Authorization: Bearer $CODEX_LB_API_KEY" -H 'Content-Type: application/json' -d '{"model":"grok-imagine-video-1.5","prompt":"The camera moves to the window.","image":{"url":"https://example.com/first.jpg"},"last_frame":{"url":"https://example.com/last.jpg"},"duration":8,"resolution":"720p"}'
```

The answer is 202 with an `id`. Poll `GET /video/v1/generations/{id}` until `status` is `done`, `failed`, `expired` or `unknown`; `done` carries `video.url`, `video.duration` and `cost_usd`. `GET /video/v1/generations` lists the key's jobs. Images are public URLs or base64 data URLs, and a request body is at most 32 MiB.

| model | text | first frame | last frame, keyframes, references |
|---|---|---|---|
| `grok-imagine-video` | yes | yes | no |
| `grok-imagine-video-1.5` | yes | yes | yes |
| `grok-imagine-video-1.5-lite` | yes | yes | no |

**Unfinished jobs (go-v1.10.2).** xAI documents `respect_moderation` on a finished video, so a moderated answer ends the job as `video_moderated` at once. A job older than two minutes logs `video job is still unfinished` once per due poll with xAI's HTTP status and status word, which separates a job xAI still holds from a read that fails. A job that gave no video answers `error_message`, xAI's own text when it gave one. The shared upstream transport waits up to three minutes for the first response byte; at 60 seconds it turned slow non-streaming image edits into 502 `upstream_unavailable`.

**xAI's answers (go-v1.10.3).** On prod xAI answered HTTP 400 to every status read of some jobs, and the runtime treated that as temporary and threw the body away, so they stayed `pending` for an hour. A 400 on a job older than two minutes now finishes it as `failed` with xAI's text; a younger one keeps polling in case the refusal passes. The job row keeps xAI's last answer (HTTP status, body up to 8 KiB, time) until it is pruned after 90 days, and the Video page shows it with `url` values hidden. Bodies stay out of logs, which carry only the status: information under two minutes, a warning after. The answer on the row is the last one, so an earlier status of the same job is only in the log.

**Moderation (go-v1.10.4).** xAI reports moderation on a status read: HTTP 400, code `imagine:content-moderated`, its text and `usage.cost_in_usd_ticks`, while finished videos answer `respect_moderation: true`. No job came back from it, so this code ends a job at once as `video_moderated`. Its reported cost is the same as for a finished video, so it now counts on the job and in the totals; whether it draws the subscription pool was not measured. xAI does not say which frame or text it rejected. A 400 without this code still waits two minutes.

**Weekly pool (go-v1.10.0).** The page shows what is left of each account's pool and when it resets, read right after sign-in and every 5 minutes from `cli-chat-proxy.grok.com/v1/billing?format=credits`. This route is undocumented and is what the official CLI uses, so a failed read keeps the last value and never blocks generation. It needs the scope `grok-cli:access`: an account signed in before go-v1.10.0 shows **Sign in again** and keeps generating until then. Selection prefers accounts with more left, in 10-point bands, and skips an account at 100% until its period ends, so it never spends on-demand credits by itself, even with a prepaid balance. When only exhausted or cooling accounts remain, the client gets 429 `video_pool_exhausted` with `Retry-After`.

**Failure handling.** A job is created on the least busy account. There is no local limit on unfinished jobs: a limit of four, chosen without measurement, blocked new jobs for an hour once four hung, while xAI refused bursts by itself and accepted new jobs alongside hung ones. A 401 after one refresh, a 403 or a 429 moves the request to another account, trying at most four. A 429 cools the account down by `Retry-After`, or one minute without it. A 403 sets **Refused by xAI** until the operator resumes the account. A server error or a lost connection gives 502 and is not replayed, because the video may have been generating. Every account refusing gives 429 `video_pool_exhausted` with `Retry-After`; no available account gives 503 `video_no_accounts`. A background task finishes jobs nobody polls; a job pending for an hour becomes `unknown`, and finished rows are deleted after 90 days.

**Limits.**
- Sign-in uses the official CLI's public OAuth client id, built into codex-lb, because xAI publishes no way to register one; xAI may stop or forbid it. The owner accepted this risk for their own accounts.
- The video URL is xAI's and can be read by anyone who has it. Its lifetime was not measured.
- The refusal at an exhausted limit was not observed; R10 of the spec follows the HTTP status.
- `cost_usd` is the cost xAI reports, not money charged to a subscription.

Probe evidence and design: [grok-video-generation](../../../../specs/grok-video-generation.md).
