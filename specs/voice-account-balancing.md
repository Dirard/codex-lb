# Voice account balancing

- Last edited with skill pack: `0.2.2`

## Title and scope

Voice traffic on the subscription pool picks its account by load and limits instead of always taking the first active account: GPT-Live calls, Realtime calls (app and Codex routes) and app Realtime WebSocket sessions. A call or session refused for a limit before it exists moves to the next account, and voice limits the subscription reports keep new calls away from an account until they reset.

## Planning anchor

- Entrypoints:
  - existing `internal/application/codex_operations_owner.go#CodexOperations.accountForOwner` (first active ChatGPT account by id when nothing pins the owner);
  - existing `internal/application/live_calls.go#CodexOperations.CreateAppLiveCall`;
  - existing `internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall`;
  - existing `internal/application/realtime_apps.go#CodexOperations.PrepareRealtimeSession` and `RealtimeSession`.
- Changed assumption: go-v1.13.0 serves voice from the first active ChatGPT account in id order. Both prod checks landed on `3241176f`, a refusal never moves to another account, and `session.usage.updated.usage_limit` is ignored. On 2026-11-04 the owner asked to fix it («Да, сделай») after asking whether Live switches away from exhausted accounts.
- Evidence: the GPT-Live probes and the prod check (`specs/gpt-live-for-apps.md`) show `usage_limit {status, reset_seconds}` on every usage event; it stayed `null` there. How the subscription refuses a call at an exhausted voice limit was never observed.
- Non-local: application selection and failover for three voice entry points, Live observer, provider failure details, OpenSpec `go-runtime`, release notes.
- OpenSpec change `balance-voice-accounts`, archived as `openspec/changes/archive/2026-11-04-balance-voice-accounts/`: `go-runtime` ADDED «Voice routes balance subscription accounts».
- Impacted specs in `specs/`: `specs/gpt-live-for-apps.md` and `specs/realtime-voice-for-apps.md` (`amend`: their "pick one ChatGPT subscription account eligible for the key" now follows use case 1 here).

## Connected groups or observed existing logic

### Connected groups

1. Selection
   - `accountForOwner` returns a pinned owner's account (`strictOwnerAccount`) or the first account of `EligibleAccounts` (status `active`, no egress decision pending, ordered by id) that is a ChatGPT account.
   - `AccountSelectionSnapshot` (port `AccountSelectionSnapshots`) loads quota windows for many accounts in one read; the Responses balancer uses it.
2. Dispatch
   - `dispatchRealtimeCall` turns any non-2xx answer into `ProviderFailure{Code: realtime_call_unavailable, Status, Dispatched: true}`; the failed result keeps the upstream headers.
   - `Adapter.RealtimeSession` returns `ProviderFailure{realtime_session_unavailable, 429 or 502, Dispatched: false}` when the dial is refused, before anything is relayed.
3. Accounting
   - Each Live call and Realtime session reserves on the key with its account id; a settlement must name the same account.
4. Observation
   - `observeLiveCall` decodes `session.started`, `session.usage.updated` and `session.closed` frames from its sideband.

## Use cases

### 1. Choose a voice account

voice request --keep a pinned conversation owner--> pinned account or open choice --drop accounts refused for voice--> candidates --rank by voice warnings, exhausted Codex windows, live voice traffic and window use--> ordered accounts

Requirements:

  R1:
A voice request whose conversation already has an owner SHALL keep that owner's account, as before. Otherwise the candidates SHALL be the key's eligible active ChatGPT accounts, minus accounts refused for voice within their cooldown. When every candidate is refused, the request SHALL fail with 429 `voice_limit_reached` and `Retry-After` set to the earliest cooldown end, before any reservation or dispatch.

  R2:
Candidates SHALL be ordered with accounts without a current voice warning first, then accounts without a Codex quota window at or above 100% with a future reset, then by the fewest voice calls and sessions this worker is serving on the account (observed Live calls and Realtime WebSocket sessions), then by the lowest highest-window use, then by account id.

Logic Details:
- Voice limits are reported apart from Codex limits, but whether voice also draws on them is unknown, so an exhausted Codex window only lowers an account's place.
- Counts and cooldowns live in the worker's memory: a replacement worker starts empty and relearns from the next refusal or warning.

Files And Functions:
 - planned: internal/application/voice_accounts.go#CodexOperations.voiceAccounts - workflow steps "keep a pinned conversation owner", "drop accounts refused for voice", "rank by voice warnings, exhausted Codex windows, live voice traffic and window use"
 - planned: internal/application/voice_accounts.go#voiceBalance

Tests:
internal/application/voice_accounts_test.go
 - description: ranking puts warned, then Codex-exhausted, then busier, then more used accounts later; refused accounts drop out until their cooldown ends; all refused gives 429 with Retry-After; a pinned owner is kept
   requirements: R1, R2

### 2. Move a refused voice request to the next account

voice attempt on a candidate --recognize a limit refusal before the call or session exists--> refused attempt --cool the account down for voice and release its reservation--> next candidate --create the call or open the session there--> served request

Requirements:

  R3:
A Live call, a Realtime call or an app Realtime WebSocket session SHALL try at most three ranked candidates. Only a refusal with HTTP 429 at call creation, or at the session dial before anything is relayed, SHALL move it to the next one. Any other failure SHALL end the request as before, because the attempt may have executed. A pinned owner SHALL never move.

  R4:
