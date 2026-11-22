# Sticky-only accounts keep their conversations when their limits run out

- Last edited with skill pack: `0.2.2`

## Title and scope

An account can be switched to sticky-only, the owner's «отключить балансировку для этого аккаунта». It still takes new conversations from balancing, but a conversation that started on it stays on it: when its limit runs out, codex-lb returns the quota error instead of replaying the conversation on another account. The option exists on every account kind: ChatGPT accounts, model-source accounts and the planned Antigravity accounts.

## Planning anchor

- Entrypoints:
  - existing `internal/application/proxy.go#Proxy.respond` quota branch: after an explicit quota refusal before visible output, it excludes the account and replays the stored history on another account;
  - existing `internal/application/proxy.go`: owner resolution, `owner.QuotaRefused` hydrated replay on the next request;
  - existing `internal/application/codex_compact.go#CodexOperations.Compact`: the same quota replay for compaction;
  - existing `PATCH /api/accounts/{id}` (`internal/adapters/httpapi/accounts.go#Server.updateAccount`).
- Changed assumption: an explicit quota refusal always authorizes moving the conversation to another account («Preserve active owner continuity and distinguish quota errors»). The owner clarified on 2026-11-22: «балансировка должна быть, но только в стики режиме, т.е. если чат начавшийся на одном аккаунте прервался на исчерпании лимитов - он не перескочит автоматически на другой». The option applies to every account.
- Non-local: domain account, SQLite schema and account reads, the proxy and compaction failover, the admin API, the dashboard account sheet and model-source dialog, OpenSpec `go-runtime`, and release notes.
- OpenSpec change `add-sticky-only-accounts`, archived as `openspec/changes/archive/2026-11-22-add-sticky-only-accounts/`: `go-runtime` ADDED «A sticky-only account keeps its conversations».
- Impacted specs in `specs/`:
  - `specs/voice-account-balancing.md`: voice calls already never move a pinned owner. Action: `reuse`.
  - No other solution spec describes quota replay. Action: none.

## Connected groups or observed existing logic

### Connected groups

1. Entry and orchestration
   - `Proxy.respond` loops over attempts. After settlement, an attempt with `quota && safeZero` excludes the account, re-reads candidates with `selectAccountAdmitted(..., owner=nil, previous=account, excluded)` and replays `replayBody(request, history)` on the next account.
   - The settlement plan sets `plan.Outcome.QuotaMark` when the refusing account is the conversation owner. `FinalizeResponse` then marks the continuation `quota_refused`, so the next request of the conversation enters the `owner.QuotaRefused` path: it builds the replay before dispatch and selects a different account.
   - `CodexOperations.Compact` marks the continuation `quota_refused` (`MarkContinuationQuotaRefused`) after a quota refusal and replays the compaction on another ChatGPT account. A continuation that was already marked skips the owner and replays at once (`alreadyRefused`).
2. Loaders and resolvers
   - `Proxy.respond` loads the owner's account (`pinned`) through `GetAccount` and checks its generation and route revision. `accountCandidates` uses the owner only while `!owner.QuotaRefused`.
   - `CodexOperations.accountForOwner` uses `strictOwnerAccount` for compaction.
3. Persistence
   - `accounts` has per-account policy columns (`routing_policy`, `limit_warmup_enabled`, `security_work_authorized`). Each has its own single-column update (`updateAccountPolicy`) so a stale snapshot cannot revert another writer.
   - The account reads in `internal/adapters/sqlite/accounts.go` list columns explicitly.
4. Response and propagation
   - The quota refusal reaches the client through `finishOutput(output, callErr)` when no replay happens, the same path used when replay is impossible.
   - `GET /api/accounts` and the account sheet show per-account policies. Model sources are edited in their own dialog and are `accounts` rows of kind `external`.
5. Validation
   - Proxy failover tests use `wireFixtureWithProxy` with two accounts and a provider stub that refuses the first with a quota error.
6. Specs
   - `openspec/specs/go-runtime/spec.md` «Preserve active owner continuity and distinguish quota errors» stays authoritative for every other account. The new requirement narrows it for sticky-only accounts.

## Use cases

### 1. Keep a sticky-only account's conversation on it

owned request on a sticky-only account --dispatch to the owner even after an earlier quota mark--> owner attempt --recognize the explicit quota refusal--> refused attempt --settle it uncharged without marking the conversation or replaying it--> quota error to the client

Requirements:

  R1:
When the owner account of a conversation is sticky-only, a Responses request or a compaction of that conversation SHALL be dispatched to the owner even when the continuation is marked `quota_refused`. An explicit quota refusal from that owner SHALL reach the client as HTTP 429 (or a WebSocket error frame) with type and code `usage_limit_reached`, and it SHALL NOT be replayed on another account. The continuation SHALL NOT be marked `quota_refused`.

  R2:
A request without an established owner SHALL still be balanced and SHALL still move to another account after an explicit quota refusal before visible output, as today, because no conversation has started on the refusing account yet. Accounts that are not sticky-only keep today's behavior.

  R3:
