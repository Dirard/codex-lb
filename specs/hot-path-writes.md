# Hot request path: fewer durable commits and JSON passes, account-bound replay fixes

- Last edited with skill pack: `0.2.2`

## Title and scope

Cut the time a proxied Responses turn spends before dispatch and between the upstream terminal and the client terminal: one durable write before dispatch (reservation with its affinity hint), one after the terminal (settlement, account outcome, continuation and aliases), every durable write through the batch writer without five-second starvation, one selection snapshot and fewer full JSON passes. Fix two ownership bugs on the same path: a provider-side `conversation` silently stripped during quota failover (APP-5) and the compact replay losing its Responses Lite flag (APP-11).

## Planning anchor

- Entrypoints: `internal/application/proxy.go#Proxy.Respond`, `internal/application/response_ownership.go#{resolveOwner,accountCandidates,remember}`, `internal/application/codex_compact.go#CompactWithOptions`, `internal/adapters/sqlite/writer.go#batchWriter.commit`, `internal/adapters/sqlite/{ledger,affinity,continuation,account_outcomes,model_pricing}.go`.
- Source findings: `specs/audits/2026-10-28/01-proxy-application.md` (APP-1…7, APP-11, APP-14), `02-sqlite.md` (DB-1, DB-2, DB-7, DB-9, DB-11, DB-13, DB-18), `04-transport.md` (NET-4). Auditor prototypes: `.local/audit/scratch-proxy-app/` (overlay benchmarks `BenchmarkAuditPreDispatch*`, `verify_external_test.go`), `.local/audit/scratch-sqlite/`.
- Changed assumptions:
  - The pre-dispatch path commits twice (`ReserveUsage`, then `SaveAffinity`) and the post-terminal path four or five times (`SettleUsage`, `RecordAccountOutcome`, `SaveContinuation`, one or two `SaveSessionContinuation`); every commit is a WAL fsync under `synchronous=FULL`.
  - Continuation, capability, error-archive, resource-owner and most admin writes bypass the batch writer (`transact(ctx, s.db, …)`), and the batch writer starts its five-second deadline before it obtains the single writer connection.
  - A non-empty Responses `conversation` is not treated as account-bound, and `replayBody` deletes it.
- Non-local: application ports (`UsageLedger`, `Continuations`, `AccountQuotaMetadata`), the SQLite adapter, one schema migration, `go-runtime` OpenSpec requirements for affinity persistence, terminal durability, repricing atomicity and provider-state ownership.
- Impacted specs in `specs/`: `specs/engineering-baseline.md` — `reuse` (gates only).
- Impacted OpenSpec capability: `go-runtime` — `amend` through change `openspec/changes/hot-path-single-commit/`.

## Connected groups or observed existing logic

### Observed Existing Logic

1. Entry and orchestration: `Proxy.Respond` authenticates the key again (`GetAPIKey`), loads settings, parses the body (`parseResponse` → `decodeResponseObject`, `accountBoundItems`, `validateCompactTrigger`, `inputUsesResponsesLite`), admits, resolves the owner (`resolveOwner` decrypts and unmarshals the stored history on every `previous_response_id` turn), resolves file owners (`collectFileIDs`), looks up affinity, selects an account, then for each attempt reserves (`ReserveUsage`), saves affinity (`requestAffinity.save` → `SaveAffinity`), dispatches, settles, records the account outcome, remembers the continuation and only then emits the buffered terminal (`responseOutput.finish`).
2. Selection: `accountCandidates` calls `ResolvePrice` (production: `ModelPricingService.ResolveCodex` → `GetModelPriceOverride`), `ListAccountAdditionalQuotas`, `LoadAccountCreditStatus`, `ListAccountQuota`, `LoadAccountQuotaRefusalAt` per candidate account.
3. Persistence: `batchWriter` groups up to 16 waiting jobs per transaction with per-job `SAVEPOINT write_job`; the five-second context is created before `BeginTx`. `SaveAffinity` re-validates the reservation and key/account scope and CASes the binding on `version` using the reservation rowid. `saveContinuation` deletes expired rows, upserts, then loops `SELECT count(*), sum(length(context_encrypted))` and deletes one oldest row per iteration. `rememberConversationAliases` copies the owner's encrypted context into each `session:` alias row. `ApplyModelPriceChange` saves the override and reprices the whole model history in one direct transaction.
4. Replay: `replayBody` drops `previous_response_id`, `conversation` and `turn_state`; quota failover is allowed when `request.FilePinned` and the owner's `FilePinned` are false. `CompactWithOptions` recomputes `request.ResponsesLite` from `request.Input`, which `parseResponse` leaves nil.
5. Measured before (this host, i9-14900K, NVMe ext4 97 % full, file DB, audit harness, 20 accounts): pre-dispatch p50 2.68 ms at c=1, 12.4 ms at c=8 (2 KiB input), 41.3 ms at c=8 (256 KiB); 6.12 fsync per sequential request, 3.5 at c=8 (strace between markers).

