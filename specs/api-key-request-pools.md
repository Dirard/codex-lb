# API key request pools and forks

- Last edited with skill pack: `0.2.2`

## Title and scope

An operator gives an API key a request pool: at most N model requests per minute, a request over the pool waits up to a maximum time for a slot and is otherwise refused with 429 and `Retry-After`, and every pooled answer reports the pool in `x-ratelimit-*` headers. A key holder can then fork the key through the public API: forks are child keys that never exceed their parent, share the parent's pool and report their own share of it.

The work ships in two ordered slices under this spec:

1. Request pools (use cases 1-4), OpenSpec change `add-api-key-request-pools` (archived as `openspec/changes/archive/2026-11-17-add-api-key-request-pools/`), release go-v1.14.0.
2. Key forks (use cases 5-8), OpenSpec change `add-api-key-forks`, release go-v1.15.0.

## Planning anchor

- Owner request, 2026-11-16: «добавить пул на ключ и возможность его конфигурировать, т.е. я хочу в 1 проекте указать для его ключа 120rpm, превышение = ожидание высвобождения до 30сек (при превышении ожидания 429 с заголовком Retry-After), в ответе заголовок с кол-вом текущих доступных запросов в рамках пула».
- Scope addition the same day: «также нужна возможность через апи создать/обновлять/удалять форк ключа, чтобы владелец ключа мог создавать дочерние ключи и управлять ими в рамках своих возможностей. также в ответе на запрос каждого форка ключа в заголовке показывать сколько запросов в минуту из общего ограничения занимает этот форк».
- Entrypoints:
  - existing `internal/adapters/httpapi/proxy_auth.go#authenticateProxyKey`, `authenticateBearerKey`, `authenticateKeyValue` (every proxy route loads the key from SQLite on each request, so key settings already apply without restart);
  - existing route handlers `ProxyHandler.responses`, `ProxyHandler.websocket`, `CodexOperationsHandler.{compact,images,transcribeBackend,transcribeV1,createRealtimeCall,createAppRealtimeCall,createAppLiveCall,realtimeSession,publicWarmup}`, `NativeAPIHandler.{chat,embeddings}`, `VideoHandler.create`;
  - existing `internal/adapters/httpapi/keys.go#Server.saveKey` (admin key API, `PUT/POST /api/api-keys`);
  - existing `internal/adapters/sqlite/keys.go` and `ledger.go#Store.ReserveUsage`.
- Existing limits (`api_key_limits`, `domain.LimitRule`) are budgets settled by the ledger. The pool is a separate request-rate gate in front of everything else and is never stored in the ledger.
- Precedent for headers: image limit refusals copy `Retry-After` and limit headers (`writeProxyError`); capacity refusals answer 429 with `Retry-After: 2`.
- Non-local: domain key model, application limiter, every model route, Responses WebSocket frames, SQLite migration, admin API, dashboard key editor and key page, OpenSpec `go-runtime`, release notes.
- Impacted specs in `specs/`: none amended; `request-body-budget.md` is reused (`reuse`): a waiting request reads no body, so it holds no body budget.

## Connected groups or observed existing logic

### Connected groups

1. Authentication and key settings
   - Every proxy handler authenticates first and receives `domain.APIKey` (or only its id in `CodexOperationsHandler.authenticate` and `VideoHandler.authorize`). The key is read from SQLite per request; a WebSocket re-authenticates before each `response.create`.
   - Realtime app routes may authenticate with a client secret that resolves to a key id (`ResolveRealtimeClientSecret`).
2. Route order
   - Handlers read and charge the body (`BodyBudget`), then the application use case does capability checks, proxy admission (`MaxStreams`/`MaxQueued`), account selection, reservation and dispatch.
3. Errors
   - `writeProxyError` maps application errors to OpenAI-style bodies; `sendWebSocketError` writes `{"type":"error","status":N,"error":{...}}` on the Responses WebSocket. Codex parses that wrapped error, including an optional `headers` object.
4. Storage
   - `api_keys` columns are listed explicitly in `keyColumns`; `SaveAPIKey` upserts only the columns it lists, so older code keeps columns it does not know. Migrations 41-49 are additive.
