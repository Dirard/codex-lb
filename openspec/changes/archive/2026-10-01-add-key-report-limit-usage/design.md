# Personal and group limit usage in key reports

- Last edited with skill pack: `0.2.2`

## Title and scope

Show personal limit usage, a separate compact same-group key list, and aggregate subscription-quota usage across that group's upstream accounts after API-key login. Reuse the administrator key page's counters and presentation.

## Planning anchor

`KeyUsageHandler.serveReports` returns only key-scoped traffic aggregates. `ApiListItem` and `ApiKeyInfo` already display current `LimitRule` values, but key-report login cannot call the admin key API. Amend the `go-runtime` key-report privacy contract to permit the requested narrow same-group summaries; all detailed traffic remains private to the authenticated key. Historical archived changes remain historical.

## Connected groups or observed existing logic

- Entry/auth: `/v1/usage/reports` and its slash alias authenticate the supplied key and reject arbitrary scope selectors. Preserve this path and add fields to its response, not an admin bypass.
- Persistence: group membership is `api_keys.group_id`; effective group limits are already copied into each key's `api_key_limits` with independent counters. Reads must not sum group usage or recompute consumption from report logs. Deleted/internal keys stay excluded; disabled/expired peers remain visible as inactive entries.
- Application: ReportsService owns report queries. Add a scoped limit-summary query to its existing repository boundary; use a short read-only SQLite snapshot joining the live requesting key to itself and its current group peers. Recheck active/non-expired requestor in that snapshot so stale authentication cannot disclose the previous group. Read aggregate-account inputs under the same snapshot.
- Time/accounting: `current_value` includes held reservations. Expired windows are projected as zero and advanced with the existing whole-window reset helper, without writing to the ledger. Window values are independent of report date/model filters. Preserve over-limit counters, clamp only the visual bar.
- Frontend: extract the existing mini limit bar/status and detailed limit rows into small shared components. Use them in both administrator and report views. Group rows are noninteractive; no credentials/prefixes, account assignments, per-account balances or peer logs enter this response. Derive percentages from props, with no new state/effects or dependency.
- Validation: extend the existing public HTTP isolation tests and report-login UI tests; cover cross-group access, live moves/revocation, expired windows, group-limit edits, read-only storage and clearing on logout. Existing admin widget tests protect the extraction.

The code and current specs are sufficient to understand this bounded slice; no separate reverse-documentation artifact or automatic review loop is needed. No schema migration or deployment is planned.

## Use cases

### 1. Read current personal and same-group limits

authenticated report request --validate report filters--> key-scoped traffic --read current authorized limit snapshot--> own limits and optional same-group summary --serialize safe fields--> report response

Implementation Logic:
The scoped query uses the authenticated key ID only; clients cannot choose another key/group. Return own `limits` and nullable `group` with group name and safe peer fields (ID, name, enabled/expiry state, current-key marker, limit rules). Join membership, requester validity and limit rows within one SQLite read snapshot. Use the existing reset calculation for elapsed windows in memory; do not invoke mutating refresh/reset APIs. Only active limit rules and non-deleted/non-internal keys are read. Current counters include reserved budget and can exceed the maximum after settlement.

Tests:
- description: Two keys in group A and keys in group B or without a group share an upstream account.
  expected outcome: A can see only A's safe per-key limit summary; traffic aggregates still belong solely to the caller.
- description: The caller moves groups or is revoked between initial authentication and the snapshot read.
  expected outcome: Read current membership or deny the request, never return the old group's summary.
- description: Expired, model-specific, held and over-limit counters exist.
  expected outcome: Correct current-window display without modifying rules, reservations or account state.

### 2. Display existing key widgets read-only

validated report response --render personal limits if configured--> optional group mini-list --refresh or sign out--> updated report or cleared session

Implementation Logic:
Reuse extracted limit rows for personal used/maximum amounts and the existing maximum-consumed-percentage mini bar for group rows. Show each peer's name and active/disabled/expired state. Keys without limits show no-limit text rather than an invented zero. Add a brief current-window/held-reservation note so date filters cannot be mistaken for the limit period. The report page keeps its own memory-only query cache and never loads administrator APIs. Reuse the existing locale set; the change is additive for older report responses.

Tests:
- description: Sign in with a grouped key, refresh changed counters, then sign out and sign in with an ungrouped key.
  expected outcome: Own details and peer percentages render as on the key page; no peer navigation, admin request or previous group data remains.

### 3. Show aggregate usage of group accounts

authorized grouped caller --read group account quota snapshot--> normalize observed windows --weight by subscription capacity--> independent window percentages --render group subscription usage--> read-only summary

Implementation Logic:
The user confirmed that “accounts” means upstream provider accounts, not the group's API keys. Sum used subscription credits divided by capacity credits using existing plan estimates, separately per primary/weekly/monthly window. Do not average account percentages or combine different windows. Count each non-deleted ChatGPT account once, even if it belongs to another group too; external providers have no compatible subscription window. Show window coverage against group account count so missing data cannot be mistaken for the full group. Unknown-plan capacity is never invented. Expired telemetry windows are omitted until observed again, matching existing pooled usage. Purchased balances have no known original capacity, so they do not enter this percentage; 100% subscription usage does not imply that a credit-backed account is unusable. Honor the global upstream-quota privacy flag and the caller's account-pool usage visibility. Return aggregate percentages/counts only, not account identities or balances.

Tests:
- description: A plus account at 100% and a pro account at 0% share a group; another exhausted account belongs only elsewhere.
  expected outcome: The percentage uses group capacity weights, not a 50% mean or unrelated accounts.
- description: Windows are missing, monthly-only, unknown-plan or quota visibility is disabled.
  expected outcome: Applicable windows and coverage are explicit, unknown is not zero, and hidden upstream data remains absent.

## Implementation checklist

1. [x] Trace existing report/limit widgets and define the narrow same-group disclosure contract.
2. [x] Add safe typed response fields and the authorized read-only snapshot query with regressions.
3. [x] Share existing limit/status widgets and render personal/group sections with UI regressions.
4. [x] Run focused/full checks and offline browser verification; synchronize specs and archive.

## Open questions

None blocking. “All keys” includes disabled/expired but not deleted/internal keys; group rules remain per-key budgets. Detailed peer report access, editing, new accounting, pricing changes and service updates are out of scope.

## Decision log

Use the screenshot's existing presentation and existing counters. Do not call the administrator list endpoint or retrieve every installation key before filtering. No new permission setting is needed because the requested feature explicitly grants this narrow same-group read view.

Verified: `go test ./...`, `go test -race ./...`, `go vet ./...`, frontend typecheck/build, focused ESLint, all 1236 frontend tests, and 10 separate frontend contracts against the built Go binary. Public route tests cover group isolation, membership changes between authentication and read, revocation, hidden account-quota settings, independent counters and no credential disclosure. SQLite tests confirm read-only expired-window projection and capacity-weighted quotas. Browser verification used an isolated loopback fixture with synthetic keys/accounts; the existing server was not restarted. OpenSpec strict validation and the configured SDD structural validator passed.