## Use cases

### 1. Reserve the attempt and its soft affinity in one write job
selected account and observed affinity version --reserve budget and compare-and-set affinity in one batch-writer job--> durable reservation with current affinity --dispatch--> upstream attempt

Requirements:

  R1:
`ReserveUsage` SHALL accept an optional affinity compare-and-set and apply it in the same transaction as the reservation, only after the reservation and its key/account scope checks succeed, using the new reservation rowid as the binding version.

  R2:
A lost compare-and-set race SHALL leave the winning binding unchanged without failing the reservation; any other affinity write error SHALL roll back the reservation and fail the attempt before dispatch with `affinity_persistence_failed` (503), so no unused reservation remains.

  R3:
Responses and compact attempts SHALL no longer issue a separate affinity write before dispatch.

Files And Functions:
 - existing: internal/domain/models.go#ReservationRequest - optional `Affinity *AffinityCAS`
 - existing: internal/domain/affinity.go - `AffinityCAS`, `ErrAffinityPersistence`
 - existing: internal/adapters/sqlite/ledger.go#ReserveUsage - CAS via shared `saveAffinityTx`
 - existing: internal/adapters/sqlite/affinity.go#SaveAffinity - removed; CAS logic kept in `saveAffinityTx`
 - existing: internal/application/response_affinity.go#requestAffinity - `cas()` replaces `save()`
 - existing: internal/application/proxy.go#Respond, internal/application/codex_operations_accounting.go#billedAdmitted - pass the CAS with the reservation

Tests:
 - description: affinity committed with the reservation
   input: new request with a prompt-cache hint
   workflow: request --Respond--> one reservation job --> binding version equals reservation rowid
   expected outcome: `TestRequestAffinity*`, `affinity_*_test.go` pass; adapter test shows binding written only with a valid reservation
 - description: affinity write failure leaves no reservation
   input: trigger that aborts inserts into `affinity_bindings`
   workflow: ReserveUsage with affinity --SQL error--> rolled back
   expected outcome: error wraps `ErrAffinityPersistence`, `GetReservation` returns not found; route test returns 503 `affinity_persistence_failed` without provider calls

### 2. Finalize a Responses attempt with one durable commit before the terminal
upstream terminal buffered --build settlement, outcome, quota mark, continuation and aliases outside the writer--> finalization plan --one batch-writer job with nested savepoints--> durable settlement and ownership --emit terminal--> client sees the terminal

Requirements:

  R4:
After an attempt returns, the proxy SHALL persist settlement (or retained uncertain usage), the account outcome with any continuation quota mark, the response continuation and its session/turn aliases in one write job; marshal and encryption SHALL happen before the job.

  R5:
A settlement failure SHALL roll back the whole job. An outcome failure SHALL roll back only the outcome and skip the continuation. A continuation or alias failure SHALL roll back only the continuation and aliases. Settlement SHALL never be undone by a later step.

  R6:
The buffered terminal event SHALL be emitted only after the finalization job committed; if the outcome or the continuation failed, the client SHALL receive the error and not the terminal.

  R7:
Existing fences SHALL hold: settlement exactly once, outcome only for a settled or retained-and-quota reservation, alias upsert fenced by reservation rowid, quota mark ignored for an older reservation.

Files And Functions:
 - planned: internal/domain/finalization.go - `ResponseFinalization`, `ResponseFinalizationResult`
 - existing: internal/application/response_ports.go#Continuations - `FinalizeResponse`
 - existing: internal/application/proxy.go#Respond, #settle - plan-then-finalize; `finishOutput` helper
 - existing: internal/application/response_ownership.go#remember - returns the continuation plan instead of writing
 - existing: internal/application/conversation_identity.go#rememberConversationAliases - alias plan
 - planned: internal/adapters/sqlite/finalize.go#FinalizeResponse - one `s.write` with `SAVEPOINT finalize_outcome` and `SAVEPOINT finalize_continuation`
 - existing: internal/adapters/sqlite/{ledger,account_outcomes,continuation,native_usage_uncertain}.go - extracted `*Tx` helpers