5. Ledger (slice 2)
   - `ReserveUsage` loads the key's active `api_key_limits` rows, increments them and records one `usage_reservation_items` row per limit; settlement adjusts exactly those items. Account scope comes from the key row (`group_id`, assignment flags, `api_key_accounts`, `api_key_sources`).
6. Dashboard
   - `web/src/features/api-keys` (create and edit dialogs, zod schemas) and `web/src/features/key-page` (properties section, built from `application/key_page.go`).

## Use cases

### 1. Configure a key's request pool

operator edits a key --validate requests per minute and maximum wait--> pool setting --store with the key--> pooled key --next request reads the key--> new pool applies without restart

Requirements:

  R1:
A key MAY have a request pool with `requestsPerMinute` from 1 to 10000 and `maxWaitSeconds` from 0 to 60, default 30 when omitted. The admin key API (`POST /api/api-keys`, `PATCH /api/api-keys/{id}`) SHALL accept `requestPool: {requestsPerMinute, maxWaitSeconds}` and `requestPool: null` (remove), keep the current pool when the field is absent, refuse other values with 422 `invalid_request_pool`, and return `requestPool` (or null) in every key representation, including the key page.

  R2:
A changed or removed pool SHALL apply to the next request of that key without a restart. Requests already admitted keep their slot; requests already waiting are re-evaluated against the new limit.

Logic Details:
- Storage: two nullable columns on `api_keys` (`request_pool_rpm`, `request_pool_max_wait_seconds`), migration 50, additive: older code neither reads nor writes them and its key upsert leaves them untouched, and `ALTER TABLE ADD COLUMN` rewrites nothing.
- The pool belongs to the key, not to a group; group membership does not change it.

Files And Functions:
 - planned: internal/domain/request_pool.go#RequestPool, RequestPool.Validate
 - existing: internal/domain/models.go#APIKey - field `RequestPool`
 - existing: internal/adapters/sqlite/keys.go#scanKey, Store.SaveAPIKey
 - planned: internal/adapters/sqlite/request_pool_schema.go#schemaV50
 - existing: internal/adapters/httpapi/keys.go#Server.saveKey
 - existing: internal/application/key_page.go - `requestPool` on the key page
 - existing: web/src/features/api-keys/components/api-key-create-dialog.tsx, api-key-edit-dialog.tsx, web/src/features/key-page/components/key-properties-section.tsx

Tests:
internal/adapters/sqlite/request_pool_test.go
 - description: a pool round-trips; null removes it; out-of-range values are refused; migration from 49 leaves every key unpooled and records version 50
   requirements: R1
internal/adapters/httpapi/request_pool_test.go
 - description: the admin API sets, keeps when absent, removes with null and refuses invalid pools with 422; a pool change applies to the next proxied request
   requirements: R1, R2
web/src/features/api-keys/components/request-pool-editor.test.tsx
 - description: the create and edit dialogs send, keep and clear the pool and refuse out-of-range values; the key page shows it (`key-page.test.tsx`)
   requirements: R1
web/src/__integration__/go-runtime.test.ts
 - description: against the built binary, a pool round-trips through the admin API and the key page, a model request carries the headers, the next one is refused with Retry-After, and a cleared pool removes the headers
   requirements: R1, R2, R6

### 2. Admit a pooled model request

pooled model request --authenticate the key--> key with pool --count admissions in the last minute--> free slot or full pool --take a free slot at once, otherwise join the key's queue--> queued request --wait until a slot frees or the maximum wait ends--> admitted request or refusal --continue with body, accounts, reservation and dispatch--> served request

Requirements:

  R3:
A model request of a pooled key SHALL take one slot after authentication and before its body is read, before proxy admission, account selection, budget reservation and any upstream I/O. A pool admits at most `requestsPerMinute` requests in any 60-second interval; a slot frees 60 seconds after the request that took it was admitted. A key without a pool SHALL behave exactly as before and get no pool headers.

  R4:
