# Subscription end on the Accounts page: the date ChatGPT reports now, with the +14-day reference

- Last edited with skill pack: `0.2.2`

## Title and scope

The Accounts page shows each ChatGPT account's subscription end as ChatGPT reports it now, read in the background from `GET /backend-api/subscriptions`, followed by `/` and that date plus 14 days. Until an account has such a read, the page keeps the date from the ID token saved at sign-in, which OpenAI does not update when the token is refreshed.

## Planning anchor

- Entrypoints:
  - existing `internal/application/account_usage.go#AccountUsageService.RefreshAll` (the usage poller, every minute);
  - existing `internal/adapters/httpapi/admin.go#Server.listAccounts` (`GET /api/accounts`);
  - existing `web/src/features/accounts/components/subscription-remaining.tsx#SubscriptionRemaining`.
- Changed assumption: the archived change `show-account-subscription-expiry` (2026-09-05) took the end from the ID token's `chatgpt_subscription_active_until` and expected it to follow a renewal once the token was refreshed. On 2026-11-02 prod showed that refreshed ID tokens keep `chatgpt_subscription_active_until` and `chatgpt_subscription_last_checked` from the last interactive sign-in. Four of six active accounts showed an elapsed period while they worked; the owner's report was skolcov (`adb59f43`): signed in on 2026-09-22, recorded end 2026-10-22, paid since.
- Evidence: an owner-approved read-only probe on 2026-11-02 with the owner's own Codex login (account `0b80f112`):
  - `GET https://chatgpt.com/backend-api/subscriptions?account_id=<id>` answered 200 with `active_until` `2026-11-05T18:27:39Z`, `active_start`, `will_renew: true`, `is_active: true`, `plan_type`, `billing_period` and `entitlement.{expires_at,renews_at,cancels_at}`;
  - the ID tokens of the same account still carry 2026-10-05 (prod sign-in on 2026-09-05) and 2026-08-23 (local sign-in on 2026-07-30), both refreshed in late October;
  - `GET /backend-api/accounts/check/v4-2023-04-27` carries the same entitlement for every workspace of the user; `/wham/usage` and `/wham/accounts/check` carry no dates.
- Non-local: ChatGPT usage adapter, application usage service, runtime wiring, admin API, dashboard schema, component and locales, OpenSpec `go-runtime` and `frontend-architecture`, release notes.
- OpenSpec change `show-live-subscription-end`, archived as `openspec/changes/archive/2026-11-02-show-live-subscription-end/`:
  - `go-runtime`: ADDED «The subscription end is read from ChatGPT in the background»; MODIFIED «The account list is read in a fixed number of statements» (adds `subscription`);
  - `frontend-architecture`: MODIFIED «Account views display subscription time remaining».
- Impacted specs in `specs/`: `specs/dashboard-redesign.md` names the Accounts page requirements and lists the `GET /api/accounts` additions of its own slice. Its statements stay true for that slice: `reuse`, no edit.

## Connected groups or observed existing logic

### Observed Existing Logic

1. Token claim: `accountAuthentication` decodes the saved ID token and returns `chatgpt_subscription_active_until` as `subscriptionActiveUntil`. `TokenService.exchange` stores every refreshed ID token, but OpenAI keeps the subscription claims of the last sign-in in it.
2. Usage poller: `RunUsagePoller` runs `RefreshAll` every minute after a 10–30 s start jitter. For each `usageEligible` account (ChatGPT, not deleted, a real ChatGPT account id, no pending egress decision, not paused, re-login or deactivated) it refreshes usage and then the in-memory reset-credit snapshot. Errors stay per account; the loop ignores them.
3. Provider adapter: `chatgpt.UsageClient.get` sends `GET <base>/backend-api<path>` with the bearer token and `chatgpt-account-id`, which it omits for synthetic `email_`/`local_` ids. It caps the body at 4 MiB and maps a status of 400 or more to `UsageError` with that status. The application retries a definitive 401 once after `ForceRefresh`.
4. In-memory snapshots: reset credits live in `AccountUsageService.snapshots`, keyed by account id and generation. `CachedResetCredits` serves `GET /api/accounts` without storage or provider reads.
5. API: `listAccounts` builds one `accountListView` per account. `GET /api/accounts` must not call a provider, and `accounts_list_test.go` compares every field with the go-v1.4.0 handler.
6. Dashboard: `SubscriptionRemaining` shows `Subscription: Xd / +14d: Yd` from `subscriptionActiveUntil`, `Recorded period ended` when elapsed, and a tooltip with both dates and the lag warning. Locales are `en`, `ko` and `zh-CN`.

## Use cases

### 1. Read the subscription end in the background

eligible account in a usage poll --skip unless 15 minutes passed since its last attempt--> due account --take an access token and read the subscription endpoint--> [kept read, previous read kept]