Tests:
 - description: one commit after the terminal
   input: fresh streaming request with session id
   workflow: request --Respond--> FinalizeResponse once --> terminal emitted
   expected outcome: post-terminal fsync count 1 per request (strace harness); existing proxy suites pass
 - description: continuation failure keeps settlement
   input: finalization whose continuation save fails (owner changed)
   workflow: Respond --FinalizeResponse--> settlement committed, continuation rolled back
   expected outcome: reservation `finalized`, no continuation row, terminal not emitted, error returned
 - description: settlement failure publishes nothing
   input: store whose settlement fails
   workflow: Respond --FinalizeResponse error--> `usage_settlement_failed`
   expected outcome: no outcome, no continuation, no terminal

### 3. Route every durable write through the batch writer without starvation
mutation request --enqueue job--> batch writer waits for the writer connection without a deadline --five-second deadline covers only execution--> committed batch

Requirements:

  R8:
The batch writer SHALL obtain the writer connection before starting the five-second execution deadline, so waiting behind another transaction cannot fail queued jobs.

  R9:
Runtime mutations in the SQLite adapter SHALL use `s.write`; only migrations and the offline legacy import may open a direct writer transaction. Reads SHALL use the read pool or run inside the write job.

  R10:
Grouping SHALL accept up to 64 waiting jobs per commit.

