## ADDED Requirements

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

## MODIFIED Requirements

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