Requirements:

  R1:
The usage poller SHALL attempt a subscription read for each usage-eligible ChatGPT account at most once per 15 minutes per account incarnation, counting failed attempts.

  R2:
A read SHALL send `GET /backend-api/subscriptions?account_id=<ChatGPT account id>` with the account's access token, `chatgpt-account-id`, `Originator: codex_cli_rs` and `User-Agent: codex_cli_rs/<saved Codex client version>`, and SHALL be retried once after the usage token refresh on a definitive 401. An account with a synthetic ChatGPT account id SHALL NOT be read.

  R3:
A successful read SHALL keep in memory `active_until`, `will_renew`, the time of the read and the ChatGPT account id it was taken for. An answer without a valid `active_until` SHALL count as a failed read.

  R4:
A failed read SHALL keep the previous read and SHALL NOT change the account's status, credentials, quotas or routing. The provider's answer MUST NOT be logged.

  R5:
A read SHALL be served only for the account incarnation and ChatGPT account id it was taken for.

Types:
Subscription
 - ActiveUntil: time.Time # end of the current paid period, `active_until`
 - WillRenew: *bool # `will_renew`, nil when absent
AccountSubscription
 - ChatGPTAccountID: string # the id the read was taken for
 - Subscription: Subscription
 - ObservedAt: time.Time # when the read finished

Data:
subscription_read:
- request: `GET {usage base}/backend-api/subscriptions?account_id=<query-escaped ChatGPT account id>`, headers as for `/wham/usage` plus the Codex client identity of model catalog reads (`Originator`, `User-Agent` with the saved version);
- interval: 15 minutes per account incarnation, measured from the start of the last attempt;
- answer fields used: `active_until` (RFC 3339), `will_renew` (boolean); all others are ignored.

Logic Details:
- The attempt time is recorded before the request, so a failing account is tried again only after the interval and a slow upstream cannot cause back-to-back reads.
- The read runs after usage and reset credits in the same per-account step of `RefreshAll`, so its failure cannot affect them, and it reuses the access token path of usage polling.
- About four reads per account per hour are added to about 120 usage and reset-credit reads.

Files And Functions:
 - planned: internal/application/account_subscription.go#SubscriptionClient - application port for the read
 - planned: internal/application/account_subscription.go#AccountUsageService.RefreshSubscription - workflow step "take an access token and read the subscription endpoint"
 - planned: internal/application/account_subscription.go#AccountUsageService.CachedSubscription - the read served to the account list
 - planned: internal/adapters/chatgpt/usage.go#UsageClient.FetchSubscription - provider request and parsing
 - existing: internal/application/account_usage.go#AccountUsageService.RefreshAll - calls the read for each eligible account
 - existing: cmd/codex-lb/runtime.go - wires the usage client as the subscription client

Tests:
internal/adapters/chatgpt/usage_test.go
 - description: the read sends the path, query and headers and parses `active_until` and `will_renew`
   requirements: R2, R3
 - description: an answer without `active_until` is `invalid_response`; a 401 keeps its status; a synthetic id sends no request
   requirements: R2, R3
 - description: regression for go-v1.10.5 — an upstream that answers other clients with a 403 challenge page answers the read, which carries the saved version in `User-Agent`; without a valid version nothing is sent
   requirements: R2
internal/application/account_subscription_test.go
 - description: a read is kept, served for the same incarnation and ChatGPT account id only, and not repeated within 15 minutes
   requirements: R1, R3, R5
 - description: a failed read keeps the previous read, counts as an attempt and leaves the account and its quotas unchanged
   requirements: R1, R4
 - description: a definitive 401 is retried once after a token refresh; a paused account is not read
   requirements: R2

### 2. Return the read with the account list

GET /api/accounts --attach each account's in-memory read--> rows with `subscription` or null

Requirements:

  R6:
`GET /api/accounts` SHALL return per account `subscription` with `activeUntil`, `willRenew` and `observedAt` from the in-memory read, or null without one. It SHALL keep `subscriptionActiveUntil` and every other field, and MUST NOT call a provider.

Files And Functions:
 - existing: internal/adapters/httpapi/admin.go#accountListView - gains `Subscription`
 - existing: internal/adapters/httpapi/admin.go#Server.listAccounts - attaches `CachedSubscription`

Tests:
internal/adapters/httpapi/accounts_list_test.go
 - description: a seeded read is returned as `subscription`; an account without one returns null; other fields match the oracle
   requirements: R6

### 3. Show the current end and its +14-day reference

account row --take the ChatGPT read, else the token date--> displayed end --render the countdown, `/` and the +14-day countdown--> label and tooltip

Requirements:

  R7:
