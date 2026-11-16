## ADDED Requirements

### Requirement: API keys can have a request pool

An API key MAY have a request pool with `requestsPerMinute` from 1 to 10000 and `maxWaitSeconds` from 0 to 60, default 30 when omitted. The admin key API SHALL accept `requestPool: {requestsPerMinute, maxWaitSeconds}`, keep the current pool when the field is absent, remove it on `null`, refuse other values with 422 `invalid_request_pool`, and return `requestPool` in every key representation. A changed or removed pool SHALL apply to the key's next request without a restart. A key without a pool SHALL behave as before and get no pool headers.

A pool SHALL admit at most `requestsPerMinute` model requests in any 60-second interval; a slot SHALL free 60 seconds after the request that took it was admitted. A pooled model request SHALL take its slot after authentication and before its body is read, proxy admission, account selection, budget reservation and any upstream I/O. A request over the pool SHALL wait in arrival order. When the slot it would get frees no later than its arrival plus `maxWaitSeconds`, it SHALL wait and continue; otherwise it SHALL be refused at once with 429, `Retry-After` in whole seconds (at least 1) until the slot a new request would get frees, and the body `{"error":{"type":"requests","code":"rate_limit_exceeded","message":"... Please try again in Ns."}}`. A request whose client disconnects while it waits SHALL consume no slot. The wait SHALL hold no lock, transaction or body budget and SHALL be bounded by the maximum wait and the request context.

Every answer to a pooled request, success or failure, SHALL carry `x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests` (free slots after this request, 0 while requests queue) and `x-ratelimit-reset-requests` (time until every slot taken now is free again, as a Go duration such as `850ms` or `59.5s`); browser Realtime and Live call routes SHALL expose them to scripts.

Responses over HTTP, each `response.create` on the Responses WebSocket, compact, image generations and edits, chat completions, embeddings, transcriptions, Realtime and Live call creation, each app Realtime WebSocket session, video generation creation and `/v1/warmup` SHALL consume one slot each. WebSocket upgrades, `response.cancel`, `response.interrupt`, Realtime sideband sockets, client-secret creation, video status and listing, Codex control and file routes, `/v1/models`, key usage and report routes and every admin or dashboard route SHALL NOT. The Responses WebSocket upgrade SHALL carry the pool headers without taking a slot; a waiting `response.create` SHALL keep Codex clients alive with `codex.keepalive`, end without a slot on `response.cancel`, and a refused one SHALL get `{"type":"error","status":429,"error":{...},"headers":{"retry-after":...,"x-ratelimit-limit-requests":...,"x-ratelimit-remaining-requests":"0","x-ratelimit-reset-requests":...}}` with the connection kept open. Pools are kept per worker.

#### Scenario: A full pool frees a slot within the maximum wait
- **WHEN** a key with 120 requests per minute and a 30-second maximum wait took 120 slots during the last 45 seconds and sends one more request
- **THEN** the request waits until the oldest slot frees, at most 15 seconds, is served, and its answer carries `x-ratelimit-remaining-requests: 0`

#### Scenario: The slot would free after the maximum wait
- **WHEN** a key with 2 requests per minute and a 10-second maximum wait sends a third request right after two others
- **THEN** it receives 429 at once with `Retry-After: 60`, the `rate_limit_exceeded` body and the pool headers, and no account is selected

#### Scenario: A queued client disconnects
- **WHEN** a request waiting in a full pool is cancelled by its client
- **THEN** it consumes no slot and the next queued request gets the slot that frees first

#### Scenario: A key without a pool
- **WHEN** a key without a request pool sends any request
- **THEN** it is served as before and its answer carries no `x-ratelimit-*-requests` headers

#### Scenario: A WebSocket turn is refused
- **WHEN** a `response.create` arrives on a Responses WebSocket of a key whose full pool cannot free a slot within the maximum wait
- **THEN** the client receives an error frame with status 429, `rate_limit_exceeded` and the pool values in `headers`, and the next `response.create` on the same connection is served when a slot is free
