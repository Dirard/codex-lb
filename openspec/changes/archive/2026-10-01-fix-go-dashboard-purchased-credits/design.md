# Dashboard credit visibility and admission

- Last edited with skill pack: `0.2.2`

## Title and scope

Repair missing account credit fields and permit purchased credits after included subscription capacity is exhausted. Continue the authorized implementation flow; deployment remains separately authorized.

## Planning anchor

`GET /api/dashboard/overview` serializes `DashboardAccount` without the credit fields used by `accountSubscriptionCredits` and `formatPurchasedCredits`. `EffectiveAccountQuotaStatus` and `creditBackedReservationAllowedTx` independently reject exhausted primary windows. Amend the primary-precedence statement in the authoritative `go-runtime` spec; reuse its other ownership and accounting rules.

## Connected groups or observed existing logic

- Account usage polling persists explicit credits and quota windows in SQLite; omitted metadata preserves the previous observation. `LoadAccountCreditStatus` reads those values after restart. Keep this storage contract and do not add a second credit ledger.
- Reports currently compute subscription capacity for aggregates but omit it from individual account summaries. Reuse the plan capacities used by `/api/accounts` and the existing window normalization; unknown capacity stays null. Frontend schema/cards already accept all required fields, so no UI redesign is necessary.
- Selection and SQL reservation are independent enforcement boundaries. Both must allow included-window exhaustion with usable credits and retain administrator, authentication, group, key, generation, route and separately metered-model checks. A stored quota refusal outranks an older credit snapshot; reuse the existing refusal timestamp and enforce the same rule inside the reservation transaction.
- Legacy primary precedence is an obsolete compatibility assumption for this requested behavior. Official Codex pricing says available credits permit continued work after included limits: https://developers.openai.com/codex/pricing/ . This does not promise recovery from any arbitrary upstream error.
- Validate through authenticated dashboard HTTP, real proxy use cases backed by temporary SQLite, ledger boundary tests, and existing dashboard rendering tests. No working database mutation or chargeable upstream call is required.

Focused inspection establishes the affected paths; separate reverse documentation and a broad review loop are unnecessary for this bounded repair.

## Use cases

### 1. Show account balances

authenticated overview request --load persisted quotas and credits--> known or unknown balances --serialize individual account fields--> existing card and list rendering

Implementation Logic:
Share the existing plan-capacity calculation. Fill the applicable primary, weekly or monthly window fields only when observed; unknown plans/credits remain null, exhausted known capacity is zero, and unlimited purchased credits retain their flag. Do not add purchased credits to subscription capacity. Use the same effective status as account selection.

Tests:
- description: Read dashboard summaries with dual-window, weekly-only, monthly, unknown, exhausted and unlimited credit metadata.
  expected outcome: Authenticated JSON exposes the correct independent values; no invented balances or phantom windows.

### 2. Continue on purchased credits

new request --apply key and account restrictions--> candidate with exhausted included quota --check usable credit evidence--> reserve through SQLite --dispatch and settle normally--> upstream result

Implementation Logic:
Usable credits bypass primary and long included-window exhaustion. An explicit finite zero balance is not overridden by a generic has-credits flag; unlimited remains authoritative. For stored quota-exceeded accounts, credit observation must postdate any recorded upstream quota refusal. The reservation transaction repeats that check against current state. Preserve actual rate-limit blocks, disabled states, separate model quotas and established-owner routing.

Tests:
- description: Exhaust both included windows with positive or unlimited credits, then publish a zero-credit observation.
  expected outcome: New requests use the credit-backed account while eligible, then use another account; established owners keep existing continuation semantics.
- description: Record a real proxy quota refusal after a positive credit observation, then attempt another request and direct reservation.
  expected outcome: Old credit data does not reopen the account; newer usable credit evidence can restore credit-backed admission without escaping scopes.

## Implementation checklist

1. [x] Trace dashboard, usage persistence, selection and reservation; specify the bounded change.
2. [x] Add regressions and populate dashboard credit/window fields using shared capacities.
3. [x] Align credit-backed selection and reservation with refusal freshness safeguards.
4. [x] Run focused/full tests, synchronize the main spec/context and archive the verified change.

## Open questions

None blocking. Actual OpenAI refusal remains authoritative; purchased credits do not guarantee admission against every provider restriction. No update of the running service is authorized by this change.

## Decision log

Use existing metadata, refusal timestamps, rendering and tests; no new configuration, schema, credit purchase, retry loop or dependency. Keep the previous deployment artifact unchanged.

The new dashboard HTTP and primary-credit regressions failed before the fix and pass after it. The common plan-capacity calculation now serves account views, dashboard cards and existing key-usage summaries. Positive-credit, zero-credit, unlimited, weekly/monthly, unknown-plan, upstream-refusal freshness and unchanged key settlement paths are covered without real provider traffic. The local database was inspected read-only and already contained a positive purchased balance with a fully used weekly window; missing UI fields were not missing upstream data.

Validation passed: `go test ./...`, full `go test -race ./...`, `go vet ./...`, 77 existing dashboard display/schema tests, and 10 frontend contract tests against a freshly built temporary binary. Strict OpenSpec validation of this change and `go-runtime`, and the configured SDD structural validator passed. Repository-wide strict validation reports 38 passing and 22 failing existing capability specs; for example, untouched `account-auth-export` has a placeholder Purpose rejected under strict mode. Those unrelated legacy specifications were not edited. No live service, credentials, account policy, price history or existing release archive was changed.