A refused account SHALL be cooled down for voice for the refusal's `Retry-After` seconds (1 to 86400), else 15 minutes. Each refused attempt SHALL release or settle its own reservation, and the next attempt SHALL reserve again for its own account. The last refusal SHALL reach the client with its status, a fixed code and the upstream `Retry-After` when there was one.

Logic Details:
- A 429 answer means the subscription created nothing, so a retry cannot execute anything twice. The invariant "failover requires an explicit upstream quota refusal before anything was visible to the client" holds: call creation returns nothing before success, and a refused session dial happens before the first relayed frame.
- Codex's own `/backend-api/codex/realtime/calls` benefits too unless its conversation pins an owner.

Files And Functions:
 - existing: internal/application/codex_operations_realtime.go#CodexOperations.CreateRealtimeCall - candidate loop
 - existing: internal/application/codex_operations_realtime.go#CodexOperations.dispatchRealtimeCall - marks a 429 as a quota refusal with its Retry-After
 - existing: internal/application/live_calls.go#CodexOperations.CreateAppLiveCall - candidate loop with one reservation per attempt
 - existing: internal/application/realtime_apps.go#CodexOperations.RealtimeSession - candidate loop before the relay

Tests:
internal/adapters/httpapi/voice_balancing_test.go
 - description: a Live call refused with 429 and Retry-After on the first account is created on the second; the first account is skipped by the next call; the refused reservation is released and the call is charged on the second account
   requirements: R3, R4
 - description: a Realtime app call and a Realtime WebSocket session refused with 429 move to the next account; a 502 refusal does not move
   requirements: R3
 - description: when every account refuses, the client gets 429 with the upstream Retry-After, and the next request is refused before dispatch
   requirements: R3, R4

### 3. Spread calls and heed voice warnings

observed Live call --count the call on its account while it runs--> busy account --read the usage_limit of each usage event--> voice warning --rank the account last until the warning's reset--> spread voice traffic

Requirements:

  R5:
A Live call SHALL count on its account from creation until its observation ends, and a Realtime WebSocket session SHALL count while it is relayed.

  R6:
A Live usage event whose `usage_limit.status` is not null SHALL mark its account as warned for `usage_limit.reset_seconds` (1 to 86400), else 15 minutes, refreshed by later events. A warning SHALL only lower the account's rank; it SHALL NOT refuse calls, end the running call or change account health.

Files And Functions:
 - existing: internal/application/live_calls.go#CodexOperations.observeLiveCall - counting and warnings
 - existing: internal/application/realtime_apps.go#CodexOperations.RealtimeSession - counting

Tests:
internal/adapters/httpapi/voice_balancing_test.go
 - description: with two accounts, a second Live call goes to the other account while the first is observed; a `usage_limit.status: approaching` event sends the next call to the other account even when it is busier
   requirements: R5, R6

## Implementation checklist

1. [x] Write this spec and the OpenSpec change `balance-voice-accounts`; validate both.
2. [x] Application: `voiceBalance` and `voiceAccounts` with unit tests (R1, R2).
3. [x] Failover loops for Live calls, Realtime calls and Realtime sessions; quota refusal details from `dispatchRealtimeCall` (R3, R4).
4. [x] Counting and warnings in the Live observer and Realtime sessions (R5, R6).
5. [x] End-to-end tests with two subscription accounts against loopback stubs; mutations.
6. [x] Gates, release notes `go-v1.13.1`, context notes, sync and archive the OpenSpec change.
7. [ ] Publish go-v1.13.1 and stop before the rollout.

Deferred:
- Persisting cooldowns across workers and restarts.
- Balancing Realtime WebRTC app calls and Codex calls by live load: their media bypasses codex-lb, so they are not counted.
- Moving a running call when its account's limit ends mid-call: a call lives on its account.

## Open questions

- non-blocking: The subscription's refusal at an exhausted voice limit was never observed. HTTP 429 is assumed, as for every other subscription limit. If it is another status, calls keep failing on that account until it is known.
- non-blocking: The values of `usage_limit.status` besides `null` are unknown; every non-null value is treated as a warning.

## Decision log

- 2026-11-04: Rank instead of round robin: the fewest live voice calls spread the load, and warnings and exhausted windows move accounts back without excluding them, because no evidence says voice stops there.
- 2026-11-04: Only a 429 moves a request: it is the subscription's quota refusal everywhere else in codex-lb, and it guarantees nothing ran. Up to three accounts are tried, so one request cannot sweep a large pool.
- 2026-11-04: Cooldowns and counts stay in worker memory. They are cheap and relearned within one refusal, and they need no migration.
- 2026-11-04: Validation: ranking, cooldown and Retry-After unit tests, and end-to-end tests with two subscription accounts against a loopback GPT-Live and Realtime. They cover:
  - a Live call, a Realtime call and a Realtime session moving past a 429;
  - a 502 staying put;
  - every account refusing, then a refusal before dispatch;
  - spreading by load;
  - a warning outranking load.
  Fourteen mutations each fail their test. A first version of the spread test missed the "null status is no warning" mutation, because every stub sideband reported usage and so warned both accounts. Only the first call reports usage now.
- 2026-11-04: All twelve local gates green on the branch.