A request over the pool SHALL wait for a slot in arrival order. When the slot it would get frees no later than its arrival plus `maxWaitSeconds`, it SHALL wait and then continue; otherwise it SHALL be refused at once (with `maxWaitSeconds: 0`, every request over the pool) with 429, `Retry-After` in whole seconds (at least 1) until the slot a new request would get frees, and the OpenAI-style body `{"error":{"type":"requests","code":"rate_limit_exceeded","message":"... Please try again in Ns."}}`.

  R5:
A request whose client disconnects while it waits SHALL leave the queue without consuming a slot; a slot it was granted in the same instant SHALL be returned to the pool. The wait SHALL hold no lock, SQLite transaction or body budget, and SHALL be bounded by `maxWaitSeconds` and the request context. A pool's queue never holds more than `requestsPerMinute` requests.

Logic Details:
- Algorithm: per pool a sliding-window log of admission times plus a FIFO queue. With the slots ordered by when they free, the queued request at position `p` gets the slot at index `admitted - limit + p`; when that index points past the current admissions, its slot would free only after a request not yet admitted, which is later than one window (≥ the maximum wait), so it is refused at once. The estimate is exact for one constraint, so refusing early refuses exactly the requests that could not be served in time.
- A waiter sleeps until its estimate or deadline; any change that can move it forward (an arrival with a raised limit, a waiter leaving) wakes the queue to recompute.
- In memory, one process: the limiter is a field of the runtime; during a seamless update the replacement worker starts with empty pools, so a key may get up to twice its pool in the minute of the handoff. Idle pools are swept once a minute.
- Waiting before the body read keeps the body budget free while requests queue; `RequestBodyTimeout` starts when the body is opened, after the wait.

Files And Functions:
 - planned: internal/application/request_pools.go#RequestPools, RequestPools.Admit, RequestPools.Status
 - planned: internal/application/request_pools.go#RequestPoolExhausted
 - planned: internal/adapters/httpapi/request_pool.go#requestPoolGate.admit
 - existing: internal/adapters/httpapi/proxy.go#writeProxyError - 429 shape and headers
 - existing: cmd/codex-lb/runtime.go - one `RequestPools` for every proxy handler

Tests:
internal/application/request_pools_test.go
 - description: with synctest: a full pool queues and admits when the oldest slot frees; FIFO order; a request whose slot frees after the maximum wait is refused at once with the right RetryAfter; zero wait refuses; a cancelled waiter leaves without a slot and the next waiter moves up; a raised limit admits waiters at once; a lowered limit delays them; the queue is bounded; idle pools are swept
   requirements: R3, R4, R5
internal/adapters/httpapi/request_pool_test.go
 - description: on `/backend-api/codex/responses` and `/v1/responses` a pooled key waits for a freed slot and is then served; a request over the maximum wait gets 429 with Retry-After, the rate_limit_exceeded body and no upstream call; a client disconnect while queued gives the next request the slot; a key without a pool is unchanged
   requirements: R3, R4, R5

### 3. Report the pool in every pooled answer

admitted or refused request --read the pool after this request--> limit, remaining and reset --write headers before the answer starts--> client sees the pool

Requirements:

  R6:
Every answer to a request of a pooled key on a pool-consuming route, success or failure, streamed or not, SHALL carry `x-ratelimit-limit-requests` (the pool's requests per minute), `x-ratelimit-remaining-requests` (slots free now after this request, 0 while requests queue) and `x-ratelimit-reset-requests` (time until every slot taken now is free again, as a Go duration such as `1s`, `850ms` or `59.5s`). A 429 refusal SHALL carry them too, with `Retry-After`. Browser-facing Realtime and Live call routes SHALL expose these headers to scripts through `Access-Control-Expose-Headers`.

Logic Details:
- The names and formats follow OpenAI's request rate-limit headers, so OpenAI clients and dashboards read them unchanged.

Files And Functions:
 - planned: internal/adapters/httpapi/request_pool.go#setRequestPoolHeaders
 - existing: internal/adapters/httpapi/realtime_apps.go#allowRealtimeCallOrigin

Tests:
internal/adapters/httpapi/request_pool_test.go
 - description: success, streamed success, upstream failure and refusal carry the three headers with the expected values
   requirements: R6
cmd/codex-lb/request_pool_routes_test.go
 - description: in the wired runtime every consuming route takes a slot and refuses the next request with Retry-After, the browser call routes expose the headers, and `/v1/models`, `/v1/usage`, files, Codex control, client secrets, video listing and sideband routes never take one
   requirements: R6, R7

### 4. Pool the Responses WebSocket and the other model routes

route request --classify the route--> consuming or free route --consume one slot per model request--> pooled route

Requirements:

  R7:
These requests SHALL consume one slot each: Responses over HTTP (`/v1/responses`, `/backend-api/codex/responses`), each `response.create` on the Responses WebSocket, compact, image generations and edits, chat completions, embeddings, transcriptions, Realtime and Live call creation, each app Realtime WebSocket session, video generation creation and `/v1/warmup`. These SHALL NOT: the WebSocket upgrade itself, `response.cancel` and `response.interrupt`, Realtime sideband sockets of an existing call, client-secret creation, video status and listing, Codex control routes, file routes, `/v1/models`, key usage and report routes, and every admin or dashboard route.

  R8:
On the Responses WebSocket the upgrade response SHALL carry the pool headers of that moment without taking a slot. Each `response.create` SHALL wait in the pool as an HTTP request does, while Codex clients keep receiving `codex.keepalive` frames; a `response.cancel` during the wait SHALL end it without consuming a slot. A refused `response.create` SHALL get the error frame `{"type":"error","status":429,"error":{"type":"requests","code":"rate_limit_exceeded","message":...},"headers":{"retry-after":"N","x-ratelimit-limit-requests":...,"x-ratelimit-remaining-requests":"0","x-ratelimit-reset-requests":...}}` and the connection SHALL stay open. Admitted turns carry no extra frame, because the Responses event stream has no rate-limit event.

Files And Functions:
 - existing: internal/adapters/httpapi/proxy_websocket.go#ProxyHandler.websocket
 - existing: internal/adapters/httpapi/proxy_websocket_stream.go#ProxyHandler.respondWebSocket - admission inside the turn worker
 - existing: internal/adapters/httpapi/proxy_websocket.go#sendWebSocketError - `headers` on pool refusals
 - existing: internal/adapters/httpapi/codex_operations.go, codex_images.go, native_api.go, video.go, realtime_apps.go, public_warmup.go

Tests:
internal/adapters/httpapi/request_pool_websocket_test.go
 - description: the upgrade carries the headers and takes no slot; a second response.create over a full pool with zero wait gets the 429 frame with headers and the socket serves the next turn; a response.cancel during a wait leaves no slot taken
   requirements: R7, R8

### 5. Manage forks with the parent key

key holder request with the parent key --authenticate a real, active, unexpired non-fork key--> parent --validate the fork against the parent--> narrower fork --store the fork linked to the parent and return its secret once--> fork --list, read, update or delete only the parent's own forks--> managed forks

Requirements:

  R9:
A key holder SHALL manage forks of their own key with that key as the bearer under `/v1/key/forks`: `GET` lists, `POST` creates (the answer carries the fork's secret once), `GET /{id}` reads, `PATCH /{id}` updates and `DELETE /{id}` deletes. A fork, keyless local access and a Realtime client secret SHALL NOT manage forks (403 `fork_management_forbidden`), and an id that is not one of the caller's forks SHALL answer 404. A parent SHALL have at most 100 forks.

  R10:
A fork SHALL never exceed its parent. At write time its allowed models, enforced model, allowed and enforced reasoning efforts, enforced service tier, expiry and request-pool cap SHALL be equal to or narrower than the parent's (422 `fork_exceeds_parent` names the field otherwise). Traffic class, transport policy, usage sections, group and account and source scope are inherited and cannot be set on a fork. Forks SHALL NOT have forks.

  R11:
Each fork records who created and last changed it (`owner` through the key API, `admin` through the dashboard) and when; management actions are logged with key ids only, never secrets. The administrator keeps full control of forks in the dashboard and admin API, bound by the same narrowing rules.

Logic Details:
- A fork is an `api_keys` row with `parent_key_id`. Its secret has the prefix `sk-clb-fork-` and is stored as the SHA-256 of a domain-separated value, so code that predates forks cannot authenticate a fork (fail closed) and migration 51 stays additive.

Files And Functions:
 - planned: internal/domain/key_forks.go#ValidateFork, EffectiveKey
 - planned: internal/application/key_forks.go#KeyForks
 - planned: internal/adapters/httpapi/key_forks.go
 - planned: internal/adapters/sqlite/key_forks.go, key_forks_schema.go#schemaV51

Tests:
internal/adapters/httpapi/key_forks_test.go
 - description: create, list, read, update and delete through `/v1/key/forks`; the secret is shown once; a fork, another key's fork id and keyless access are refused; a wider fork is refused with the field name; the fork count is bounded
   requirements: R9, R10, R11

### 6. Serve a fork within its parent

fork request --authenticate the fork and its parent--> effective key --intersect the fork's settings with the parent's current settings--> narrowed request --reserve on the fork's and the parent's limits--> fork usage counts on the parent

Requirements:

  R12:
A fork's requests SHALL be served with the intersection of the fork's and the parent's current settings and the parent's account and source scope. They SHALL be refused as an invalid key when the parent is inactive, expired or deleted. Deleting a parent SHALL delete its forks.

  R13:
A fork's reservation SHALL take budget on the fork's own limits and on every active limit of its parent in the same transaction, and settlement SHALL settle both exactly once, so a fork's usage counts against the parent.

Files And Functions:
 - existing: internal/adapters/httpapi/proxy_auth.go#authenticateKeyValue
 - existing: internal/adapters/sqlite/ledger.go#Store.ReserveUsage
 - existing: internal/adapters/sqlite/keys.go#Store.eligibleAccounts, Store.ScopedAccountForOwner, Store.DeleteAPIKey

Tests:
internal/adapters/httpapi/key_forks_test.go
 - description: a fork narrower in models is refused a parent-allowed model; a parent narrowed after the fork was created narrows the fork; a deactivated parent disables the fork; a fork request charges the parent's cost limit and a full parent limit refuses the fork
   requirements: R12, R13

### 7. Share the parent's pool with its forks

fork request --use the parent's pool when it has one--> shared pool --apply the fork's own cap first--> fork slot --take a shared slot within the same deadline--> admitted fork request --count the fork's admissions in the shared window--> fork share

Requirements:

  R14:
A parent's request pool SHALL be shared by the parent and all its forks. A fork MAY have its own `requestsPerMinute` cap, at most the parent's pool; a fork request then needs a slot under its cap and a shared slot, both within the parent's maximum wait from its arrival, and gives the cap slot back when it gets no shared slot. When the parent has no pool, a fork's cap is its own pool with its own maximum wait.

  R15:
Every answer to a fork's request SHALL carry, next to the pool headers of R6, `x-ratelimit-fork-used-requests` (this fork's requests in the shared pool during the last minute, this one included) and, when the fork has a cap, `x-ratelimit-fork-limit-requests`. `x-ratelimit-remaining-requests` SHALL be the smaller of the shared pool's and the fork cap's free slots.

Files And Functions:
 - existing: internal/application/request_pools.go#RequestPools.Admit - members and caps

Tests:
internal/application/request_pools_test.go
 - description: parent and forks share one window; a capped fork waits for its cap without blocking other forks; a fork refused on the shared pool returns its cap slot; fork counts per member
   requirements: R14, R15
internal/adapters/httpapi/key_forks_test.go
 - description: two forks and the parent share 3 rpm; the fork headers report each fork's share
   requirements: R14, R15

### 8. Show forks in the dashboard

admin opens a key --load its forks or its parent--> key with family --show forks under the parent and the parent on a fork--> managed family

Requirements:

  R16:
The key page of a parent SHALL list its forks (name, prefix, status, created by, last used) with links, and the key page of a fork SHALL name its parent. The keys list SHALL mark forks with their parent.

Files And Functions:
 - existing: internal/application/key_page.go, web/src/features/key-page, web/src/features/keys

Tests:
web/src/features/key-page/components/key-forks-section.test.tsx
 - description: forks are listed under the parent and the parent is linked from a fork
   requirements: R16

## Implementation checklist

Slice 1 — request pools (go-v1.14.0)

1. [x] Write this spec and the OpenSpec change `add-api-key-request-pools`; validate.
2. [x] Domain `RequestPool`, `APIKey.RequestPool`; SQLite migration 50 (additive), scan and save; key page field (R1).
3. [x] Application `RequestPools`, keyed by pool id so forks can share a parent's pool; synctest unit tests (R3-R5).
4. [x] HTTP gate, headers and 429 body on every consuming route; admin API pool field; runtime wiring (R1, R2, R6, R7).
5. [x] Responses WebSocket admission per `response.create`, keepalive, cancel and refusal frame (R8).
6. [x] Dashboard: pool fields in create and edit dialogs, key page property, contract test (R1).
7. [ ] Route-level tests; `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`; release notes go-v1.14.0; context notes; sync and archive the change; PR, CI, merge, publish.

Slice 2 — key forks (go-v1.15.0)

8. [ ] OpenSpec change `add-api-key-forks`; refine use cases 5-8 against the code.
9. [ ] Migration 51, fork storage, domain-separated fork secrets, cascade delete (R9-R12).
10. [ ] Effective key at authentication and parent scope in SQL eligibility (R12).
11. [ ] Ledger reservation and settlement across fork and parent limits (R13).
12. [ ] Shared pools with fork caps and fork headers (R14, R15).
13. [ ] Fork management API and admin narrowing (R9-R11).
14. [ ] Dashboard forks (R16).
15. [ ] Gates, release notes go-v1.15.0, sync and archive, PR, CI, merge, publish.

Deferred:
- Sharing pools across workers of one installation (two workers overlap only during a seamless update).
- A rate-limit frame on admitted WebSocket turns: no Responses event exists for it.
- Token-per-minute pools.
- Regenerating a fork's secret through the key API (delete and recreate instead).

## Open questions

- non-blocking: Whether the owner wants pool waits shown in request logs (queue latency). The request log keeps its current timing, which starts after admission.

## Decision log

- 2026-11-16: Sliding-window log, not a token bucket: a bucket with a burst of N refills while it drains and admits up to 2N in a minute, while the owner asked for 120 per minute. A slot frees 60 seconds after it was taken, which is the owner's «ожидание высвобождения».
- 2026-11-16: Refuse at once when the slot would free after the maximum wait. With one queue in arrival order the estimate is exact, so waiting 30 seconds would only end in the same 429 later.
- 2026-11-16: The slot is taken before the body is read: a waiting request holds no body memory, and a refused one never uploads its body. Invalid requests therefore count, as at OpenAI.
- 2026-11-16: The maximum wait is capped at 60 seconds (one window), which also bounds each queue by the pool size.
- 2026-11-16: Header names `x-ratelimit-{limit,remaining,reset}-requests` follow OpenAI; the 429 body uses `code: rate_limit_exceeded` with "Please try again in Ns.", which Codex reads as the retry delay.
- 2026-11-16: Forks ship second, as their own PR and release, because they change authentication, the ledger and account scope; the limiter keys pools by pool id, so forks can share their parent's pool.
- 2026-11-17: Slice 1 validation: limiter unit tests under synctest, in-process route tests under synctest (the SQLite store and the provider stub run inside the bubble), WebSocket tests on a real connection, and a wiring test over every route of the runtime. Eight mutations (HTTP gate removed, no early refusal, cancelled waiter kept, WebSocket turn ungated, refusal frame without headers, no success headers, limit change ignored, compact ungated) each fail their test. Local gates green: `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`. `TestVideoTokensRefreshOnceAndRotate` failed once under the full parallel run; it fails on `main` too with `-cpu 1` (three concurrent requests against a proxy admitting one stream and one queued), so it is a pre-existing flake, not part of this change.