The row SHALL count down to `subscription.activeUntil` when present, else to `subscriptionActiveUntil`, followed by `/` and the countdown to that date plus 14 × 24 hours.

  R8:
The tooltip SHALL give both exact dates and the source: for a ChatGPT read, the time of the read and whether the subscription renews; for the token date, that it was saved at sign-in and may lag renewal.

  R9:
After the end, a ChatGPT read taken after that end SHALL be labelled `Subscription ended`; any other elapsed end SHALL keep `Recorded period ended`. Labels SHALL be localized in `en`, `ko` and `zh-CN`.

Files And Functions:
 - existing: web/src/features/accounts/schemas.ts - optional nullable `subscription`
 - existing: web/src/features/accounts/components/subscription-remaining.tsx#SubscriptionRemaining - chooses the source and its tooltip
 - existing: web/src/features/accounts/components/accounts-table.tsx - passes `subscription`
 - existing: web/src/i18n/locales/en.json - new subscription labels, also in `ko.json` and `zh-CN.json`

Tests:
web/src/features/accounts/components/subscription-remaining.test.tsx
 - description: a ChatGPT read wins over the token date and both countdowns follow it; the tooltip names the read time and renewal
   requirements: R7, R8
 - description: an elapsed read taken after its end says `Subscription ended`; one taken before keeps `Recorded period ended`; the token-only cases keep their labels
   requirements: R9

## Implementation checklist

1. [x] Write the OpenSpec change `show-live-subscription-end` (proposal, tasks, `go-runtime` and `frontend-architecture` deltas) and validate it strictly.
2. [x] Add `UsageClient.FetchSubscription` with adapter tests (R2, R3).
3. [x] Add `SubscriptionClient`, `RefreshSubscription`, `CachedSubscription` and the call from `RefreshAll`, with docstrings naming the use-case steps, and application tests (R1–R5).
4. [x] Wire the usage client as the subscription client in `cmd/codex-lb/runtime.go`.
5. [x] Return `subscription` from `GET /api/accounts` and extend the list test (R6).
6. [x] Show the read in `SubscriptionRemaining` with locales and vitest cases (R7–R9).
7. [x] Update operating notes: `openspec/specs/go-runtime/context.md` (the endpoint and the probe) and `openspec/specs/account-identity/context.md` (a refresh does not update the claim).
8. [x] Run all gates.
9. [x] Archive the change, sync the deltas, write `openspec/specs/go-runtime/releases/go-v1.10.5.md` and publish the release.
10. [x] After the owner updates prod: check that skolcov shows the paid end. On go-v1.10.5 every read got Cloudflare's 403 challenge; fixed by task 11. Verified on go-v1.10.6 on 2026-11-02: within 30 s of the update all six active accounts had a read (skolcov: end 2026-11-22, renews, against 2026-10-22 in its token), and the Accounts page showed "Subscription: 20d / +14d: 34d" with the read time in the tooltip. The two paused accounts and the re-login account keep the token date, as designed.
11. [x] Send the Codex client identity on the read (OpenSpec change `fix-subscription-read-identity`, release go-v1.10.6) with an adapter regression test (R2).

## Open questions

None blocking. Non-blocking, under the stated assumption: the answer of `/subscriptions` for a free account, a cancelled subscription or a team seat was not observed. A missing `active_until` counts as a failed read, so such an account keeps the token date.

## Decision log

- 2026-11-02: the owner asked for the current date always ("мне нужно чтобы там показывалась всегда актуальная дата") and to keep the +14-day reference ("актуальная дата / +14 дней"). Marking the token date as unconfirmed alone was dropped.
- Source: `/subscriptions?account_id=` instead of `accounts/check`: it answers for one workspace with a small body, and its `active_until` has the same meaning as the token claim.
- The read is kept in memory, like the reset-credit snapshot, not in a table: no migration is needed. After a restart or an update the token date shows until the first read, within the poller's start jitter and one pass.
- Interval 15 minutes for every account: after a renewal the page shows the new date within 15 minutes, for about 3% more provider requests.
- Paused, re-login and deactivated accounts are not read, as in usage polling; they show their last read of this process or the token date.
- The token-date fallback and `subscriptionActiveUntil` stay for compatibility and for accounts without a read.
- Workflow verification loop: skipped; the slice is narrow and every decision comes from the owner's request or the probe.
- Spec status: `specs/dashboard-redesign.md` reused unchanged.
- 2026-11-02, after go-v1.10.5 on prod: every read got Cloudflare's 403 challenge because the Go client sent its default `User-Agent`; the owner's probe had sent the Codex identity. An owner-approved diagnostic from prod confirmed it, and the read now sends `Originator` and `User-Agent` like model catalog reads. Usage and reset-credit reads keep their headers.