Files And Functions:
 - existing: internal/adapters/sqlite/writer.go#batchWriter.commit, #writeBatchSize
 - existing: internal/adapters/sqlite/*.go - `transact(ctx, s.db, …)` and `s.db.ExecContext` call sites (reports_*.go stay with the data slice)

Tests:
 - description: wait does not count against the deadline
   input: direct writer transaction held longer than a shortened test deadline
   workflow: ReserveUsage queued --connection released--> executes
   expected outcome: reservation succeeds

### 3.1 Reprice model history in bounded chunks
price change --read-only provenance check--> accepted change --save override and pending marker--> saved price --reprice raw events and covered buckets in chunks of at most 500 per write job--> repriced history --clear pending marker--> done

Requirements:

  R11:
`ApplyModelPriceChange` SHALL check current cost-limit charge provenance on a read snapshot before writing and SHALL fail with `ErrConflict` without saving the tariff when provenance is missing.

  R12:
The override SHALL be saved in its own job; history SHALL be repriced in jobs of at most 500 events (raw) or 100 rollup buckets, each applying event costs and their aggregate and current-limit deltas atomically, so ledger jobs interleave between chunks.

  R13:
A pending marker SHALL record an unfinished reprice; the store SHALL resume pending reprices in the background after opening, and re-running a reprice SHALL be idempotent (unchanged costs produce zero deltas).

Files And Functions:
 - existing: internal/adapters/sqlite/model_pricing.go#ApplyModelPriceChange
 - existing: internal/adapters/sqlite/model_pricing_reprice.go, model_pricing_folded.go - chunked passes with keyset cursors
 - existing: internal/adapters/sqlite/store.go#Open, #Close - background resume lifecycle
 - existing: internal/adapters/sqlite/migrations.go - `model_reprice_pending` in schema 34

Tests:
 - description: ledger keeps working during a long reprice
   input: thousands of usage events for one model, concurrent reserve/settle loop
   workflow: ApplyModelPriceChange --chunks--> reserves interleave
   expected outcome: no reserve/settle error; some reserves complete before the reprice finishes; totals equal the sum of event costs
 - description: interrupted reprice resumes
   input: pending marker with partially repriced history
   workflow: Open --background resume--> marker cleared
   expected outcome: history fully repriced; existing `TestModelPricing*` pass

### 4. Keep provider-side conversation state on its account
request with non-empty `conversation` --parse marks it account-bound--> pinned request --quota refusal--> 409 `quota_failover_requires_context` without a second attempt

Requirements:

  R14:
A request whose top-level `conversation` is present and not `null` or an empty string SHALL be account-bound like a file-pinned request: it SHALL NOT be replayed to another account or rebuilt without `conversation`, and its continuation SHALL be stored as pinned.

Files And Functions:
 - existing: internal/application/response_request.go#parseResponse, #replayBody
 - planned: internal/application/conversation_pin_test.go - route-level regression

Tests:
 - description: conversation is not stripped on quota failover
   input: `{"conversation":"conv_123","input":"…","stream":true}`, first account refuses quota
   workflow: Respond --quota refusal--> replayUnavailable
   expected outcome: one provider attempt; error code `quota_failover_requires_context`

### 5. Preserve the Lite flag in compact quota replay
compact quota refusal --parse replay body--> request with Lite flag from the replayed input --select replacement with catalog policy--> Lite-aware selection

Requirements:

  R15:
Compact replay SHALL keep the `ResponsesLite` value computed by `parseResponse` for the replayed input, so a key-enforced non-Lite model is rejected with `responses_lite_model_mismatch`.

Files And Functions:
 - existing: internal/application/codex_compact.go#CompactWithOptions - remove the recomputation

Tests:
 - description: Lite replay keeps catalog enforcement
   input: owner history with `additional_tools`, key forcing a model declared `use_responses_lite:false`, first compact attempt refused for quota
   workflow: CompactWithOptions --replay--> selection
   expected outcome: 403 `responses_lite_model_mismatch`, one provider call

### 6. Read account selection state once per choice
eligible accounts --memoize ChatGPT price per model--> priced candidates --one read transaction for quotas, credits, refusal and additional quotas--> candidate snapshot --choose--> selected account

Requirements:

  R16:
Within one `accountCandidates` call the ChatGPT price SHALL be resolved once per model; external sources keep per-account prices.

  R17:
Quotas, credit status, quota-refusal time and additional quotas for all candidates SHALL be loaded from one consistent read transaction with a constant number of queries, producing the same eligibility as the per-account reads.

Files And Functions:
 - existing: internal/application/response_ownership.go#accountCandidates
 - existing: internal/application/ports.go#AccountQuotaMetadata - `AccountSelectionSnapshot`
 - planned: internal/adapters/sqlite/account_selection.go#AccountSelectionSnapshot

Tests:
 - description: snapshot equals per-account reads
   input: accounts with quotas, credits, refusals and additional quotas
   workflow: snapshot --compare--> per-account loaders
   expected outcome: identical values; `TestProxy*Quota*`, `TestSelectionStrategiesAndWeights` pass

### 7. Load continuation history lazily
owner resolved --history needed before dispatch only for a quota-refused owner--> dispatch --decrypt and parse in a request-owned goroutine--> history awaited before replay or remember

Requirements:

  R18:
The stored history SHALL be decrypted and parsed before dispatch only when the owner is quota-refused; otherwise in a goroutine started at dispatch that is always awaited before `Respond` returns.

  R19:
A history that cannot be decrypted or parsed SHALL make cross-account replay unavailable and SHALL store the next continuation without context instead of failing an already executed turn.

Files And Functions:
 - existing: internal/application/response_ownership.go#resolveOwner
 - planned: internal/application/response_history.go#lazyHistory

Tests:
 - description: replay and remember still see history
   input: existing replay and continuation tests under `-race`
   workflow: Respond --lazy history--> replay/remember
   expected outcome: `response_continuation_test.go`, quota failover tests pass; race detector clean

### 8. Skip generic decodes when no account-bound marker can be present
raw input bytes --byte prefilter for markers and escapes--> decode skipped unless a marker or escape is present --generic decode when required--> account-bound, file and transport decisions

Requirements:

  R20:
`accountBoundItems`, `collectFileIDs` and `autoTransportRequiresHTTP` SHALL return without a generic decode when the raw items contain none of their trigger substrings and no `\u` escape; results SHALL equal the generic decode for every input.

  R21:
`remember` SHALL derive `FilePinned` incrementally from the request, the new output and the previous owner instead of rescanning the whole history.

Files And Functions:
 - existing: internal/application/response_request.go#accountBoundItems
 - existing: internal/application/codex_operations.go#collectFileIDs
 - existing: internal/application/response_transport.go#autoTransportRequiresHTTP
 - planned: internal/application/json_prefilter.go, json_prefilter_test.go - prefilter and fuzz equivalence

Tests:
 - description: prefilter equivalence
   input: fuzz corpus including `\u`-escaped keys and values
   workflow: input --old and new detection--> compare
   expected outcome: identical results

### 9. Reference owner context from conversation aliases
owner and aliases saved in one job --aliases store a reference to the owner row--> deduplicated context --stats row maintained by triggers--> bounds checked in O(1)

Requirements:

  R22:
Session and turn-state alias rows SHALL reference the owner response row instead of copying its encrypted context; lookups SHALL return the owner's context.

  R23:
Record count and context bytes SHALL be maintained incrementally in the same transaction; eviction SHALL not scan the table per save.

Files And Functions:
 - existing: internal/adapters/sqlite/continuation.go#GetContinuation, #saveContinuationTx
 - existing: internal/adapters/sqlite/migrations.go - schema 34: `context_response_id`, `continuation_stats`, triggers, `model_reprice_pending`

Tests:
 - description: aliases do not duplicate context
   input: response with session and turn aliases
   workflow: remember --lookup alias-->
   expected outcome: alias row has no blob, lookup returns the owner's context; stats equal a full recount after inserts, updates, evictions and deletes; migration from 33 backfills the stats row

### 10. Isolate analytics reads and remove dead ledger APIs
dashboard analytics query --separate small read pool--> report without taking proxy read connections

Requirements:

  R24:
Dashboard reports and request-log listing SHALL use a separate read pool of at most 3 connections.

  R25:
`TouchReservation` and `ReleaseStaleReservations` SHALL be removed from the ledger port, adapter and tests.

Files And Functions:
 - existing: internal/adapters/sqlite/store.go#Open - `analyticsDB`
 - existing: internal/adapters/sqlite/reports_*.go - `s.analyticsDB` (mechanical, data slice informed)
 - existing: internal/application/ports.go#UsageLedger, internal/adapters/sqlite/ledger.go

## Implementation checklist

1. [ ] OpenSpec change `hot-path-single-commit` (affinity persistence, terminal durability, chunked repricing, provider conversation state) validated strictly.
2. [ ] APP-11 regression test, then fix (R15).
3. [ ] APP-5 route regression test, then fix (R14).
4. [ ] Writer deadline after connection, batch size 64, DB-2 conversions (R8–R10).
5. [ ] APP-1 reservation with affinity CAS (R1–R3).
6. [ ] APP-2 `FinalizeResponse` and plan-then-finalize in `Respond` (R4–R7).
7. [ ] DB-1 chunked repricing with pending marker and resume (R11–R13).
8. [ ] APP-7/DB-9 price memo and selection snapshot (R16, R17).
9. [ ] APP-4 lazy history (R18, R19).
10. [ ] APP-3a/APP-14 prefilters and incremental `FilePinned` (R20, R21).
11. [ ] APP-6/DB-11 alias references and stats (R22, R23) in schema 34.
12. [ ] DB-7 analytics pool, DB-13 removal (R24, R25).
13. [ ] Measure pre-dispatch p50 (c=1, c=8) and fsync per request after; record below. Run vet, lint, tests, race.

## Open questions

None blocking. DB-18 (TTL for durable locality kinds) conflicts with the current operator-purge scenario and is left for a separate decision.

## Decision log

- 2026-10-28: Branch `perf/hot-path-writes` starts from `chore/engineering-baseline` (the base that carries the audit, this spec format and the rewritten conventions); `main` lacks them.
- 2026-10-28: Affinity failure inside the combined reservation job fails the attempt before dispatch and leaves no reservation (instead of a settled unused reservation). Same transaction means the failure is a storage failure; failing closed keeps one write and no orphan reservation.
- 2026-10-28: A continuation failure after settlement is isolated with a nested savepoint; settlement is never rolled back by ownership persistence, and the client receives the error instead of the terminal.
- 2026-10-28: Repricing trades one atomic transaction for bounded chunks. The provenance check before saving keeps the observable "failed reconciliation changes nothing" behaviour for the data-driven conflict; a later chunk failure leaves already applied chunks internally consistent and the pending marker for an idempotent resume.
- 2026-10-28: Non-empty `conversation` reuses the file-pinned path (account-bound) instead of a new flag: every failover branch already honours it and the stored continuation inherits it.
- 2026-10-28: Compact finalization keeps its separate writes in this slice; only Responses uses `FinalizeResponse`.
- 2026-10-28: Baseline numbers (before): pre-dispatch p50 c=1 2.68 ms, c=8/2 KiB 12.4 ms, c=8/256 KiB 41.3 ms; fsync per request 6.12 (c=1), 3.5 (c=8).