The refusal SHALL be settled exactly as today: uncharged when nothing was visible and no usage was reported, and the account outcome SHALL still record the quota refusal, so new conversations avoid the exhausted account.

Logic Details:
- Clearing the replay only for sticky-only owners keeps the invariant "failover requires an explicit upstream quota refusal before anything was visible to the client" for every other account. A sticky-only account never fails over owned work.
- A continuation already marked `quota_refused` before the option was enabled is treated as unmarked while the owner is sticky-only. Turning the option off restores normal replay at the next refusal.
- Voice calls already keep a pinned owner. Image routes are not conversations and keep their own image cooldown.

Files And Functions:
 - existing: internal/application/proxy.go#Proxy.respond - steps "dispatch to the owner even after an earlier quota mark" and "settle it uncharged without marking the conversation or replaying it"
 - existing: internal/application/codex_compact.go#CodexOperations.Compact - the same two steps for compaction
 - existing: internal/application/sticky_accounts.go#stickyOwner - clears the quota mark of a sticky-only owner
 - existing: internal/application/sticky_accounts.go#keepsStickyConversation - stops the replay for a sticky-only owner
 - existing: internal/application/sticky_accounts.go#stickyQuotaError - gives the client `usage_limit_reached`

Tests:
internal/adapters/httpapi/sticky_accounts_test.go
 - description: a conversation owned by a sticky-only account gets the quota refusal; the second account is not called; the next turn goes to the owner again
   requirements: R1, R3
 - description: the same conversation on a normal account replays on the second account, as before
   requirements: R2
 - description: a new conversation whose first request is refused for quota by a sticky-only account moves to the second account
   requirements: R2
 - description: a compaction of a sticky-only account's conversation is not replayed elsewhere
   requirements: R1

### 2. Switch an account to sticky-only

administrator --set the sticky-only option on the account--> stored option --read it with every account--> routing and dashboard show the option

Requirements:

  R4:
`PATCH /api/accounts/{id}` SHALL accept `stickyOnly` (boolean) for any account, ChatGPT or external, and update only that column. Account reads used for routing and the account list SHALL return it as `stickyOnly`. A new account SHALL default to `false`.

  R5:
The dashboard SHALL offer the option on the account sheet and in the model-source dialog, labeled so the owner can read it as «keep conversations on this account when its limit runs out».

Tables:
accounts
 - sticky_only: INTEGER NOT NULL DEFAULT 0 # 1 keeps owned conversations on the account

Logic Details:
- Migration 52 adds the column. It is additive: older code ignores it and `ALTER TABLE ... ADD COLUMN` with a constant default finishes at once.

Files And Functions:
 - existing: internal/domain/models.go#Account - `StickyOnly`
 - existing: internal/adapters/sqlite/sticky_accounts_schema.go#schemaV52
 - existing: internal/adapters/sqlite/accounts.go - account reads
 - existing: internal/adapters/sqlite/account_policy.go#Store.UpdateAccountStickyOnly
 - existing: internal/application/accounts_service.go#AccountsService.UpdateAccount
 - existing: internal/adapters/httpapi/accounts.go#Server.updateAccount
 - existing: web/src/features/accounts/components/account-detail-sheet.tsx
 - existing: web/src/features/model-sources/components

Tests:
 - description: PATCH sets and clears `stickyOnly` on a ChatGPT and an external account; the list returns it; migration leaves existing accounts at false and `PRAGMA user_version` equals the migration count
   requirements: R4
 - description: the dashboard toggle sends `stickyOnly` and shows the stored value
   requirements: R5

## Implementation checklist

1. [x] OpenSpec change `add-sticky-only-accounts` (proposal, tasks, ADDED requirement), validated strictly.
2. [x] Domain field, migration 52 (additive), account reads and single-column update. (R4)
3. [x] `stickyOwner` and the proxy and compaction changes, with docstrings naming the workflow steps. (R1, R2, R3)
4. [x] Admin API `stickyOnly`. (R4)
5. [x] Dashboard toggle on the account sheet and the model-source dialog, with locales. (R5)
6. [x] Route-level tests for sticky, normal and new conversations and compaction; API and migration tests; dashboard test. (R1–R5)
7. [x] Gates: `make check`, `make web-check`, `make web-deps build`, `make test-web-contract`.
8. [x] Archive the OpenSpec change, sync `go-runtime`, write release notes, merge and release; stop before prod.

## Open questions

None.

## Decision log

- 2026-11-22: the owner chose sticky behavior over a dedicated account: the account stays in balancing, and only owned conversations stop failing over. The option exists on every account.
- A first request without an owner may still move after a quota refusal. The owner's words name a chat "начавшийся на одном аккаунте", and before the first answer no chat has started there.
- The account outcome still records the refusal, so balancing keeps new conversations away from the exhausted account.
- The client receives `usage_limit_reached` (type and code) instead of the generic `insufficient_quota` "Upstream request failed", because Codex shows that as "usage limit reached", not as a billing problem.
- Model sources show the option as a switch on their list row and change it through `PATCH /api/accounts/{id}`, since a source is an account row; the source JSON carries `stickyOnly` read from that row.
