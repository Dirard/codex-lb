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

The dashboard retains password/TOTP access. Initial direct-loopback setup does
not require a bootstrap token; remote setup uses
`CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`. Trusted identity headers require explicitly
configured trusted socket peers. CSRF protection remains enabled, including
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
over HTTPS; Secure cookies follow the existing trusted-proxy policy. This page
does not replace deployment TLS or administrator authentication.

## Routing and accounting

Accounts may belong to multiple groups. A key selects one group and gets its
limit configuration with independent consumption. Leaving a group keeps the
last group limits; an empty scoped group does not mean unrestricted access.
The group picker therefore displays No accounts selected for an empty membership,
including its clear-selection menu item. All accounts remains the label for an
unrestricted direct API-key assignment or an all-account automation, not for an
empty group. For example, clearing a group's sole member leaves its keys scoped
to zero accounts; it does not broaden them to the installation's other accounts.

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

## Concurrent streaming and memory sizing

The global active-stream limit, per-host HTTP connection limit and live upstream
WebSocket session limit are 256. The request queue remains bounded at 128 with a
15-second timeout; account/source capacity and fair-share controls remain in
force. The subscription test uses 40 eligible accounts rather than pretending
that one account's quota/capacity can serve every client.

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
required. Do not turn the preferred 128 MiB budget into a hard process limit or
count queued work as active streams. No memory-limit setting was forced into
the server/service: an undersized Go soft limit can cause GC pressure too.

Large payload fields share immutable validated wire storage; normalization of
duplicate/case-variant keys, security markers, key policies, replay and full
input validation remain enforced. SQLite already stores durable state on disk;
moving it into another file spool was not needed for this measured bottleneck.

For commands, comparison stages and limitations, see the
[concurrent-streaming change notes](../../changes/archive/2026-09-27-optimize-go-concurrent-streaming/notes.md).
