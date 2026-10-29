# Dashboard redesign: pages and their APIs

- Last edited with skill pack: `0.2.2`

## Title and scope

Rebuild the administrator dashboard around what its single operator needs at hundreds of keys and dozens of groups: an overview organized by account groups with alerts and keys needing attention (PR 2), one key page shared by the administrator and the key owner plus reports for API-key users (PR 3), and keys, groups and accounts management pages (PR 4), each page served by one cheap rollup-backed endpoint whose main content answers in under 300 ms of server time. The public key-usage contracts (`/v1/usage`, `/api/codex/usage`, `/v1/usage/reports`) stay unchanged, and admin endpoints the new pages stop calling are deprecated for one release and then removed.

Out of scope: the rollups themselves (PR 1, [`usage-rollups.md`](usage-rollups.md)), Automations and Settings content beyond moving the key and group sections out of Settings, notifications outside the dashboard, multi-user roles, long-range legacy rollups (usage-rollups D7).

## Planning anchor

- Entrypoints:
  - Frontend: `web/src/App.tsx` (routes, `AuthGate`), `web/src/components/layout/app-header.tsx` (`CORE_NAV_ITEMS`, `/api/accounts` badge poll), `web/src/components/layout/status-bar.tsx` (`/health/ready`, `/api/dashboard/status` polls), `web/src/features/dashboard/components/dashboard-page.tsx`, `web/src/features/reports/components/reports-page.tsx`, `web/src/features/apis/components/apis-page.tsx`, `web/src/features/accounts/components/accounts-page.tsx`, `web/src/features/key-reports/key-reports-page.tsx`, `web/src/features/settings/components/settings-page.tsx` (`ApiKeysSection`, `AccountGroupsSection`).
  - Backend: `internal/adapters/httpapi/server.go#Handler` (`/api/` behind `compressJSON(requireAdmin(...))`; key-report session routes on the outer mux with cookie path `/api/key-reports`), `reports.go` (overview, projections, key trends and usage-7d, request logs, conversations), `admin.go#listAccounts` / `#listKeys` / `#listGroups`, `keys.go#saveKey`, `account_usage.go`, `key_report_session.go`, `key_reports.go#serveKeyReports`, `key_usage.go#RegisterPublicRoutes`; `internal/application/reports_dashboard.go`, `reports_projections.go`, `reports_weekly.go`, `key_usage.go`, `reset_credits.go`; `internal/adapters/sqlite/keys.go#ListAPIKeys` (one `GetAPIKey` per key), `key_limits.go#syncKeyLimitsTx` / `#backfillLimitTx`, `key_report_limits.go`, `key_report_group_quota.go`, `groups.go`.
  - Rollups from PR 1 ([design](../openspec/changes/archive/2026-10-29-usage-rollups/design.md) §2.2 and §4): `usage_rollup_total_hour`, `usage_rollup_cube_hour` with `idx_usage_rollup_cube_key`, `usage_rollup_slice` (kinds `key`, `account`, `model`, `useragent`; hour and UTC-day grains), `usage_rollup_error_hour`, `usage_rollup_conversation_hour`, `usage_rollup_log_hour`, `usage_rollup_dim`, the window planner `planRollupWindow` and the top-N keys and groups query of §4.2.
- Measured baseline (`.local/review/pages-latency.md`, production copy of go-v1.2.0, warm medians): Dashboard first useful render ≈ 8.5 s (overview 7d 5.2 s, request-log options 7.4 s, three heavy reads contending for the three-connection analytics pool); Reports ≈ 3.8 s; key report 0.84 s; busy-key usage-7d 0.75 s; Accounts calls ChatGPT synchronously whenever an account is selected.
- Changed assumptions:
  - Scale: plan for 300 API keys, 50 groups and up to 100 accounts (production today: 9 accounts, 28 keys).
  - The only user is the administrator; dense operator tables are preferred over cards.
  - Analytics come from the PR 1 rollups; group spend uses current membership (usage-rollups D4).
  - `group_limits` rows are templates copied into each member key's own `api_key_limits` (`SaveAPIKey` → `groupRulesTx` → `syncKeyLimitsTx`); there is no shared group counter, so group-limit fill is derived from the member keys' counters.
  - Ungrouped keys may use every permitted account, so the ungrouped row's pool is the whole ChatGPT account pool.
- Non-local: every dashboard feature folder, the admin HTTP adapter, new application use cases, SQLite readers on top of the rollups, the key-report session routes, OpenSpec `go-runtime` and the legacy `frontend-architecture` reference.
- Impacted specs in `specs/`:
  - `usage-rollups.md` — `reuse`. PR 2–4 consume its rollups and window planner. Two additions are requested from it (an `api_key_dim` index on `usage_rollup_error_hour` and a histogram quantile function), see Q7.
  - `dashboard-delivery.md` — `mark outdated` for its use case 4 when PR 2 lands (the status bar's own 60 s poll and the projections cadence are replaced by use case 1 of this spec); its delivery, compression, locale and toolchain use cases stay authoritative.
  - `storage-growth-backups.md`, `runtime-observability.md`, `security-hardening.md` — `reuse` (retention semantics, request logging, cookie and CSP rules are unchanged).
- Impacted OpenSpec capabilities:
  - `go-runtime` (normative) — `amend` through one change per PR: `openspec/changes/redesign-overview-alerts/` (PR 2), `openspec/changes/redesign-key-page-reports/` (PR 3), `openspec/changes/redesign-key-group-account-management/` (PR 4). They add the shell-status, overview, alert, attention, requests-page, key-page, owner-scope, key-report, keys-list, bulk-action, groups-summary, accounts-page, compatibility and latency-budget requirements of this spec. The existing requirements "API-key holders can view only their own read-only reports", "Key reports show current personal and same-group limit usage", "Key group reports expose aggregate account subscription usage", "Key group reports include remaining purchased credit totals", "Administrators can reset a group's key accounting periods", "Account cards can assign multiple groups atomically" and "Reports and request logs use bounded queries" stay and are extended, not replaced. "Read-only weekly-credit pace follows persisted settings" is modified only if the owner drops the pace card (Q2).
  - `frontend-architecture` (legacy reference, still amended by `optimize-dashboard-delivery`) — `amend` with REMOVED and location-only MODIFIED deltas for the widgets this redesign removes or moves (named in each use case's `Extension/Removed` layer), so the legacy reference does not contradict the new pages; no new requirements are added there.
  - Legacy references consulted for semantics only, not changed and not normative: `api-keys`, `account-groups`, `account-quota-presentation`, `account-pool-usage-v1-usage`, `usage-error-metrics`, `conversations-api`, `rate-limit-reset-credits`, `date-display-format`, `data-retention`, `admin-auth`, `automations`.

## Connected groups or observed existing logic

1. Frontend shell and routing
   - `AuthGate` restores both sessions, then renders `AppLayout` (header, `ApiKeyAuthWarning`, lazy routes `/dashboard`, `/reports`, `/accounts`, `/automations`, `/apis`, `/settings`) or, for a key-report session, `KeyReportsPage` without the admin layout.
   - Every page polls `/api/accounts` every 30 s only for the header reset-credit badge (≈60 point queries and credential decryption per call), plus `/health/ready` and `/api/dashboard/status` every 60 s.
   - `lib/query-client.ts`: `staleTime` 30 s, `retry` 1, no background polling.
2. Dashboard (`features/dashboard`)
   - Period picker 1d/7d/30d, `StatsGrid` (6 cards), `UsageDonuts`, `WeeklyCreditsPaceCard`, account list or cards, request logs or conversations with two more period pickers.
   - Four views of the same quota numbers and three independent period pickers; dead overview fields `additionalQuotas`, `accounts[].requestUsage`, `windows`.
3. Reports (`features/reports`)
   - `/api/reports` (daily rows, `byModel`, `byUseragent`, dead `byAccount`), a second "catalog" report when a model or user agent is selected, a second `/api/accounts` under another query key, and `/api/request-logs/options` over the whole history only to list key names.
   - No per-key breakdown and no group selection.
4. APIs page and key report
   - `/api/api-keys/` builds every key through `GetAPIKey` (4 queries) plus `UsageTotals`; the detail adds `/trends` (7-day hourly) and `/usage-7d`; overview cards are lifetime totals without a period label.
   - `KeyReportsPage` shows limits, summary cards, cost and tokens per day, a model donut and a daily table; `byUseragent` and latency medians arrive but are not shown; the model filter is free text.
   - Key-report reads revalidate the key, apply the firewall allowlist and are not compressed.
5. Accounts
   - `/api/accounts` builds each account with ≈6 point queries; selecting an account calls `GET /api/accounts/{id}/usage-reset-credits`, which refreshes usage from ChatGPT synchronously; "Request logs total" is a lifetime figure without a label.
6. Keys, groups and limits in storage
   - `api_keys.group_id` (one group per key, `idx_api_keys_group`), `account_groups`, many-to-many `account_group_accounts` (`idx_account_group_accounts_account`), `group_limits` templates, per-key `api_key_limits(current_value, max_value, reset_at, is_active)`, and `api_keys.expires_at`, `last_used_at`, `is_active`, `deleted_at`.
   - Saving a grouped key overwrites its limits with the group rules; a new limit window is backfilled by `backfillLimitTx` with a raw sum over the key's events of the window (`request_kind='normal'`) inside the write transaction.
   - `PATCH /api/api-keys/{id}` merges a partial body over the stored key.
7. Quota data
   - `account_quotas` (latest per window), `account_quota_history` (newest observation per five-minute bucket; `RecentAccountQuotaHistory` bounded to a 6 h horizon and 64 points per series), `account_usage_credits`.
   - `quotaDepletion` projects one account linearly; `weeklyCreditPace` builds a pool runway with reset ticks from 7 days of weekly samples.
8. Validation
   - `web/src/__integration__/*.test.tsx` page flows with MSW mocks, `go-runtime.test.ts` contract tests against the built binary (`make test-web-contract`), `BenchmarkAdminReports`, and the `pages-latency.md` harness on a production copy.
9. Specs: see Planning anchor.

## Use cases

### 1. Navigation, shell status and global search
signed-in administrator --open any page--> app layout with one page poll --page response carries the shell status--> header badges and status bar updated without own polls --press Ctrl+K and type--> directory matches --select a match--> entity page

Extension/Purpose:
One layout for every admin page: navigation in operator order, shell status taken from the page's own poll, and a search that jumps to any key, group or account by name, prefix or e-mail.

Extension/Screen:
1. Header: brand; navigation `Overview` · `Groups` · `Accounts` · `Keys` · `Reports` · `Automations` · `Settings` (final order after PR 4); badge on `Accounts` with accounts needing re-login, then available reset credits when that badge is enabled; global search box (`Ctrl/Cmd+K`); privacy toggle; language; logout.
2. `ApiKeyAuthWarning` when API-key authentication is disabled (from the shell status).
3. Page content.
4. Status bar: ready state, "quota synced" (relative and absolute time), "usage folded" (rollup state), routing strategy, version and update link.

Extension/Navigation evolution:
- PR 2: `Overview` replaces `Dashboard`; the `Requests` page (use case 3) is reachable from the overview, the key page and search, not from the navigation (Q3); `Accounts`, `APIs`, `Reports`, `Automations`, `Settings` are unchanged.
- PR 3: `APIs` is renamed `Keys`; `/keys/:id` is the key page; the current APIs list stays at `/keys` until PR 4.
- PR 4: `Groups` is added, the `Keys` list is replaced, the key and group sections leave Settings.
- Redirects: `/` and `/dashboard` → `/overview`; `/apis` → `/keys`; `/apis?selected=<id>` → `/keys/<id>`; `/accounts?selected=<id>` keeps working.

Events And Endpoints:
- `GET /api/dashboard/status` (existing, additive fields) → `ShellStatus`. Polled every 60 s only on pages without their own poll (Settings, Automations, Requests while Live is off, Reports).
- Every page poll response (`/api/overview`, `/api/groups/summary`, `/api/keys`, `/api/api-keys/{id}/page`, `/api/accounts`) carries `shell: ShellStatus`; the page fetcher writes it into the `["shell","status"]` query, which the header and the status bar read.
- `GET /api/directory` (new, admin, gzip) → `Directory`. Three queries: non-deleted non-internal `api_keys`; `account_groups` with member and key counts (`GROUP BY` over `account_group_accounts` and `api_keys`); `accounts`. ≈450 rows at 300 keys / 50 groups / 100 accounts, under 10 ms. `staleTime` 5 min; invalidated by key, group and account mutations; fetched when search opens and by pickers (Reports, Keys filters, bulk move).
- `GET /api/api-keys/{id}` (new, admin) → one `APIKey` (one `GetAPIKey`, 4 point queries) for edit dialogs opened outside the keys list.
- Removed polls: header `/api/accounts` (30 s) and status-bar `/health/ready` (60 s).

Types:
ShellStatus
 - lastSyncAt: time|null # newest quota observation
 - usageFoldedAt: time|null # usage_rollup_state.updated_at
 - rollupsUsable: bool
 - ready: bool # same readiness as /health/ready
 - reauthRequiredAccounts: int
 - availableResetCredits: int # in-memory reset-credit snapshot
 - showResetCreditBadges: bool
 - apiKeyAuthEnabled: bool
 - routingStrategy: string
Directory
 - keys: list
   - id: string
   - name: string
   - prefix: string
   - groupId: string|null
   - status: active|disabled|expired
 - groups: list
   - id: string
   - name: string
   - accountCount: int
   - keyCount: int
 - accounts: list
   - id: string
   - label: string # alias, else e-mail
   - email: string
   - status: string
   - kind: chatgpt|external

Requirements:

  R1:
Every admin page SHALL issue at most one periodic request: its page endpoint, or `GET /api/dashboard/status` every 60 s on a page without one. The header and the status bar SHALL read the shell status from that response and SHALL NOT poll `/api/accounts` or `/health/ready`.

  R2:
The shell status SHALL be computed from in-memory state and a fixed number of batch reads, independent of the number of accounts and keys.

  R3:
Global search SHALL match keys by name and prefix, groups by name and accounts by alias, e-mail and id from `GET /api/directory`, case-insensitively, SHALL show at most 20 results grouped by kind and SHALL navigate to the entity page; an input shaped like a request id SHALL open `/requests?request_id=<id>`.

  R4:
`/dashboard`, `/apis` and `/apis?selected=<id>` SHALL redirect to their replacements.

Files And Functions:
 - existing: web/src/App.tsx - routes and redirects
 - existing: web/src/components/layout/app-header.tsx#CORE_NAV_ITEMS - order, badges from the shell status, search entry
 - existing: web/src/components/layout/status-bar.tsx - reads `["shell","status"]`
 - planned: web/src/components/layout/use-shell-status.ts#useShellStatus - query with a route-dependent interval
 - planned: web/src/features/search/components/global-search.tsx#GlobalSearch - dialog, shortcut, grouped results
 - planned: web/src/features/search/hooks/use-directory.ts#useDirectory
 - existing: internal/adapters/httpapi/dashboard_status.go - additive shell fields
 - planned: internal/application/shell_status.go#ShellStatusService.Status
 - planned: internal/adapters/httpapi/directory.go#directory
 - planned: internal/adapters/sqlite/directory.go#Store.Directory

Tests:
web/src/components/layout/app-header.test.tsx
 - description: badges come from the page response and no `/api/accounts` or `/health/ready` request is made on the overview
   requirements: R1
web/src/features/search/components/global-search.test.tsx
 - description: prefix, e-mail and group matches; a request id opens the requests page
   requirements: R3
internal/adapters/httpapi/directory_test.go
 - description: deleted and internal keys are excluded, counts per group are correct and the statement count is equal for 30 and 300 keys
   requirements: R2, R3

### 2. Overview by account groups
admin opens the overview --GET /api/overview with the browser time zone--> overview snapshot --render alerts then the pool strip then group rows then attention keys--> overview page --expand a group row--> problem accounts and top keys of that group --act on an alert or a key--> mutation and refetched overview

Extension/Purpose:
Show within two seconds whether the account pool is healthy, which group needs attention, what each group of keys spends today and which keys need an action. It is organized by groups because there are tens to hundreds of keys, and it never lists all keys. Delivered in PR 2.

Extension/Screen:
1. Header line: title `Overview`, time zone label, "quota synced 40 s ago · usage folded 2 s ago", refresh button. There is no period picker: every number carries its own fixed period label (today, last hour, 7 days, current 5 h and weekly windows).
2. Alerts (`AlertsPanel`): rows `severity icon · subject · message · action button`, sorted by use case 2.2; the first 6 are shown with "show all (N)"; a single green line "No alerts" when the list is empty.
3. Pool strip (`PoolSummaryStrip`), one dense row: account health as a segmented bar with counts (ok, low, limited, re-login, paused, off) and the number of external sources; 5 h remaining % with next reset and projected exhaustion; weekly remaining % with next reset and projected exhaustion; spend today and at the same time yesterday; requests today; error rate in the last hour; 7-day spend sparkline with total.
4. Groups table (`GroupHealthTable`): one row per group plus `Ungrouped keys · whole pool`, attention first (use case 2.1). Columns: name and attention badge; account health segmented bar; 5 h remaining % and next reset; weekly remaining % and next reset; projected exhaustion (only when it falls before the reset); group-limit fill (highest member-key fill % and "n ≥ 90 %"); keys active/total; spend today; 7-day sparkline and 7-day total. Row actions: expand, open the group (Groups page from PR 4, the Settings group section before), open the group's keys.
5. Expanded group row (`GroupRowDetail`): problem accounts (up to 10: label, status, 5 h and weekly remaining with resets, reason, action Re-login, Resume or Open) and top keys (up to 5 by spend today: name, spend today, 7-day spend, highest limit fill, error rate in the last hour, last used, link to the key page).
6. Keys needing attention (`AttentionKeysTable`, top 10, use case 2.3): key and prefix, group, reasons as badges, spend today, fullest limit with its reset, expiry, primary action button and a menu (Open, Edit limits, Extend expiry, Disable); footer "n keys need attention" with links to the Keys list filtered by near limit and by expiry (PR 4).
7. Links to `Requests` and `Reports`.

Events And Endpoints:
- `GET /api/overview?timezone=<IANA>` (new, admin, gzip) → `OverviewResponse`. Default zone UTC; an unknown zone returns 400 `invalid_timezone`. Handler `internal/adapters/httpapi/overview.go#overview` → `application.OverviewService.Overview(ctx, zone, now)`.
- Sources, read in one read-pool snapshot and one analytics read transaction, with a fixed number of statements:
  - accounts, latest quotas, purchased-credit status and group memberships: four batch queries over `accounts`, `account_quotas`, `account_usage_credits`, `account_group_accounts`; burn rates from `RecentAccountQuotaHistory` (≈19 k rows at 100 accounts × 3 windows);
  - keys and limits: one query over `api_keys`, one over active `api_key_limits`;
  - per-key spend and requests for today and for each of the 7 local days ending today: `usage_rollup_slice` kind `key` (UTC-day grain inside the window, hour grain at local-day corrections and for today) with the PR 1 tail, journal and edges;
  - last-hour and trailing-24 h errors: `usage_rollup_total_hour` (pool), `usage_rollup_slice` kind `key`, grain 3600 (per key), `usage_rollup_error_hour` (top code, `client_disconnected` excluded).
- Expected cost at 300 keys / 50 groups / 100 accounts (est., modernc, warm): quota part 20–40 ms; key slices ≤ 18 k rows (Europe/Prague) 25–55 ms; errors and the current partial hour 5–45 ms; assembly under 5 ms; total 60–150 ms. Response ≈ 60–90 KB raw, 10–15 KB gzip.
- While `rollupsUsable` is false (backfill after an upgrade or a lagging folder), today's values are computed from today's raw events only and every `trend7d` is null.
- Mutations started here use existing endpoints: `PATCH /api/api-keys/{id}` (disable, extend expiry), `POST /api/accounts/{id}/reactivate`, the OAuth re-login flow (`POST /api/oauth/start`), and the existing `ApiKeyEditDialog` loaded through `GET /api/api-keys/{id}`. Each mutation invalidates the overview, the directory and the shell status.

Types:
OverviewResponse
 - generatedAt: time
 - timezone: string
 - shell: ShellStatus
 - pool: PoolSummary
 - alerts: list of Alert # use case 2.2
 - groups: list of GroupRow
 - attentionKeys: list of AttentionKey # use case 2.3, at most 10
 - attentionKeysTotal: int
PoolSummary
 - accounts: HealthCounts
 - externalSources: int
 - primary: WindowHealth # 5 h window
 - secondary: WindowHealth # weekly window
 - spendTodayUsd: number
 - spendYesterdaySameTimeUsd: number
 - requestsToday: int
 - errorRateLastHour: number|null
 - trend7d: list of number|null # cost per local day, oldest first, today last
HealthCounts
 - ok: int
 - low: int
 - limited: int
 - reauth: int
 - paused: int
 - off: int
WindowHealth
 - remainingPercent: number|null # capacity-weighted over covered accounts
 - remainingCredits: number|null
 - capacityCredits: number|null
 - coveredAccounts: int
 - totalAccounts: int
 - nextResetAt: time|null # earliest future reset among covered accounts
 - fullResetAt: time|null # latest future reset among covered accounts
 - burnCreditsPerHour: number|null
 - projectedExhaustionAt: time|null
 - runsOutBeforeReset: bool
GroupRow
 - id: string|null # null is the ungrouped row
 - name: string
 - attention: none|warning|critical
 - accounts: HealthCounts
 - primary: WindowHealth
 - secondary: WindowHealth
 - limitFill: LimitFill
   - maxPercent: number|null
   - keysAtOrAbove90: int
   - keysBlocked: int
 - keys: KeyCounts
   - active: int
   - total: int
 - spendTodayUsd: number
 - spend7dUsd: number|null
 - trend7d: list of number|null
 - problemAccounts: list of ProblemAccount # at most 10
 - topKeys: list of TopKey # at most 5
ProblemAccount
 - id: string
 - label: string
 - status: string
 - health: ok|low|limited|reauth|paused|off
 - primaryRemainingPercent: number|null
 - primaryResetAt: time|null
 - secondaryRemainingPercent: number|null
 - secondaryResetAt: time|null
 - action: reauth|resume|open
TopKey
 - id: string
 - name: string
 - spendTodayUsd: number
 - spend7dUsd: number|null
 - maxLimitPercent: number|null
 - errorRateLastHour: number|null
 - lastUsedAt: time|null

Extension/Polling and states:
- Poll every 30 s while the tab is visible (`refetchIntervalInBackground: false`), refetch on focus, `placeholderData: keepPreviousData`; the refresh button refetches only this query.
- Loading: the shell renders at once; skeletons for the pool strip and six group rows.
- Error: an inline retryable error card above the last successful data, labelled "stale since HH:MM"; 401 follows the existing sign-in handling.
- Empty: no accounts → "Add an account" (`/accounts?add=1`); no keys → "Create a key"; no groups → only the ungrouped row and a hint to create groups; no alerts → "No alerts"; no attention keys → "No keys need attention".
- Rollups unusable → a banner "Usage analytics are catching up; 7-day trends are hidden".

Extension/Removed:
- The 1d/7d/30d period picker and `StatsGrid` (requests, tokens, cost, conversations, burn projection, error rate with previous-window deltas): period comparisons belong to Reports, and the burn-projection card was opaque (its sparkline was a rescaled token trend).
- `UsageDonuts` and `WeeklyCreditsPaceCard`: further views of the numbers the pool strip and group rows now show once; the pace card moves to the Groups page or is dropped (Q2).
- Account list and account cards: management moves to Accounts; the overview shows only problem accounts inside group rows.
- Request logs and conversations with their own period pickers: moved to `/requests` (use case 3).
- The 15 s overview poll, the projections request chained after it and the refresh button that invalidated every `["dashboard", …]` query.
- `frontend-architecture` deltas: REMOVED "Dashboard weekly credits pace", "Dashboard projections load after the primary dashboard data", "Dashboard usage donuts present credits as stacked remaining and capacity", "Dashboard account summaries sorted by primary capacity", "Account card row height is 11.5rem", "Weekly credits pace header uses flex-start alignment", "Dashboard account cards show live credit state", "Dashboard accounts section shows account availability summary", "Dashboard overview summary cards show previous-window usage deltas", "Dashboard estimated cost card meta avoids duplicate estimate and cache copy", "Dashboard accounts section supports card and list views"; MODIFIED (location only) "Account views display subscription time remaining", "Dashboard limit warm-up controls" and the dashboard request-log requirements, which now name the Accounts and Requests pages.

Requirements:

  R5:
`GET /api/overview` SHALL return the pool summary, every group row, the ungrouped row, the alerts and at most 10 attention keys for the requested zone in one response, using a number of SQL statements independent of the number of keys, groups and accounts; while the rollups are usable it SHALL NOT read raw usage events beyond the PR 1 edges, tail and journal.

  R6:
Spend SHALL be estimated cost in USD from the rollups for the current local day of the requested zone and for each of the 7 local days ending today. Group spend SHALL be the sum over the group's current non-deleted, non-internal member keys; the ungrouped row SHALL sum the keys without a group.

  R7:
The overview page SHALL render the alerts, the pool strip, the groups table and the attention keys in that order, SHALL have no period picker and SHALL label every metric with its fixed period.

  R8:
The overview page SHALL poll only `GET /api/overview`, every 30 s while visible, and SHALL keep showing the previous data with a stale label when a refetch fails.

  R9:
While the rollups are unusable the overview SHALL compute today's values from today's raw events only and SHALL return null 7-day trends.

Files And Functions:
 - planned: web/src/features/overview/components/overview-page.tsx#OverviewPage
 - planned: web/src/features/overview/components/alerts-panel.tsx#AlertsPanel
 - planned: web/src/features/overview/components/pool-summary-strip.tsx#PoolSummaryStrip
 - planned: web/src/features/overview/components/group-health-table.tsx#GroupHealthTable
 - planned: web/src/features/overview/components/group-row-detail.tsx#GroupRowDetail
 - planned: web/src/features/overview/components/attention-keys-table.tsx#AttentionKeysTable
 - planned: web/src/features/overview/hooks/use-overview.ts#useOverview
 - planned: web/src/features/overview/api.ts - fetcher that also writes the shell status
 - planned: web/src/features/overview/schemas.ts - zod schema of `OverviewResponse`
 - existing: web/src/components/sparkline-chart.tsx - 7-day sparklines (own SVG, no recharts)
 - existing: web/src/components/mini-quota-bar.tsx, web/src/components/status-badge.tsx - reused
 - existing: web/src/features/api-keys/components/api-key-edit-dialog.tsx - opened from attention keys
 - planned: internal/domain/overview.go - `HealthCounts`, `WindowHealth`, `AccountHealthState`, `ProjectRunway`
 - planned: internal/application/overview.go#OverviewService.Overview - owns its `OverviewRepository` port
 - planned: internal/adapters/httpapi/overview.go#overview
 - planned: internal/adapters/sqlite/overview_rollup.go#Store.KeyDailySpend - local-day sums per key from slices
 - planned: internal/adapters/sqlite/overview_rollup.go#Store.ErrorWindows - last hour and trailing 24 h
 - planned: internal/adapters/sqlite/accounts_batch.go#Store.ListAccountHealth - batch account, quota, credit and membership reads
 - planned: internal/adapters/sqlite/keys_batch.go#Store.ListKeyRows - one query for keys and one for limits

Tests:
internal/adapters/httpapi/overview_test.go
 - description: fixture with 300 keys, 50 groups and 100 accounts; the statement count equals the count for 30 keys; ungrouped row present; deleted and internal keys excluded
   requirements: R5, R6
internal/adapters/sqlite/overview_rollup_test.go
 - description: `KeyDailySpend` equals the raw evaluation in UTC, Europe/Prague across both DST changes and Asia/Kolkata, in folded, tail and pending-journal states
   requirements: R6
 - description: with the rollups unusable only today's raw rows are read and trends are null
   requirements: R9
internal/application/overview_test.go
 - description: a key moved between groups counts in its current group for every day
   requirements: R6
web/src/features/overview/components/overview-page.test.tsx
 - description: section order, no period picker, one poll every 30 s, stale label after a failed refetch
   requirements: R7, R8

### 2.1 Group health, projections and ordering
group accounts and member keys --aggregate account health and capacity-weighted windows--> group window health --project the runway from current burn and resets--> projected exhaustion --derive limit fill and spend from member keys--> group row --score attention--> ordered group rows

Logic Details:
- Account health state (`domain.AccountHealthState`), ChatGPT accounts only: `reauth` for status `reauth_required`; `paused`; `off` for `deactivated`; `limited` for `rate_limited`, `quota_exceeded`, or a window at 0 % without usable purchased credits; `low` when active and a window has ≤ 10 % remaining; otherwise `ok`. External sources are counted separately.
- Window health follows the existing key-report group aggregate: remaining % = Σ remaining credits / Σ known capacity over accounts with a current observation; coverage stays explicit; unknown windows are null, never zero. An account in several groups counts fully in each of them and once in the pool.
- Burn per account $b_a$: positive deltas of used % within one reset window over the trailing ≤ 5 h of `RecentAccountQuotaHistory`, times capacity / 100, per hour; fewer than two observations make $b_a$ unknown (excluded, coverage reduced). Group burn $B = \sum_a b_a$ is the accounts' whole burn whichever keys caused it, because that is what drains the group's accounts.
- Runway (`domain.ProjectRunway`, pure): with remaining $R_a$, capacity $C_a$ and reset time $\rho_a$ of each covered account, at time $t \ge t_0$

  $$R(t) = \sum_a R_a - B\,(t - t_0) + \sum_{a:\ t_0 < \rho_a \le t} \min\big(C_a,\ C_a - R_a + b_a(\rho_a - t_0)\big)$$

  `projectedExhaustionAt` is the first $t \le$ `fullResetAt` with $R(t) \le 0$ (piecewise linear between reset instants); `runsOutBeforeReset` is true when it exists. `fullResetAt` is the latest $\rho_a$ among covered accounts, or $t_0$ plus the window length when no reset is known. No projection is made when $B$ is unknown or zero.
- Limit fill: per member key, the maximum over its active limits of `current_value / max_value`, with expired windows (`reset_at ≤ now`) counted as 0 without mutating storage, as the key report does. Group fill is the maximum over members; `keysAtOrAbove90` and `keysBlocked` (≥ 100 %) count members.
- Attention score: 2 when the group has a critical alert (critical runway, an account needing re-login, a blocked key), 1 for a warning, else 0.

Implementation Logic:
- Local-day sums per key for a whole-hour zone with UTC offset $o$ hours: a local day $[L_0, L_0 + 24\,h)$ overlaps two UTC days. Each day is read either as the UTC-day slice row of the day it mostly overlaps, minus the $|o|$ hour rows of that UTC day outside the local day, plus the $|o|$ hour rows of the neighbouring UTC day inside it, or as its 24 hour rows, whichever reads fewer rows. Days with a DST change use hour rows. Today is read from hour rows since local midnight plus the PR 1 sources for the current hour. Fractional-offset zones use the PR 1 raw split hour at each local midnight (usage-rollups D3).
- The per-key day series feeds pool trends, group trends, top keys, spend-spike reasons and the keys list (use case 6), so it is computed once per request.

Requirements:

  R10:
Pool and group window health SHALL be capacity-weighted over accounts with a current observation, SHALL report coverage and SHALL show unknown windows as unavailable rather than zero.

  R11:
Projected exhaustion SHALL follow `domain.ProjectRunway` from the trailing ≤ 5 h burn of the group's accounts and their reset times; a group SHALL be marked as running out before reset only when its runway reaches zero before the latest reset of its covered accounts.

  R12:
Group rows SHALL be ordered by attention score, then number of problem accounts, then keys at or above 90 %, then spend today, then name; the ungrouped row SHALL follow the same rule.

Tests:
internal/domain/overview_test.go
 - description: runway with an account resetting before and after exhaustion, unknown burn excluded, zero burn without projection, account shared by two groups
   requirements: R10, R11
 - description: health state for every account status, low at exactly 10 %, purchased credits preventing `limited`
   requirements: R10
internal/application/overview_test.go
 - description: ordering by score, problem accounts, keys at or above 90 %, spend and name
   requirements: R12

### 2.2 Alert rules
overview inputs --evaluate each rule with its fixed thresholds--> raw alerts --collapse more than five subjects of one rule and severity--> alert list --sort by severity then rule order then value--> alerts panel

Data:
alert rules:

| Rule | Subject | Warning | Critical | Action |
|---|---|---|---|---|
| `account_reauth` | account | — | status `reauth_required` | Re-login through OAuth for that account |
| `quota_runway` | pool and each group | `runsOutBeforeReset` in the 5 h or weekly window | exhaustion within 1 h (5 h window) or 12 h (weekly), or remaining ≤ 5 % | Open the group; for the pool open Accounts |
| `key_limit` | key | an active limit ≥ 90 % | an active limit ≥ 100 % (requests are refused) | Edit limits; for a grouped key open the group's limits |
| `error_spike` | pool | last hour: error rate ≥ 5 %, ≥ 20 errors and ≥ 3 × the trailing 24 h rate (baseline floor 1 %) | last hour: error rate ≥ 20 % with ≥ 20 errors | Open `/requests?status=error&timeframe=1h` |
| `key_error_spike` | key | last hour: ≥ 20 errors and error rate ≥ 25 % | — | Open the key page's errors section |
| `key_expiry` | key | an active key expires within 7 days | within 24 hours | Extend the expiry by 30 days |
| `quota_data_stale` | pool | newest quota observation older than 10 min while ChatGPT accounts exist | older than 60 min | Open Accounts |

Logic Details:
- Errors exclude `cancelled` and `client_disconnected`, as everywhere else. Windows are hour-aligned so that only the current hour needs the PR 1 current-hour sources: last hour = $[\lfloor now \rfloor_h - 1\,h,\ now)$; trailing = the 24 hours before it.
- Every alert has a stable id `rule:subjectType:subjectId:window`, so the page keeps expanded rows across polls.
- More than five subjects of one rule and severity collapse into one summary alert ("12 keys at or above 90 %") whose action opens the filtered list (`/keys?near_limit=1`, `/accounts?status=reauth_required`, `/keys?expiring_days=7`).
- The ungrouped row raises no `quota_runway` alert of its own; the pool alert covers its accounts.
- Thresholds are constants in `internal/domain/alerts.go` (Q1). Alerts are evaluated per request; nothing is stored, acknowledged or sent.

Types:
Alert
 - id: string
 - rule: account_reauth|quota_runway|key_limit|error_spike|key_error_spike|key_expiry|quota_data_stale
 - severity: warning|critical
 - subject: AlertSubject
   - type: pool|group|account|key
   - id: string|null
   - name: string
 - window: 5h|weekly|null
 - value: number|null # percent or error rate
 - at: time|null # exhaustion, expiry or last observation
 - count: int # greater than 1 for a collapsed summary
 - action: AlertAction
   - kind: reauth|open_group|open_accounts|edit_limits|open_group_limits|open_requests|open_key_errors|extend_expiry|open_list
   - target: string # route or entity id

Requirements:

  R13:
The overview SHALL evaluate exactly the rules of the alert-rules table with their thresholds and SHALL NOT persist, acknowledge or send alerts.

  R14:
Alerts SHALL be sorted critical first, then by the rule order of the table, then by value, and more than five subjects of one rule and severity SHALL collapse into one summary alert whose action opens the filtered list.

  R15:
Every alert SHALL carry exactly one action that the page performs or navigates to without a request other than the action's own.

Files And Functions:
 - planned: internal/domain/alerts.go#EvaluateAlerts - rules, thresholds, collapse, ordering
 - planned: web/src/features/overview/components/alert-action-button.tsx#AlertActionButton

Tests:
internal/domain/alerts_test.go
 - description: table-driven boundaries 89.9/90/100 %, 7 days/24 h, 19/20 errors, baseline floor, stale 10/60 min, runway 1 h/12 h
   requirements: R13
 - description: six key-limit subjects collapse into one summary; ordering critical, rule order, value
   requirements: R14
web/src/features/overview/components/alerts-panel.test.tsx
 - description: each action kind navigates or opens its dialog
   requirements: R15

### 2.3 Keys needing attention
member keys with limits spend and errors --collect reasons per key--> candidate keys --rank by severity then reason order then value--> top ten attention keys --pick the primary action of the top reason--> attention table

Data:
attention reasons in rank order:

| Reason | Condition | Severity | Primary action |
|---|---|---|---|
| `limit_blocked` | an active limit ≥ 100 % | critical | Edit limits (ungrouped) or open the group's limits (grouped) |
| `limit_near` | an active limit ≥ 90 % | warning | same as above |
| `error_spike` | rule `key_error_spike` | warning | Open the key page's errors section |
| `expiring` | expires within 7 days; critical within 24 hours | warning or critical | Extend by 30 days |
| `spend_spike` | spend today ≥ 2 × the average of the previous 7 local days and ≥ 10 USD | warning | Open the key page |

Types:
AttentionKey
 - id: string
 - name: string
 - prefix: string
 - groupId: string|null
 - groupName: string|null
 - reasons: list of AttentionReason
   - reason: limit_blocked|limit_near|error_spike|expiring|spend_spike
   - severity: warning|critical
   - value: number|null
 - spendTodayUsd: number
 - spend7dUsd: number|null
 - maxLimit: LimitState|null
   - type: string
   - window: string
   - modelFilter: string|null
   - percent: number
   - resetAt: time
 - expiresAt: time|null
 - lastUsedAt: time|null
 - primaryAction: edit_limits|open_group_limits|open_key_errors|extend_expiry|open_key

Requirements:

  R16:
The overview SHALL list at most 10 active keys with at least one reason from the attention table, ranked by highest severity, then reason order, then the reason's value, then spend today, and SHALL report how many keys have a reason in total.

  R17:
The primary action SHALL be available inline. Extend SHALL set `expiresAt` to 30 days after the later of now and the current expiry, and Disable SHALL set `isActive=false`, each after a confirmation and through `PATCH /api/api-keys/{id}`.

Tests:
internal/domain/attention_test.go
 - description: ranking across reasons, ties by spend, disabled and deleted keys excluded, spend-spike floor at 10 USD
   requirements: R16
web/src/features/overview/components/attention-keys-table.test.tsx
 - description: extend and disable send the expected PATCH bodies after confirmation and refetch the overview
   requirements: R17

### 3. Requests page for the request log and conversations
admin opens the requests page or follows a filtered link --GET /api/request-logs for the last 24 hours--> first page --switch to conversations or change the one period picker--> filtered view --open a row--> request or conversation details

Extension/Purpose:
Keep the raw request log and the conversation view for diagnosis, moved off the overview, with one period picker and no history-wide reads on load. Delivered in PR 2 by moving the existing components.

Extension/Screen:
1. Header: `Requests`, one period picker (1 h, 24 h default, 7 d, 30 d), view switch Requests or Conversations, Live toggle (off by default).
2. Filters (`RequestFilters`): search, accounts, keys, models, statuses, conversation chip. Key and account values come from the directory; model and status values from `GET /api/request-logs/options` for the selected timeframe, fetched when the filter panel opens.
3. Table (`RecentRequestsTable`) with the saved column layout and the details dialog, or `ConversationsView` with its dialog.
4. "Load more" through `nextCursor`; total from the PR 1 counters.

Events And Endpoints:
- Existing `GET /api/request-logs` (keyset `cursor`, totals from PR 1), `GET /api/request-logs/options?timeframe=…` (log cube, PR 1), `GET /api/conversations`, `GET /api/conversations/{id}`; no shape change.
- URL parameters `timeframe`, `status`, `api_key_id`, `account_id`, `request_id`, `conversation_id` prefill the filters for links from alerts, the key page and search.

Extension/Polling and states:
- No polling unless Live is on (30 s, first page only); options `staleTime` 5 min; the shell status is polled at 60 s.
- Empty: "No requests in the selected period"; error: retryable inline message keeping the previous page.

Extension/Removed:
- The "all" timeframe as default and the second and third period pickers of the dashboard; free-text search over a 30-day window is an explicit action and stays bounded by the timeframe.

Requirements:

  R18:
The requests page SHALL default to the last 24 hours, SHALL have one period picker shared by logs and conversations, SHALL NOT poll unless Live is on, and SHALL fetch filter options only for the selected timeframe when the filter panel opens.

Files And Functions:
 - planned: web/src/features/requests/components/requests-page.tsx#RequestsPage
 - existing: web/src/features/dashboard/components/recent-requests-table.tsx - moved to `features/requests`
 - existing: web/src/features/dashboard/components/filters/request-filters.tsx - moved, period picker removed
 - existing: web/src/features/dashboard/components/conversations-view.tsx - moved
 - existing: web/src/features/dashboard/hooks/use-request-logs.ts, use-conversations.ts - moved, polling behind Live

Tests:
web/src/__integration__/requests-flow.test.tsx
 - description: default 24 h, one picker, no polling until Live, options fetched only when filters open, alert link prefills status
   requirements: R18

### 4. Key page for the administrator and the key owner
key page request with a key id or an owner session --authorize in admin or owner scope--> authorized key --read the key limits and period rollups in one snapshot--> key page data --project each limit and apply the scope--> scoped key page --render the sections--> key page --load more requests or act on the key--> updated key page

Extension/Purpose:
One page per key, the same component for the administrator (`/keys/:id`, with management actions) and for the key owner (key-report mode after signing in with the key), much more informative than today's APIs detail and key report. Delivered in PR 3.

Extension/Screen:
Sections in screen order; "admin" marks parts that only the administrator sees.
1. Header: name, prefix, status (active, disabled, expired), group (admin: link), created, last used, expiry countdown. Admin actions: Edit, Edit limits (ungrouped) or Open group limits (grouped), Move to group, Disable or Enable, Regenerate, Reset period (grouped, applies to the whole group), Delete, Copy owner link. Owner actions: Copy link, Sign out.
2. Period picker, the page's only period control: Today, 7 d (default), 30 d, 90 d, custom up to 730 days; `?from=YYYY-MM-DD&to=YYYY-MM-DD` in the URL. Sections 4–9 follow it; sections 3 and 10 are labelled "current".
3. Limits (current windows): per limit its type, window, model filter, used/max, %, reset time (absolute and relative) and projected exhaustion ("runs out at 14:20 at the last 3 h pace" or "lasts until reset"); expired windows show 0 and the advanced reset. Below them the pool remaining: for an ungrouped key the whole pool's 5 h and weekly remaining with next reset; for a grouped key the group aggregate and purchased credits as today. Owner mode of a grouped key keeps the same-group mini-list.
4. Period KPIs with the previous-period comparison: requests, input/output/cached tokens and cache share, cost, errors and error rate, cancelled, TTFT p50 and p95.
5. Usage by day and model: cost per local day stacked by the top 5 models plus other (toggle requests or tokens); by-model table with model, requests, tokens, cost, share, TTFT p50 (reasoning effort is not a cube dimension; recent requests show it per request).
6. Errors by code: code, count, share, last seen; error rate per day; link "show these requests".
7. Latency per day: TTFT p50 and p95, total latency p50 and p95, queue p95, tokens per second p50.
8. User agents: group, requests, cost, share.
9. Recent requests: the last 50 with time, model and effort, status, error code, input/output/cached tokens, cost, TTFT, total latency, transport and user-agent group; admin adds account, client IP, conversation and the details dialog; "errors only" toggle; "load more".
10. Key properties (current): allowed models, enforced model and effort, allowed efforts, traffic class, service tier, usage sections, expiry; admin adds the assigned accounts and model sources, the owner sees only their counts.
11. Admin: cost by account for the period, and lifetime totals labelled "Lifetime" (`usage_totals`).

Events And Endpoints:
- `GET /api/api-keys/{id}/page?start_date&end_date&timezone` (new, admin, gzip) → `KeyPageResponse` with `scope=admin`.
- `GET /api/key-reports/page?start_date&end_date&timezone` (new; key-report cookie route under `/api/key-reports`, firewall allowlist, key revalidation, `Cache-Control: no-store`, `Vary: Cookie`, gzip) → `KeyPageResponse` with `scope=owner` (use case 4.1).
- `GET /api/api-keys/{id}/requests?cursor&limit&errors_only` (admin) and `GET /api/key-reports/requests?cursor&limit&errors_only` (owner) → `{items, nextCursor}` through `ListRequestLogs` with `APIKeyIDs=[id]` and the keyset cursor; `limit` ≤ 100; owner items are projected.
- Sources, in one analytics read transaction:
  - key row, limits, group and assignments: point reads;
  - day × model × user agent × account measures and the TTFT, queue and TPS histograms: `usage_rollup_cube_hour INDEXED BY idx_usage_rollup_cube_key` for the period and the previous period, plus the PR 1 edges, tail and journal, plus `legacy_hourly_usage` through `idx_legacy_hourly_key_account` for pruned days (pruned days have no latency, user agent or error codes);
  - errors by code: `usage_rollup_error_hour` filtered by `api_key_dim` through `idx_usage_rollup_error_key` (Q7);
  - limit pace: `usage_rollup_slice` kind `key`, grain 3600, for the last 3 hours; cube rows by key and model when the limit has a model filter;
  - recent requests: `usage_events INDEXED BY idx_usage_events_key_time` newest first, plus open reservations as in the request log; errors-only walks the same index newest first and examines at most 5 000 rows per call, continuing after the last examined row (pending entries are not failures and are left out);
  - pool or group remaining: the batch account reads of use case 2.1.
- Percentiles use the PR 1 codec's `histQuantile(q)`: linear interpolation between the order statistics around rank $q\,(n-1)$, within $1/64$ relative of the same interpolation over the exact values; the raw path builds the same histograms, so both paths agree exactly.
- Expected cost for the busiest production key (57.7 k events in 7 days, ≈ 3.8 k cube rows per 7 days): 7 d 20–40 ms; 30 d ≈ 16 k cube rows for each of two periods, 40–90 ms; error codes and recent requests under 10 ms; total 60–120 ms. Response 15–40 KB raw.
- Mutations use existing endpoints: `PATCH /api/api-keys/{id}` (edit, disable, enable, expiry, move with `groupId`), `POST /api/api-keys/{id}/regenerate`, `DELETE /api/api-keys/{id}`, `POST /api/account-groups/{id}/reset-usage` (the confirmation names the whole group).

Types:
KeyPageResponse
 - scope: admin|owner
 - shell: ShellStatus|absent # admin only
 - key: KeyProperties
 - period: ReportPeriod
   - start: date
   - end: date
   - timezone: string
   - previousStart: date
   - previousEnd: date
 - limits: list of KeyLimitState
 - pool: PoolRemaining|null # null when hidden from the owner
 - group: KeyGroupSummary|null
 - summary: KeyPeriodSummary
 - daily: list of KeyDay
 - byModel: list of KeyModelRow
 - errorsByCode: list of ErrorCodeRow
 - userAgents: list of UserAgentRow
 - byAccount: list of AccountCostRow|absent # admin only
 - lifetime: UsageTotals|absent # admin only
 - recentRequests: list of RequestLogEntry
 - nextCursor: string|null
KeyProperties
 - id: string
 - name: string
 - prefix: string
 - status: active|disabled|expired
 - groupId: string|null
 - groupName: string|null
 - createdAt: time
 - lastUsedAt: time|null
 - expiresAt: time|null
 - allowedModels: list of string|null
 - enforcedModel: string|null
 - enforcedReasoningEffort: string|null
 - allowedReasoningEfforts: list of string|null
 - trafficClass: string
 - serviceTier: string|null
 - usageSections: list of string
 - assignedAccounts: list of id and label|absent # admin only
 - assignedAccountCount: int
 - assignedSourceCount: int
KeyLimitState
 - type: string
 - window: string
 - modelFilter: string|null
 - current: int
 - max: int
 - percent: number
 - resetAt: time
 - expired: bool
 - projectedExhaustionAt: time|null
 - paceBasis: string # "last 3 h"
PoolRemaining
 - scope: pool|group
 - primary: WindowHealth
 - secondary: WindowHealth
 - purchasedCredits: existing group purchased-credit summary|null
KeyPeriodSummary
 - requests: int
 - inputTokens: int
 - outputTokens: int
 - cachedInputTokens: int
 - reasoningTokens: int|null
 - costUsd: number
 - errors: int
 - cancelled: int
 - ttftP50Ms: number|null
 - ttftP95Ms: number|null
 - totalP50Ms: number|null
 - totalP95Ms: number|null
 - queueP95Ms: number|null
 - tpsP50: number|null
 - previous: requests, tokens and costUsd of the previous period|null
KeyDay
 - date: date
 - byModel: list of model, requests, tokens and costUsd # top 5 plus other
 - errors: int
 - ttftP50Ms: number|null
 - ttftP95Ms: number|null
 - totalP50Ms: number|null
 - totalP95Ms: number|null
 - queueP95Ms: number|null
 - tpsP50: number|null

Extension/Polling and states:
- Poll the page endpoint every 60 s while visible only when the period includes today; "load more" requests are not polled.
- Loading: header from the directory entry at once (admin), skeletons for the sections; owner mode shows a skeleton instead of today's "Loading…".
- Error: retryable inline error keeping previous data; in owner mode a 401 ends the session as the key report does today, other failures never sign out.
- Empty: no traffic in the period → "No requests in this period" with the last-used time; no limits → "No limits configured" (distinct from zero usage); pruned days → note "latency and user agents are not kept for days before <retention cutoff>".

Extension/Removed:
- APIs page detail: the 7-day account-cost donut (now the admin cost-by-account table of the selected period), the 7-day hourly trend chart and the unlabelled 7-day versus lifetime figures (one period picker and explicit "Lifetime").
- Key report: the free-text model input (models are listed from the response), summary cards duplicated from admin Reports, and the separate `KeyReportsPage` component.
- `frontend-architecture` deltas: REMOVED "APIs tab shows a 7-day account-cost donut for selected API keys", "APIs tab account-cost donut uses existing account labels and privacy rules", "APIs tab account-cost donut follows the dashboard donut visual system", "APIs tab usage trend control layout is compact in the split view".

Requirements:

  R19:
The administrator and owner key pages SHALL be one React component in `web/src/features/key-page` with a `mode` prop; owner mode SHALL NOT mount the admin navigation or issue admin requests.

  R20:
`GET /api/api-keys/{id}/page` and `GET /api/key-reports/page` SHALL return the key page sections for one period from rollups for whole hours with the PR 1 edges, tail and journal, in a number of statements independent of the period length, and SHALL NOT mutate limit counters or reservations.

  R21:
Each limit SHALL show current, maximum and reset time, and the projected exhaustion time when the last 3 h pace reaches the maximum before the reset; expired windows SHALL show zero usage and the advanced reset.

  R22:
TTFT p50 and p95, total latency p50 and p95, queue p95 and tokens per second p50 SHALL come from the rollup histograms within the PR 1 error bound (Q6).

  R23:
Recent requests SHALL be read newest first through `idx_usage_events_key_time` with a keyset cursor and at most 100 rows per call; errors-only SHALL read at most 5 000 rows per call.

  R24:
The period picker SHALL be the only period control on the page and SHALL be reflected in the URL; the page SHALL poll only while the period includes today.

Files And Functions:
 - existing: web/src/features/key-page/components/key-page.tsx#KeyPage - `mode: "admin" | "owner"`
 - existing: web/src/features/key-page/components/key-limits-section.tsx#KeyLimitsSection - reuses `api-key-limit-usage.tsx` bars and the key report's group mini-list
 - existing: web/src/features/key-page/components/key-usage-section.tsx#KeyUsageSection - day by model chart and table
 - existing: web/src/features/key-page/components/key-errors-section.tsx#KeyErrorsSection
 - existing: web/src/features/key-page/components/key-latency-section.tsx#KeyLatencySection
 - existing: web/src/features/key-page/components/key-requests-section.tsx#KeyRequestsSection
 - existing: web/src/features/key-page/components/key-properties-section.tsx#KeyPropertiesSection
 - existing: web/src/features/key-page/components/key-admin-actions.tsx#KeyAdminActions - edit, limits, move, enable or disable, regenerate, group reset, delete, owner link
 - existing: web/src/features/key-page/hooks/use-key-page.ts#useKeyPage - 60 s poll only while the period includes today
 - existing: web/src/features/key-page/period.ts - `from`/`to` in the URL, presets, `includesToday`
 - existing: web/src/features/key-page/api.ts - admin and owner fetchers
 - existing: web/src/features/key-page/schemas.ts
 - existing: web/src/features/key-reports/key-reports-page.tsx - a thin owner wrapper around `KeyPage` with its own query cache
 - existing: web/src/features/key-reports/key-report-limits.tsx - group mini-list reused (`KeyReportGroupKeys`)
 - existing: web/src/features/apis/components/apis-page.tsx - the list only; its detail pane (`api-detail.tsx`, the account-cost donut and the trend chart) is removed and a key opens `/keys/:id`
 - existing: internal/domain/key_page.go - key page contracts and the owner projection of request rows
 - existing: internal/application/key_page.go#KeyPageService - `AdminPage`, `OwnerPage`, `Requests`; owns `KeyPageRepository`
 - existing: internal/adapters/sqlite/key_page_rollup.go#Store.KeyPageUsage - one analytics snapshot: `keyPeriodUsage` (cube rows by key with histograms, previous period from key slices), `keyErrorCodes`, `keyLimitPace`, first page of recent requests
 - existing: internal/adapters/sqlite/key_page_rollup.go#Store.KeyRecentRequests - key index, keyset cursor, pending entries, errors only
 - existing: internal/adapters/sqlite/reports_logs.go#readRequestLogPage - request-log page reader shared with the key page
 - existing: internal/adapters/httpapi/key_page.go#registerKeyPageRoutes - admin routes; `registerKeyPageOwnerRoutes` - owner routes
 - existing: internal/adapters/httpapi/key_report_session.go - mounts the owner `page` and `requests` routes and their firewall paths

Tests:
internal/adapters/sqlite/key_page_rollup_test.go
 - description: rollup results equal the raw evaluation for day, model, user agent, account and error codes, pace and recent requests across zones (UTC, Europe/Prague, Asia/Kolkata) and fold states; quantiles within the bound; equal statement count for 7 and 90 days
   requirements: R20, R22
 - description: recent requests use the key index and cursor pages equal the request log's offset pages; errors only examines at most 5 000 rows per call
   requirements: R23
internal/application/key_page_test.go
 - description: pace projection before and after reset, expired window shows zero, only read ports exist and are called
   requirements: R20, R21
internal/adapters/httpapi/key_page_test.go
 - description: admin routes need a session, unknown and internal keys are 404, cursor paging over the route matches the request log's offset paging, errors only
   requirements: R23
web/src/features/key-page/components/key-page.test.tsx
 - description: one period picker in the URL, polling only for periods including today, admin sections hidden in owner mode
   requirements: R19, R24

### 4.1 Key page data in owner scope
owner session read --revalidate the key and apply the firewall allowlist--> authorized owner key --compute the key page--> full key page data --drop account identities addresses messages and admin sections--> owner key page data

Requirements:

  R25:
Owner routes SHALL bind every section to the authenticated key, SHALL accept only `start_date`, `end_date` and `timezone` (page) and `cursor`, `limit` and `errors_only` (requests), and SHALL reject any other parameter with 400 `invalid_report_filters`.

  R26:
Owner responses SHALL omit account ids, e-mails and plan types, client IPs, full user-agent strings, conversation ids, error messages, cost by account, lifetime totals and assigned account ids, and SHALL include request ids, error codes and user-agent groups (Q5).

  R27:
The pool or group remaining section SHALL appear in owner scope only when upstream quotas are visible to the key: for an ungrouped key `HideUpstreamQuotaFromKeys` is off and the key's usage sections include `account_pool_usage`; for a grouped key the existing group-quota visibility rule holds.

  R28:
Owner routes SHALL keep the key-report session rules of `go-runtime` (cookie scope, no-store caching, firewall allowlist, revalidation on every read, no mutation, no quota consumption) and SHALL be gzip-compressed when the client accepts it.

  R29:
The sign-in entry SHALL keep the `from` and `to` query parameters through key sign-in, so that a link copied from the admin key page or from Reports opens the owner page for that period; the link SHALL NOT contain or grant any credential (Q4).

Tests:
internal/adapters/httpapi/key_page_owner_test.go
 - description: key A cannot read key B through any parameter; unknown parameters are rejected; omitted fields are absent from the JSON; owner reads leave limits, reservations, events and keys unchanged
   requirements: R25, R26
 - description: hidden upstream quotas remove the pool section for grouped and ungrouped keys
   requirements: R27
 - description: firewall denial; disabled, expired, regenerated or deleted key mid-session; no-store, `Vary: Cookie`, `Server-Timing` and gzip
   requirements: R28
web/src/features/auth/components/auth-gate.test.tsx
 - description: `from` and `to` survive key sign-in and select the owner page period
   requirements: R29

### 5. Reports for API-key users
admin opens Reports --choose groups or keys and a period--> report selection in the URL --GET /api/reports/keys--> per-group and per-key report --export CSV or copy an owner link--> file or link --expand traffic details--> lazily loaded model user-agent and latency report

Extension/Purpose:
Reports about the users of API keys: choose groups or keys and a period, then read totals, per-group and per-key breakdowns and daily spend, export CSV and share a link to each key owner's page. Delivered in PR 3.

Extension/Screen:
1. Selection bar: `ScopePicker` (searchable multi-select over directory groups and keys; selecting a group selects its current member keys; chips with counts; "All keys"; "Ungrouped keys"), period picker (Today, Yesterday, 7 d, 30 d, 90 d, This month, Last month, custom up to 730 days), time zone label, Copy link (selection and period in the URL), Export CSV.
2. Summary: requests, input/output/cached tokens, cost, errors and error rate, active keys, comparison with the previous period of the same length.
3. Groups table, shown when the selection spans more than one group: group, keys active/selected, requests, tokens, cost, share, errors.
4. Keys table (`PerKeyTable`), sortable and grouped by group with subtotals: key (link to `/keys/:id`), group, requests, input/output/cached tokens, cost, share, errors and error rate, last used, fullest current limit; row actions Open and Copy owner link.
5. Daily chart: cost per local day stacked by the top 8 keys, or by group when several groups are selected, plus other; toggle cost, requests or tokens.
6. Traffic details, collapsed and loaded on expand: model and user-agent distributions and daily latency medians for the selection from the existing `GET /api/reports` with `api_key_id` filters when at most 50 keys are selected, unfiltered when "All keys" is selected, otherwise a prompt to narrow the selection.

Events And Endpoints:
- `GET /api/reports/keys?group_id=…&api_key_id=…&ungrouped=1&start_date&end_date&timezone` (new, admin, gzip, `Server-Timing`; ids repeatable; an empty selection means all keys; at most 731 days; default the 7 local days ending today) → `KeyUsageReport`. Any other parameter, a blank or longer than 256-character id, more than 200 group or 1 000 key ids or `ungrouped` other than `1`/`0` → 400 `invalid_report_filters`; malformed, inverted or longer than 731-day dates → 400 `invalid_report_date_range`; unknown zone → 400 `invalid_timezone`; resolved keys × days above 250 000 → 400 `report_too_large` (bounds the per-key day sums held in memory; 300 keys fit 731 days).
- `GET /api/reports/keys/export.csv?<same parameters>` (new, admin, `Server-Timing`, not compressed) → `text/csv; charset=utf-8` with `Content-Disposition: attachment; filename="key-usage-<start>_<end>.csv"`, one row per local day and key with non-zero requests: `date,group_id,group_name,key_id,key_name,requests,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,errors,cancelled`, ordered by date, group name with ungrouped keys last, key name and id; `reasoning_tokens` empty when unknown, `cost_usd` with six decimals, text cells starting with `=`, `+`, `-`, `@`, tab or CR prefixed with `'`. The rows are computed in one analytics snapshot that is closed before the first byte is written.
- Sources, in one analytics read transaction (9 statements in whole-hour zones, 10 in fractional ones, at any number of keys and days): group names, current keys and their active limits; folded legacy hours of the read window (`legacy_hourly_usage`, filtered by the resolved keys for a strict selection); raw partial hours and fractional-zone split hours summed per key in SQL after cutting them at the day bounds; images of the unfolded tail and the journal; and one `usage_rollup_slice` kind `key` statement whose hour and UTC-day bucket lists travel as two JSON arrays (`json_each`) driving primary-key lookups. Each local day is the UTC-day row of its midpoint minus the hour rows of that UTC day outside it plus its rollup hour rows outside it, whenever that reads fewer rows than its hour rows (also in fractional zones, whose raw split hours stay with the images); the previous period reads UTC-day rows for whole UTC days and hour rows for the rest. `usage_rollup_total_hour` is not read: every figure sums the resolved keys, so the CSV, the keys table and the totals agree.
- Expected cost at 300 keys: 30 d ≈ 300 × 30 × 2–3 slice rows (Europe/Prague), 90 d ≈ 300 × 90 × 2–3 plus the previous period's day rows; see Implementation notes (PR 3b) for the measurements.
- The page no longer calls `/api/accounts` or `/api/request-logs/options`; key and group names come from the directory.

Types:
KeyUsageReport
 - generatedAt: time
 - period: ReportPeriod
   - start: date
   - end: date
   - timezone: string
   - previousStart: date
   - previousEnd: date
   - days: int
 - selection: ReportSelection
   - groupIds: list of string
   - keyIds: list of string
   - ungrouped: bool
   - allKeys: bool # empty selection
   - resolvedKeyCount: int
 - summary: UsageMeasures
 - previous: UsageMeasures|null # null when the previous period has no requests
 - activeKeys: int
 - seriesBy: key|group # group when the selection names several groups (ungrouped counts as one) or is empty and the keys span several groups
 - series: list of ReportSeries # top 8 by cost plus other; daily[].series follows this order
   - id: string|null
   - name: string
   - kind: key|group|ungrouped|other
 - groups: list of GroupReportRow # groups of the resolved keys and selected groups, by cost
   - id: string|null # null is the ungrouped keys
   - name: string
   - keyCount: int
   - activeKeys: int
   - measures: UsageMeasures
   - share: number # of the summary cost, 0..1
 - keys: list of KeyReportRow # every resolved key, by cost
   - id: string
   - name: string
   - prefix: string
   - groupId: string|null
   - groupName: string|null
   - status: active|disabled|expired
   - measures: UsageMeasures
   - share: number
   - lastUsedAt: time|null
   - maxLimitPercent: number|null
 - daily: list of ReportDay # every local day of the period
   - date: date
   - total: UsageMeasures
   - series: list of requests, tokens (input plus output) and costUsd
UsageMeasures
 - requests: int
 - inputTokens: int
 - outputTokens: int
 - cachedInputTokens: int
 - reasoningTokens: int|null
 - costUsd: number # rounded to six decimals
 - errors: int
 - cancelled: int

Extension/Polling and states:
- No interval polling; refetch on focus only when the period includes today and the data are older than 60 s.
- Loading: skeleton tables; the selection bar stays usable.
- Error: retryable inline error keeping the previous report.
- Empty: no selection → "All keys" is used; no traffic → "No usage in this period"; a selected group without keys → "This group has no keys".

Extension/Removed:
- The account filter and the unused `byAccount` payload; free-text model and user-agent filters and the second "catalog" report; the queue-wait chart (≈ 0 in normal operation); key names from `/api/request-logs/options`; the duplicate `/api/accounts` request; the 60 s polling of a full report.
- `frontend-architecture` deltas: REMOVED "Reports exposes a persisted line-chart visibility filter", "Reports safely persists visibility", "Visibility does not narrow data fetching", "Reports page exposes a visible user-agent filter"; MODIFIED "Reports page exposes visible filter controls" and "Reports page loads report data from the reports endpoint" to the selection bar and `/api/reports/keys`; the distribution, daily-chart and time-zone requirements stay and apply to traffic details.

Requirements:

  R30:
`GET /api/reports/keys` SHALL resolve groups to their current member keys, unite them with the explicit keys and the ungrouped set, and SHALL return totals, per-group and per-key measures and a daily series equal to the raw evaluation except the documented cost rounding, from rollup slices with a statement count independent of the number of keys and days.

  R31:
The CSV export SHALL contain one row per local day and key with non-zero requests in the selection, and its sums SHALL equal the JSON report for the same parameters.

  R32:
The Reports page SHALL encode the selection and the period in the URL, SHALL offer a copy-owner-link action per key that follows R29, SHALL load traffic details only when expanded and SHALL NOT poll on an interval.

Files And Functions:
 - existing: web/src/features/reports/components/reports-page.tsx - rebuilt around the selection
 - implemented: web/src/features/reports/components/scope-picker.tsx#ScopePicker - at most 100 rendered matches, keyboard navigation
 - implemented: web/src/features/reports/components/period-picker.tsx#PeriodPicker
 - implemented: web/src/features/reports/components/key-report-summary.tsx#KeyReportSummary
 - implemented: web/src/features/reports/components/per-key-table.tsx#PerKeyTable - grouped with subtotals, idle keys hidden, 20 rows per group before "Show all"
 - implemented: web/src/features/reports/components/group-report-table.tsx#GroupReportTable
 - implemented: web/src/features/reports/components/key-daily-chart.tsx#KeyDailyChart
 - implemented: web/src/features/reports/components/traffic-details.tsx#TrafficDetails - lazy, reuses the distribution donuts and latency charts
 - implemented: web/src/features/reports/report-url.ts - URL state `group`, `key`, `ungrouped=1`, `period` or `from`/`to`
 - existing: web/src/features/reports/components/model-distribution-donut.tsx, useragent-distribution-donut.tsx, time-to-first-token-chart.tsx, tokens-per-second-chart.tsx - reused in traffic details
 - existing: web/src/features/reports/components/reports-summary-cards.tsx, daily-detail-table.tsx, cost-per-day-chart.tsx, tokens-per-day-chart.tsx - no longer used by Reports; kept while `features/key-reports` imports them (removed with it in PR 3a)
 - implemented: web/src/features/reports/hooks/use-key-usage-report.ts#useKeyUsageReport
 - implemented: internal/domain/key_usage_report.go - `KeySelection.Resolve`, report types and bounds
 - implemented: internal/application/key_usage_report.go#KeyUsageReportService.Report, #KeyUsageReportService.Export - owns `KeyUsageReportRepository`
 - implemented: internal/adapters/sqlite/key_usage_report_rollup.go#Store.KeyUsageReport - rollup reader and raw fallback; the export reuses it (no separate `StreamKeyDailyRows`)
 - implemented: internal/adapters/httpapi/reports_keys.go#registerKeyUsageReportRoutes

Tests:
internal/adapters/sqlite/key_usage_report_rollup_test.go
 - description: per-key, per-day and previous-period measures equal the raw evaluation with legacy rows, an escaped key dimension, keys moved between groups, the ungrouped selection and six zones including Europe/Prague days, in every fold state; raw days equal the admin report filtered by each key
   requirements: R30
 - description: statement count equal for 30 and 300 keys and for 7 and 90 days; 30-day report at 300 keys under 300 ms (also R49)
   requirements: R30
internal/adapters/httpapi/reports_keys_test.go
 - description: CSV rows sum to the JSON report; selection resolution, validation codes, gzip, Server-Timing and authentication (also R50)
   requirements: R30, R31
web/src/__integration__/reports-flow.test.tsx
 - description: selection and period round-trip through the URL, owner link copied, traffic details requested only on expand, no interval polling
   requirements: R32

### 6. Keys page for hundreds of keys
admin opens the keys page --GET /api/keys with search filters sort and page--> one page of key rows --select rows and choose a bulk action--> confirmed bulk request --apply in short write jobs--> per-key results --refetch the page--> updated key list

Extension/Purpose:
Find and manage hundreds of keys: search, filter, sort by spend and limit state, act on many keys at once. Delivered in PR 4.

Extension/Screen:
1. Toolbar: search (name, prefix); filters group (any, one group, ungrouped), status (active, disabled, expired), near limit (≥ 90 %), expiring within 7 days, inactive for 7, 30 or 90 days; Create key; result count.
2. Table (`KeysTable`), 50 rows per page, sticky header, row checkboxes: name and prefix, group, status, spend today, 7 d and 30 d (sortable), fullest limit with reset (sortable), limits summary, expiry, last used (sortable), created; a row click opens `/keys/:id`; row menu Edit, Disable or Enable, Regenerate, Delete.
3. Bulk bar when rows are selected (`BulkActionsBar`): Disable, Enable, Move to group (picker including "no group"), Set limits (ungrouped keys only, `LimitRulesEditor`), Clear; "Select all n matching" extends the selection to the filter result (at most 1 000 ids).
4. Pagination: previous and next, page n of m, total.

Events And Endpoints:
- `GET /api/keys?q&group=<id|ungrouped>&status=active|disabled|expired&near_limit=1&expiring_days=7&inactive_days=7|30|90&sort=name|created|spend_today|spend_7d|spend_30d|limit_fill|last_used&order=asc|desc&offset&limit&timezone` (new, admin, gzip; `limit` ≤ 100, default 50) → `KeysPage`.
- Server: one query over `api_keys` joined to group names, one over active `api_key_limits`, per-key spend today, 7 d and 30 d from `usage_rollup_slice` kind `key` (use cases 2.1 and 5), then filter, sort and page in memory with a stable tie-break by id; `total` is counted before paging. Expected cost at 300 keys: 40–80 ms; 50 rows ≈ 25 KB raw.
- `GET /api/keys/ids?<same filters>` (new) → the ids of all matching keys, at most 1 000, for "Select all matching".
- `POST /api/keys/bulk` (use case 6.1).
- After this PR the UI no longer calls `GET /api/api-keys/` (use case 9).

Types:
KeysPage
 - shell: ShellStatus
 - generatedAt: time
 - timezone: string
 - total: int
 - offset: int
 - limit: int
 - rows: list of KeyRow
KeyRow
 - id: string
 - name: string
 - prefix: string
 - groupId: string|null
 - groupName: string|null
 - status: active|disabled|expired
 - expiresAt: time|null
 - lastUsedAt: time|null
 - createdAt: time
 - spendTodayUsd: number
 - spend7dUsd: number|null # null while the rollups are unusable
 - spend30dUsd: number|null # null while the rollups are unusable
 - maxLimit: LimitState|null
 - limitsSummary: list of type, window, modelFilter and max

Extension/Polling and states:
- Poll the current page every 60 s while visible; the selection survives polls and page changes.
- Loading: table skeleton with the toolbar usable; error: retryable inline error keeping the previous page; empty: "No keys match these filters" with a reset-filters action, or "Create your first key" without keys.

Extension/Removed:
- `ApiKeysOverview` lifetime cards and the lifetime "cost by key" and "tokens by key" panels: replaced by sortable spend columns with explicit periods.
- The split list and detail layout of the APIs page and the key table in Settings (`ApiKeysSection`).
- `frontend-architecture` delta: REMOVED "API key overview SHALL show lifetime usage aggregates"; MODIFIED "Settings page" without the key and group sections.

Requirements:

  R33:
`GET /api/keys` SHALL filter, sort and page on the server with a stable tie-break by id and a statement count independent of the number of keys.

  R34:
"Inactive for N days" SHALL mean `last_used_at` absent or older than N days; "near limit" SHALL mean an active, unexpired limit at or above 90 %; "expiring within N days" SHALL mean `now < expires_at ≤ now + N days`; status `expired` SHALL mean `expires_at ≤ now`; deleted and internal keys SHALL never be listed.

  R35:
The keys table SHALL poll only its current page, every 60 s while visible, and SHALL keep the selection across polls and pages.

Files And Functions:
 - existing: web/src/features/keys/components/keys-page.tsx#KeysPage
 - existing: web/src/features/keys/components/keys-table.tsx#KeysTable
 - existing: web/src/features/keys/components/keys-filters.tsx#KeysFilters
 - existing: web/src/features/keys/components/bulk-actions-bar.tsx#BulkActionsBar
 - existing: web/src/features/keys/hooks/use-keys-list.ts#useKeysList - URL-backed query state
 - existing: web/src/features/api-keys/components/limit-rules-editor.tsx, api-key-create-dialog.tsx, api-key-edit-dialog.tsx - reused
 - existing: web/src/features/api-keys/components/api-key-edit-loader.tsx#ApiKeyEditLoader - edit dialog of one key, shared with the key page
 - removed: web/src/features/apis/components/apis-page.tsx
 - existing: internal/application/keys_list.go#KeysListService.List
 - existing: internal/adapters/sqlite/keys_list.go#Store.KeyList - keys, limits, groups and per-key day costs in one analytics snapshot
 - existing: internal/adapters/httpapi/keys_list.go#registerKeysRoutes - GET /api/keys, GET /api/keys/ids, POST /api/keys/bulk

Tests:
internal/application/keys_list_test.go
 - description: every filter and sort with ties, inactive and expiry boundaries, missing values last, paging totals, ids capped at 1 000
   requirements: R33, R34
internal/adapters/sqlite/keys_list_test.go
 - description: statement count equal for 30 and 300 keys; costs equal the raw evaluation; deleted and internal keys excluded; raw fallback reads today only
   requirements: R33, R34
internal/adapters/httpapi/keys_list_test.go
 - description: route parameters, paging totals, gzip, Server-Timing and administrator authentication
   requirements: R33
web/src/__integration__/keys-flow.test.tsx
 - description: filters in the URL, overview links, selection kept across a poll and a page change
   requirements: R35

### 6.1 Bulk key actions
bulk request with ids and an action --validate ids and the action--> accepted bulk request --apply in short write jobs--> [every key changed, some keys failed] --report per-key results--> bulk result

Requirements:

  R36:
`POST /api/keys/bulk` SHALL accept `{ids, action, groupId?, limits?}` with at most 1 000 ids and `action` one of `disable`, `enable`, `move_group`, `set_limits`, SHALL report unknown, deleted and internal ids as per-key failures, and SHALL return `{results: [{id, ok, code?}]}` in request order.

  R37:
`disable` and `enable` SHALL run in write jobs of at most 25 keys; `move_group` and `set_limits` SHALL run one key per write job because each saved key may backfill its new limit windows; every job SHALL be atomic and a failed job SHALL NOT undo earlier jobs.

  R38:
`move_group` SHALL apply the target group's limits to each moved key exactly as saving that key does today; `set_limits` SHALL fail with `key_in_group` for grouped keys.

  R39:
Bulk actions SHALL require a confirmation naming the action and the number of keys, and SHALL refresh the keys list, the directory and the overview afterwards.

Files And Functions:
 - existing: internal/application/keys_bulk.go#KeysBulkService.Apply
 - existing: internal/adapters/httpapi/keys_list.go#keysBulkRequest - POST /api/keys/bulk body
 - existing: internal/adapters/sqlite/keys_bulk.go#Store.SetAPIKeysActive, #Store.MoveAPIKeyToGroup, #Store.SetAPIKeyLimits - per-job writes with the limit synchronisation of `SaveAPIKey`
 - existing: internal/adapters/sqlite/keys.go#SaveAPIKey - its `groupRulesTx` and `syncKeyLimitsTx` are reused

Tests:
internal/application/keys_bulk_test.go
 - description: partial failure in the middle of a move keeps earlier keys moved and reports the failed and later keys correctly; `set_limits` on a grouped key fails with `key_in_group`; jobs of 25; cancellation
   requirements: R36, R37, R38
internal/adapters/sqlite/keys_bulk_test.go
 - description: a bulk move and bulk limits leave the same limits and scope as saving the key; unknown, deleted and internal keys are not found
   requirements: R36, R38
web/src/features/keys/components/bulk-actions-bar.test.tsx
 - description: confirmation text, per-key failures shown, list, directory and overview invalidated
   requirements: R39

### 7. Groups page
admin opens the groups page --GET /api/groups/summary--> group rows with membership limits spend and projections --open a group--> group detail sheet --edit members or limits or reset the period--> saved group and refetched summary

Extension/Purpose:
Manage account groups and see each group's membership, limits, spend and projected exhaustion in one table. Delivered in PR 4.

Extension/Screen:
1. Toolbar: search, Create group (opens the sheet empty), refresh. A pool row on top: whole-pool health, 5 h and weekly remaining with projections, spend today, 7 d and 30 d of all reported traffic, keys active/total; beside it the pool runway card (Q2: headroom, runway, next relief, reset ticks, throttle-to and add-Pro recommendations from `GET /api/dashboard/projections`).
2. Groups table: name; accounts (count and health bar); group limits (rule summary such as "50 USD / week · 5 M tokens / day"); keys active/total; limit fill (highest %, n ≥ 90 %); spend today, 7 d and 30 d; 5 h and weekly remaining with next reset; projected exhaustion. Every column sortable (unknown values last; a third click restores the default); default order as in use case 2.1. The ungrouped row links to `/keys?group=ungrouped` and opens no sheet.
3. Group detail sheet (`GroupDetailSheet`, `/groups?selected=<id>`): name; members (`GroupMembersSelect`, a filterable checklist of every non-deleted account with its health and 5 h and weekly remaining from the summary); limits (`LimitRulesEditor`); one Save sends name, members and limits in one `PUT /api/account-groups/{id}` (atomic, member keys re-synced as today); keys of the group (top 20 by 7-day spend with a link to `/keys?group=<id>`); Reset period (existing confirmation); Delete (refused while keys reference the group, as today, shown in the confirmation).

Events And Endpoints:
- `GET /api/groups/summary?timezone` (new, admin, gzip, `Server-Timing`) → `GroupsSummary`. The same computation as the overview group rows (use case 2.1, `assembleOverview`) plus 30-day spend, the limit templates from `group_limits` (one query in the entity snapshot) and member account ids. The 30 local days are the overview's 8 days plus the span of the 22 days before them, read per key in the same analytics snapshot from UTC-day key slices with hour rows at the edges (an edge longer than half a day reads its UTC-day row minus the hour rows outside), folded legacy hours, raw split hours, the tail and the journal. Expected cost at 50 groups / 300 keys / 100 accounts: 70–150 ms.
- `GET /api/dashboard/projections` (existing, unchanged) feeds the pool runway card, requested once per visit.
- Mutations: existing `POST /api/account-groups/`, `PUT /api/account-groups/{id}`, `DELETE /api/account-groups/{id}`, `POST /api/account-groups/{id}/reset-usage`, `PUT /api/accounts/{id}/groups`.

Types:
GroupsSummary
 - generatedAt: time
 - timezone: string
 - shell: ShellStatus
 - pool: GroupSummaryRow # every ChatGPT account and key; spend of all reported traffic; attention from whole-pool alerts; no topKeys7d
 - groups: list of GroupSummaryRow # the overview's group rows and the ungrouped row, in its order
 - accounts: list of GroupAccount # every non-deleted account, for the member picker
GroupSummaryRow
 - (every field of GroupRow, flattened)
 - spend30dUsd: number|null # null while the rollups are unusable
 - limits: list of LimitTemplate # empty for the ungrouped row and the pool
 - accountIds: list of string # non-deleted members; empty for the ungrouped row and the pool
 - topKeys7d: list of GroupKey # at most 20 by 7-day spend, then spend today, then name
LimitTemplate
 - limitType: string
 - limitWindow: string
 - modelFilter: string|null
 - maxValue: int
GroupKey
 - id: string
 - name: string
 - prefix: string
 - status: active|disabled|expired
 - spendTodayUsd: number
 - spend7dUsd: number|null
 - spend30dUsd: number|null
 - maxLimitPercent: number|null
 - lastUsedAt: time|null
GroupAccount
 - id: string
 - label: string
 - email: string
 - kind: chatgpt|external
 - status: string
 - health: ok|low|limited|reauth|paused|off
 - primaryRemainingPercent: number|null
 - secondaryRemainingPercent: number|null

Extension/Polling and states:
- Poll every 60 s while visible (the response carries the shell status, so `/groups` has no status poll); the projections once per visit; the open sheet keeps its draft across polls.
- Loading: table skeleton; error: retryable inline error keeping previous data; empty: "No groups yet; ungrouped keys use the whole pool" with Create group.

Extension/Removed:
- `AccountGroupsSection` in Settings and the per-account group checkboxes as the only membership editor (the account sheet keeps its editor); `ApiKeysSection` leaves Settings, which keeps an API-key access section with the authentication and quota-privacy toggles; the appearance settings for the dashboard refresh rate and the account burn projection.
- `frontend-architecture` deltas: MODIFIED "Settings page", "API keys settings expose quota privacy toggle" and "Settings page renders in the active locale"; `go-runtime` deltas: MODIFIED "Replaced dashboard routes redirect" (`/groups` is the page, `/settings#account-groups` redirects) and "Replaced admin endpoints are deprecated for one release" (`/api/dashboard/projections` stays for the runway card).

Requirements:

  R40:
The groups summary SHALL show for every group its member accounts with health, its limit templates, key counts, limit fill, spend today, 7 d and 30 d, window remaining with next reset and projected exhaustion, computed by the same functions as the overview group rows.

  R41:
Once the Groups and Keys pages exist, Settings SHALL no longer contain the key and group sections and SHALL keep the global API-key authentication and upstream-quota privacy settings.

Files And Functions:
 - existing: web/src/features/groups/components/groups-page.tsx#GroupsPage
 - existing: web/src/features/groups/components/groups-table.tsx#GroupsTable - sorting in web/src/features/groups/groups-sort.ts#sortGroupRows
 - existing: web/src/features/groups/components/group-detail-sheet.tsx#GroupDetailSheet
 - existing: web/src/features/groups/components/group-members-select.tsx#GroupMembersSelect
 - existing: web/src/features/groups/components/pool-row.tsx#PoolRow
 - existing: web/src/features/groups/components/pool-runway-card.tsx#PoolRunwayCard
 - existing: web/src/features/groups/hooks/use-groups-summary.ts#useGroupsSummary, #usePoolRunway
 - existing: web/src/features/account-groups/hooks/use-account-groups.ts#useAccountGroupMutations - mutations without the group list
 - existing: web/src/features/settings/components/settings-page.tsx - sections removed, `#account-groups` redirect
 - existing: web/src/features/settings/components/api-key-access-settings.tsx#ApiKeyAccessSettings
 - existing: internal/domain/groups_summary.go - `GroupsSummary`, `GroupSummaryRow`, `LimitTemplate`, `GroupKey`, `GroupAccount`
 - existing: internal/application/groups_summary.go#GroupsSummaryService.Summary - reuses the overview assembly (`assembleOverview`)
 - existing: internal/adapters/sqlite/groups_summary_rollup.go#Store.GroupsSummaryEntities, #Store.GroupsSummaryUsage
 - existing: internal/adapters/httpapi/groups_summary.go#registerGroupsSummaryRoutes

Tests:
internal/application/groups_summary_test.go
 - description: rows equal the overview rows for the same snapshot; 30-day spend; templates and members
   requirements: R40
internal/adapters/sqlite/groups_summary_rollup_test.go
 - description: the span equals the raw evaluation in every fold state and zone; statement count equal for 5 and 50 groups; rows equal the overview's and 30-day spend equals the raw sum
   requirements: R40
web/src/__integration__/groups-flow.test.tsx
 - description: edit members and limits from the sheet, reset period confirmation, Settings without key and group sections
   requirements: R40, R41

### 8. Accounts page focused on management
admin opens the accounts page --GET /api/accounts from batch reads--> account table without provider calls --act inline on re-login or pause or priority--> mutation and refreshed row --open an account--> detail sheet with quota history and labelled periods --press Check now--> on-demand provider refresh

Extension/Purpose:
Manage accounts quickly: see status, remaining and resets per account, re-login, pause and change priority inline, without provider calls when browsing. Delivered in PR 4.

Extension/Screen:
1. Toolbar: search (alias, e-mail, id), status filter, group filter, sort (status severity, next reset, 5 h remaining, weekly remaining, name), Add account (OAuth or import).
2. Table (`AccountsTable`): label and e-mail, plan, status with reason, groups (chips), 5 h and weekly (and monthly when present) remaining with reset times, purchased credits, reset credits available (snapshot), warm-up, priority select (burn first, normal, preserve) inline, inline actions Re-login, Pause or Resume, menu (Probe, Export auth, Redeem reset credit, Delete).
3. Detail sheet (`AccountDetailSheet`): alias editing in the header; quota windows with resets; 7-day quota chart (existing trends); usage "Last 7 days" (requests, tokens, cost from account slices) and "Lifetime" (`usage_totals`); reset credits "available N as of HH:MM" with Check now; token information; groups editor; routing and trusted-access settings.

Events And Endpoints:
- `GET /api/accounts` (existing, additive) is built from batch reads (accounts, quotas, credit status, additional quotas, memberships, usage totals, refusal times) instead of per-account queries, decrypts credentials only for the token fields it returns, and adds `groups` (ids), `usage7d` (`requests`, `totalTokens`, `costUsd` from `usage_rollup_slice` kind `account`, day grain) and `shell`. Expected cost at 100 accounts: 10–25 ms. Polled every 30 s while visible.
- `GET /api/accounts/{id}/trends` (existing, refetched every 5 min while the sheet is open).
- Reset credits: the table shows the in-memory snapshot (`availableResetCredits`, `GET /api/accounts/{id}/rate-limit-reset-credits`); `GET /api/accounts/{id}/usage-reset-credits` (provider call) runs only on Check now.
- Existing mutations: `PUT /api/accounts/{id}/routing-policy`, `POST /api/accounts/{id}/pause`, `POST /api/accounts/{id}/reactivate`, `PUT /api/accounts/{id}/alias`, `PUT /api/accounts/{id}/groups`, `PUT /api/accounts/{id}/limit-warmup`, `POST /api/accounts/{id}/probe`, `POST /api/accounts/{id}/export/auth`, `DELETE /api/accounts/{id}`, the OAuth endpoints.

Extension/Polling and states:
- One poll (`/api/accounts`, 30 s); trends only while a sheet is open; no provider request on load or selection.
- Loading: table skeleton; error: retryable inline error keeping previous data; empty: "No accounts" with Add account.

Extension/Removed:
- The live reset-credit refresh on every selection (one number already in the snapshot); the unlabelled "Request logs total"; the list-and-detail split that hid most accounts behind one selection.
- `frontend-architecture` deltas: MODIFIED "Accounts page", "Accounts list surfaces quota reset timing", "Accounts list orders by next reset", "Accounts list supports explicit sort modes", "Accounts list uses available tall-viewport space" to the table layout; the add, import, alias, export, delete and usage-reset requirements stay.

Requirements:

  R42:
Opening the Accounts page or an account SHALL NOT call a provider; provider refreshes SHALL happen only on Check now, Probe or Re-login.

  R43:
`GET /api/accounts` SHALL use a number of statements independent of the number of accounts and SHALL keep every existing field.

  R44:
Every usage figure on the Accounts page SHALL carry an explicit period label ("Last 7 days", "Lifetime", or the window name with its reset time).

  R45:
Priority, pause or resume and re-login SHALL be available inline in the table and SHALL update the row after the mutation without reloading the page.

Files And Functions:
 - existing: web/src/features/accounts/components/accounts-page.tsx - table layout
 - planned: web/src/features/accounts/components/accounts-table.tsx#AccountsTable
 - planned: web/src/features/accounts/components/account-detail-sheet.tsx#AccountDetailSheet
 - existing: web/src/features/accounts/components/account-usage-panel.tsx - Check now instead of the selection-time refresh
 - existing: web/src/features/accounts/components/account-actions.tsx, account-groups-editor.tsx, oauth-dialog.tsx - reused
 - existing: internal/adapters/httpapi/admin.go#listAccounts - batch-built view
 - planned: internal/adapters/sqlite/accounts_batch.go#Store.ListAccountViews

Tests:
internal/adapters/httpapi/accounts_list_test.go
 - description: statement count equal for 9 and 100 accounts; every existing field still present
   requirements: R43
web/src/__integration__/accounts-flow.test.tsx
 - description: no `usage-reset-credits` request on load or selection, only on Check now; inline priority, pause and re-login update the row; period labels present
   requirements: R42, R44, R45

### 9. Endpoint compatibility and deprecation
endpoint touched by the redesign --classify its consumers--> [public key-usage contract --keep unchanged in every release--> stable endpoint, endpoint the embedded UI stops calling --keep unchanged for one release then remove--> removed endpoint, endpoint a new page extends --add optional fields only--> extended endpoint]

Data:
endpoint fate:

| Endpoint | Consumer after the redesign | Fate |
|---|---|---|
| `GET /v1/usage`, `GET /api/codex/usage`, `GET /v1/usage/reports` | key holders' tools | unchanged parameters, fields, authentication, caching and encoding; pinned by contract tests |
| `GET /api/key-reports/reports` | owner tabs opened before PR 3 | deprecated in PR 3, removed in the release after PR 4 |
| `GET /api/dashboard/overview` | tabs opened before PR 2 | deprecated in PR 2, removed in the release after PR 4 |
| `GET /api/dashboard/projections` | the pool runway card if kept (Q2) | kept with the card, else deprecated with the overview |
| `GET /api/dashboard/status` | shell status on pages without a poll | kept, additive fields |
| `GET /api/reports` | Reports traffic details | kept unchanged, including `byAccount` |
| `GET /api/request-logs`, `/api/request-logs/options`, `/api/conversations`, `/api/conversations/{id}` | Requests page | kept unchanged |
| `GET /api/api-keys/` | tabs opened before PR 4, scripts | deprecated in PR 4, removed in the release after PR 4 (Q8) |
| `GET /api/api-keys/{id}/trends`, `GET /api/api-keys/{id}/usage-7d` | tabs opened before PR 3 | deprecated in PR 3, removed in the release after PR 4 |
| `GET /api/accounts` | Accounts | kept, additive fields |
| `GET /api/accounts/{id}/trends`, `/usage-reset-credits`, `/rate-limit-reset-credits` | Accounts | kept |
| `GET /api/account-groups/` | group pickers until PR 4 | kept |
| `/api/overview`, `/api/directory`, `GET /api/api-keys/{id}`, `/api/api-keys/{id}/page`, `/api/api-keys/{id}/requests`, `/api/key-reports/page`, `/api/key-reports/requests`, `/api/reports/keys`, `/api/reports/keys/export.csv`, `/api/keys`, `/api/keys/ids`, `/api/keys/bulk`, `/api/groups/summary` | new pages | added |

Requirements:

  R46:
`GET /v1/usage`, `GET /api/codex/usage` and `GET /v1/usage/reports` SHALL keep their request parameters, response fields, authentication, caching headers and response encoding unchanged.

  R47:
An admin endpoint that the embedded UI stops calling SHALL stay unchanged in the release that ships its replacement and SHALL be removed no earlier than the following release; release notes SHALL list each deprecation and removal.

  R48:
New admin endpoints SHALL be registered behind `requireAdmin` and `compressJSON` with the existing trailing-slash conventions; new owner endpoints SHALL live under `/api/key-reports/` so that the report cookie path covers them.

Logic Details:
- Why one release: a browser tab opened before an update keeps running the previous JavaScript against the new binary after a seamless switch, so an endpoint removed in the same release would fail in that tab until a reload.
- Old endpoints keep their PR 1 rollup read paths, so keeping them costs nothing at runtime.

Tests:
web/src/__integration__/go-runtime.test.ts
 - description: contract snapshots of the three public key-usage endpoints are unchanged; every new endpoint requires an admin session or the report cookie as specified
   requirements: R46, R48

### 10. Latency budget and verification
page endpoint --measure server time on a synthetic store at target scale and on a production copy--> [within 300 ms, over budget] --record Server-Timing and benchmark results--> verified page

Data:
expected server cost at 300 keys, 50 groups and 100 accounts (est., modernc, warm):

| Page | Main endpoint | Poll | Rollup sources | Est. |
|---|---|---|---|---|
| Overview | `GET /api/overview` | 30 s | slice `key`, total_hour, error_hour, quota history | 60–150 ms |
| Requests | `GET /api/request-logs` 24 h | Live only, 30 s | log cube totals, raw page by `idx_usage_events_time_id` | 5–30 ms |
| Key page | `GET /api/api-keys/{id}/page`, `GET /api/key-reports/page` 30 d | 60 s when today is included | cube by key, error_hour by key, slice `key` | 60–120 ms |
| Reports | `GET /api/reports/keys` 90 d | none | slice `key`, total_hour, legacy hourly | ≤ 150 ms |
| Keys | `GET /api/keys` | 60 s | slice `key` | 40–80 ms |
| Groups | `GET /api/groups/summary` | 60 s | slice `key`, quota history | 70–150 ms |
| Accounts | `GET /api/accounts` | 30 s | slice `account` | 10–25 ms |
| Shell | `GET /api/dashboard/status` | 60 s where no page poll | none | < 5 ms |

Requirements:

  R49:
With usable rollups, the main endpoint of each page in the table SHALL answer in under 300 ms of server time, warm, on a synthetic store with 300 keys, 50 groups, 100 accounts and at least 1.5 million events over 30 days, and on a copy of production data.

  R50:
Page endpoints SHALL send `Server-Timing: app;dur=<ms>`; while the rollups are usable, no request issued on page load SHALL read raw usage events outside the PR 1 edges, tail and journal, except the recent-requests reads bounded by R23 and the request-log page bounded by its timeframe.

Files And Functions:
 - planned: internal/adapters/httpapi/server_timing.go#serverTiming - middleware for page endpoints
 - planned: internal/adapters/sqlite/dashboard_pages_benchmark_test.go#BenchmarkDashboardPages - synthetic store at target scale
 - existing: .local/review/pages-latency.md - harness rerun on a production copy for each PR

Tests:
internal/adapters/sqlite/dashboard_pages_benchmark_test.go
 - description: every page endpoint's reads under 300 ms per operation at target scale; results recorded in the PR
   requirements: R49
internal/adapters/httpapi/server_timing_test.go
 - description: header present on page endpoints and absent on proxy routes
   requirements: R50

## Implementation checklist

PR 2 — overview, alerts and attention keys:

1. [x] OpenSpec change `overview-groups` (named by the owner instead of `redesign-overview-alerts`): proposal, tasks, `go-runtime` delta (R1–R18, R46–R50) and the `frontend-architecture` REMOVED and MODIFIED deltas of use cases 1–3; strict validation.
2. [x] Confirm the PR 1 prerequisites: key slices at both grains, the window planner and the read snapshot are on `feat/usage-rollups` (PR 2 is branched from it, not from `main`); the top-N query is implemented here as the per-key local-day slice read; no gap for Q7.
3. [x] Domain: `AccountHealthState`, `WindowHealth` (`AggregateWindow`), `ProjectRunway`, `QuotaBurnPerHour`, `EvaluateAlerts` with thresholds, attention ranking; table tests (R10–R17). Docstrings name the workflow steps of use cases 2.1–2.3.
4. [x] SQLite: `OverviewEntities` (batch account health and key rows), `OverviewUsage` (local-day key spend and error windows), `ShellSnapshot`, `Directory`; raw-equivalence tests across zones and fold states; raw fallback for today (R5, R6, R9).
5. [x] Application and HTTP: `OverviewService`, `ShellStatusService`, `DirectoryService`, `/api/overview`, `/api/directory`, `GET /api/api-keys/{id}`, additive `/api/dashboard/status`, `Server-Timing` (R1, R2, R5, R48, R50).
6. [x] Web: `useShellStatus`, header and status bar without own polls, `GlobalSearch`, `features/overview`, `features/requests` (moved components), redirects, locales (`en`, `ko`, `zh-CN`), MSW mocks and handler coverage (R1, R3, R4, R7, R8, R15, R17, R18).
7. [x] Contract tests for the new endpoints and the unchanged public key-usage endpoints, `BenchmarkDashboardPages` for the overview and the production-copy latency run (R46, R49).
8. [x] Use case 4 of `specs/dashboard-delivery.md` is marked outdated; gates, PR (#11), CI and the release notes of go-v1.3.0 listing the deprecation of `/api/dashboard/overview` and `/api/dashboard/projections`.

PR 3a — key page (use cases 4 and 4.1):

9. [x] OpenSpec change `key-page`: `go-runtime` delta (R19–R29) extending the key-report requirements, and the `frontend-architecture` deltas of use case 4; strict validation.
10. [x] Rollup additions: `histQuantile`, the total-latency histogram and `idx_usage_rollup_error_key` landed in PR 1 (Q6, Q7) (R22).
11. [x] SQLite: `KeyPeriodUsage`, `KeyErrorCodes`, `KeyLimitPace`; raw-equivalence tests across zones and fold states (R20–R23).
12. [x] Application and HTTP: `KeyPageService` with admin and owner scopes; `GET /api/api-keys/{id}/page`, `GET /api/api-keys/{id}/requests`, `GET /api/key-reports/page`, `GET /api/key-reports/requests` with parameter validation, owner projection, compression and `Server-Timing`; security tests (R20, R23, R25–R28, R50).
13. [x] Web: `features/key-page` shared by `/keys/:id` and owner mode, `AuthGate` keeping `from`/`to`, key links switched from `/keys?selected=<id>` to `/keys/:id`, locales and MSW mocks (R19, R24, R29).
14. [x] Contract tests, `BenchmarkDashboardPages` for the key page; gates, PR (#17), CI (R46, R49).

PR 3b — reports for API-key users (use case 5):

15. [x] OpenSpec change `key-usage-reports`: `go-runtime` delta (R30–R32) and the `frontend-architecture` deltas of use case 5; strict validation.
16. [x] SQLite: `KeyUsageReport` (the export reuses it instead of a separate `StreamKeyDailyRows`); raw-equivalence tests with legacy rows, moved keys, the ungrouped selection and Europe/Prague days (R30, R31).
17. [x] Application and HTTP: `KeyUsageReportService`, `GET /api/reports/keys` (gzip) and `GET /api/reports/keys/export.csv`, both with `Server-Timing`; CSV-sum and statement-count tests (R30, R31, R50).
18. [x] Web: Reports rebuilt (`ScopePicker`, `PeriodPicker`, `PerKeyTable`, `GroupReportTable`, `KeyDailyChart`, CSV, `TrafficDetails`), owner links per R29, locales and MSW mocks (R32).
19. [x] Contract tests, `BenchmarkDashboardPages` for the report; gates, PR (#16), CI (R46, R49).

After PR 3a and 3b:

20. [x] Production-copy latency run of the new pages (2026-10-29, approved by the operator); the release notes of go-v1.4.0 list the deprecated key endpoints (R47, R49).

PR 4a — keys page (use cases 6 and 6.1):

21. [x] OpenSpec change `keys-page`: `go-runtime` delta (R33–R39) and the `frontend-architecture` deltas of use case 6; strict validation.
22. [x] Application and HTTP: `KeysListService` (batch reads reusing the overview key rows and key day slices, filter, sort and page in memory), `KeysBulkService`, `GET /api/keys`, `GET /api/keys/ids`, `POST /api/keys/bulk`; statement-count, boundary and partial-failure tests (R33, R34, R36–R38).
23. [x] Web: `features/keys` (`KeysPage`, `KeysTable`, `KeysFilters`, `BulkActionsBar`, URL-backed `useKeysList`) at `/keys`, the APIs page and `ApiKeysOverview` removed, locales and MSW mocks (R35, R39).
24. [x] Contract tests, `BenchmarkDashboardPages` for the keys list; gates, PR (#20), CI (R46, R49).

PR 4b — groups page and Settings (use case 7):

25. [x] OpenSpec change `groups-page`: `go-runtime` delta (R40, R41) and the `frontend-architecture` deltas of use case 7 and of the Settings page; strict validation.
26. [x] Application and HTTP: `GroupsSummaryService` reusing the overview group-row builder, `GET /api/groups/summary`; equality with the overview rows (R40).
27. [x] Web: `features/groups` (`GroupsPage`, `GroupsTable`, `GroupDetailSheet`, the pool runway card of Q2) at `/groups`; Settings without the key and group sections and without the appearance settings that no page uses; locales and MSW mocks (R40, R41).
28. [ ] Contract tests, `BenchmarkDashboardPages` for the groups summary; gates, PR, CI (R46, R49).

PR 4c — accounts management (use case 8):

29. [x] OpenSpec change `accounts-management`: `go-runtime` delta (R42–R45) and the `frontend-architecture` deltas of use case 8; strict validation.
30. [x] SQLite and HTTP: `Store.ListAccountViews` batch reads, `GET /api/accounts` built from them with every existing field plus `groups`, `usage7d` and `shell`; statement count equal for 9 and 100 accounts (R43).
31. [x] Web: `AccountsTable` with inline priority, pause or resume and re-login, `AccountDetailSheet` with labelled periods and Check now; no provider call on load or selection; locales and MSW mocks (R42, R44, R45).
32. [ ] Contract tests, `BenchmarkDashboardPages` for the accounts list; gates, PR, CI (R46, R49).

After PR 4a–4c:

33. [x] Production-copy latency run of the new pages (2026-10-29, approved by the operator), release notes of go-v1.5.0 listing the deprecation of `GET /api/api-keys/`; the three OpenSpec changes are archived and their deltas synced (R47, R49).

Follow-up release:

34. [ ] Remove the endpoints deprecated in PR 2–4 with their tests and mocks; update contract tests and release notes (R47).

## Open questions

Non-blocking; the defaults in parentheses are planned unless the owner decides otherwise.

- Q1: Alert and attention thresholds (90/100 % limits, 7 days/24 h expiry, error spike 5 %, 20 errors and 3 × baseline, key spike 25 %, spend spike 2 × and 10 USD, stale quota 10/60 min, runway 1 h/12 h): fixed domain constants (default) or operator settings?
- Q2: The weekly credits pace card with its "throttle to X %" and "add N Pro" recommendations: keep it as a pool runway card on the Groups page, which keeps `/api/dashboard/projections` (default), or drop it with its endpoint?
- Q3: Requests page in the top navigation, or only linked from the overview, the key page and search (default)?
- Q4: Owner links: plain links that still require signing in with the key (default), or signed, time-limited read-only share links (a new credential type with its own security requirement)?
- Q5: Owner-mode request rows omit error messages, conversation ids, client IPs and full user-agent strings (default). Should owners see redacted error messages to debug their clients?
- Q6: Total-latency p50/p95 on the key page needs a fourth histogram in the rollups (layout 2 and a rebuild): add it, or keep TTFT, queue and tokens per second only (default)?
- Q7: Add `idx_usage_rollup_error_key ON usage_rollup_error_hour(api_key_dim, bucket_epoch)` and `histQuantile` to PR 1 now (default, PR 1 is still being implemented), or as additive migration 43 in PR 3?
- Q8: Remove deprecated admin endpoints in the release after PR 4 (default), or keep `GET /api/api-keys/` longer for operator scripts?
- Q9: Bulk move and set-limits run one key per write job because limit backfill sums the key's raw events of each window inside the write transaction (up to ≈ 0.5 s for a busy key's monthly limit). Accept (default), or first move the backfill out of the write transaction?
- Q10: Scale assumptions of this plan: at most 100 accounts and at most 5 000 keys for in-memory filtering and sorting on the server. Correct?

## Implementation notes (PR 2)

- Branch `feat/overview-groups` from `feat/usage-rollups`; OpenSpec change `overview-groups`, archived as `openspec/changes/archive/2026-10-29-overview-groups/` (the owner's name for `redesign-overview-alerts`).
- Reads: `GET /api/overview` runs 24 SQL statements at any scale (entity snapshot 8, usage snapshot with legacy facts, raw spans, tail, journal, total hours, one key-slice statement of range arms and client-disconnect error hours, quota history 1, shell status 6); the directory 3 and the shell status 6. Past local days of whole-hour zones read the UTC-day slice row plus the hour rows of the offset; yesterday, which holds the same-time split, and today read hour rows; fractional zones read hour rows with the PR 1 split hours.
- Measured (i9-14900K, modernc, warm): 300 keys, 50 groups, 100 accounts, every key active every hour of 8 days, overview median 37 ms (`TestOverviewMeetsItsLatencyBudgetAtTargetScale`, asserted < 300 ms); `BenchmarkDashboardPages` with 1.51 M events over 30 days: overview 46 ms (UTC), 47 ms (Europe/Prague), 123 ms (Asia/Kolkata), directory 0.7 ms, shell status 0.8 ms, request log 24 h 4 ms.
- Shell status adds `stickyThreadsEnabled`, `preferEarlierResetAccounts` and `preferEarlierResetWindow` so the status bar's routing label needs no settings read; `spendYesterdaySameTimeUsd` and `spend7dUsd` are nullable (null while the rollups are unusable).
- Window health follows the key-report group aggregate exactly: an expired observation is not current, unknown plans have no capacity. Runways use only accounts with a known burn; reset-time jitter below 30 minutes is one window.
- Limit fill and the `≥ 90 %` counts consider active (enabled, unexpired) member keys; spend counts every non-deleted member key.
- The Requests page keeps offset pagination with the existing table; `nextCursor` "load more" is deferred. `/api/request-logs` accepts `30d` and `/api/conversations` `1h` and `24h` so one symbolic period drives both views.
- Until PR 4, Groups opens `/settings#account-groups` and Keys renders the APIs page at `/keys`; key links use `/keys?selected=<id>` until the key page of PR 3.
- The appearance settings for the dashboard refresh rate and the burn-projection card no longer affect any page; they are removed with the Settings rework of PR 4.

## Implementation notes (PR 3a)

- Branch `feat/key-page` from `feat/overview-groups` (PR #11); OpenSpec change `key-page`, archived as `openspec/changes/archive/2026-10-29-key-page/`.
- Reads: one `Store.KeyPageUsage` runs the period, error-code, pace and first-page readers (`keyPeriodUsage`, `keyErrorCodes`, `keyLimitPace`, `keyRecentRequestsTx`) in one analytics snapshot: 14 statements, 15 with model-filtered limits, for 7 and for 90 days (`TestKeyPageUsageStatementCountIsIndependentOfThePeriod`). The period and the previous period share one plan split at every local midnight; raw split hours and edges read the key's events through `idx_usage_events_key_time` (`keyImages`) instead of every key's events of the hour; the previous period reads the key's slice rows (UTC days inside, hours at the edges); folded legacy hours are read once for both periods. The raw path, fallback and oracle, reads the key's events through the key index and builds the same histograms, so both paths return identical results (`TestKeyPageUsageMatchesRawEventsInEveryFoldState`, UTC, Europe/Prague and Asia/Kolkata in all four fold states).
- The use case reads, besides that snapshot, `GetAPIKey`, `OverviewEntities` (projected limits, group, same-group mini-list, accounts for the pool and the labels), `RecentAccountQuotaHistory` (burn rates), the settings (owner visibility), and for the administrator `UsageTotals` and the shell status. The owner's key is checked again in the entity snapshot: disabled, expired or deleted by then is 401.
- Measured (i9-14900K, modernc, warm, `BenchmarkDashboardPages` with 1.51 M events of 300 keys over 30 days plus the busiest key at 57.7 k events per 7 days, 247 k in 30 days, 24 cube rows per hour): administrator key page of the busiest key 41 ms (7 d) and 90 ms (30 d) in UTC, 43 and 92 ms in Europe/Prague, 57 and 131 ms in Asia/Kolkata; a typical key's 30 days 31 ms (mostly the fixed entity, quota-history and shell reads); recent requests 0.9 ms, errors only 2.1 ms. `TestKeyPageMeetsItsLatencyBudgetAtTargetScale` asserts the busiest key's 30 days under 300 ms next to 300 keys (60 ms for 60 k events).
- Errors follow the reports: every status but success, completed and cancelled, `client_disconnected` included; errors by code come from `usage_rollup_error_hour` through `idx_usage_rollup_error_key`, last seen is the start of the newest UTC hour holding the code, and errors stored without a code (and folded legacy hours) form one row with an empty code.
- Pace counts the rollups' report events (every request kind but warm-ups) of the last 3 hours, while limit counters charge `normal` requests; it is an estimate for the projection only. The ungrouped key's pool is the whole ChatGPT pool; purchased credits are summed for the pool and for groups alike.
- Recent requests: the key's newest events after the cursor are bounded inside the page query (`ORDER BY requested_at DESC, request_id DESC LIMIT n` through the key index) and merged with its pending entries by the request log's page reader (`readRequestLogPage`, extracted from `ListRequestLogs`); there is no total. Errors only examines at most 5 000 rows per call without skipping hours by `usage_rollup_error_hour`, and leaves pending entries out.
- HTTP: the administrator routes also reject unknown parameters with 400 `invalid_report_filters`. A mutation method on an owner route reaches the administrator API behind `/api/` and answers 401, not 405. Owner routes are wrapped in `compressJSON` and `serverTiming` on the outer mux; `/api/key-reports/reports` stays uncompressed and deprecated.
- Dashboard: the by-model table has no effort column; Regenerate and Disable ask for confirmation, Enable does not; the administrator's request details dialog is the key page's own (the Requests page dialog loads the conversation archive); a 401 from the owner's requests also ends the session; an invalid day in the URL counts as absent and the custom picker ends at today at the latest; "show these requests" opens the Requests page with the smallest timeframe (24 h, 7 d or 30 d) covering the period start. The APIs page keeps the list, key creation and the lifetime cards beside the list; editing and deleting moved to the key page. `/keys/:id` carries the shell status only while its period includes today.
- The production-copy latency run of the key page is open with checklist item 20 (operator approval).

## Implementation notes (PR 3b)

- Branch `feat/key-usage-reports` from `feat/overview-groups` (PR #11); OpenSpec change `key-usage-reports`, archived as `openspec/changes/archive/2026-10-29-key-usage-reports/`.
- Scope: every figure sums the resolved current keys (non-deleted, non-internal), so totals, groups, keys, the daily series and the CSV agree; requests without a key, deleted and internal keys are in no selection, and `usage_rollup_total_hour` is not read. Unknown ids select nothing; a selected group without keys keeps a row with zero keys.
- Daily series: keys, or groups when the selection names more than one group (ungrouped counting as one) or is empty and the keys span several groups; top 8 by cost with requests, plus `other`. The top-level `series` list names them once and every `daily[].series` holds requests, tokens and cost in that order, which keeps 90 days under 150 KB raw.
- `previous` is null when the previous period has no requests (no history-wide existence read). Costs are rounded to six decimals; CSV sums equal the JSON totals up to that rounding of each row.
- The export runs the same snapshot read with the previous period skipped and writes rows only after the snapshot is closed; memory is bounded by resolved keys × days ≤ 250 000 (`report_too_large` beyond).
- Reads: 9 SQL statements in whole-hour zones and 10 in fractional ones for any number of keys and days (`TestKeyUsageReportStatementCountIsIndependentOfScale`: 30 and 300 keys, 7 and 90 days). The slice statement sends its bucket lists as JSON arrays, so the number of days does not change the statement text or the bound-parameter count.
- Measured (i9-14900K, modernc, warm, `BenchmarkDashboardPages`, 300 keys, 50 groups, 100 accounts, 1.51 M events over 30 days): all keys 30 d 16 ms (UTC), 48 ms (Europe/Prague), 208 ms (Asia/Kolkata); 90 d 18 ms, 50 ms, 211 ms; three groups 30 d 3 ms, 6 ms, 49 ms; CSV 90 d 20 ms, 41–88 ms, 202 ms. Every key active every hour of 90 days (648 k events): 90 d 47 ms (UTC), 136 ms (Europe/Prague), CSV 40 ms and 122 ms. The 30-day report at 300 keys is asserted under 300 ms (`TestKeyUsageReportMeetsItsLatencyBudgetAtTargetScale`, median 12 ms on 8 days of data). Fractional zones read 7 slice rows per key and day and sum their raw split hours per key in SQL, which cut Asia/Kolkata from 313 ms to 208 ms.
- Web: the scope picker renders at most 100 matches (type to narrow); the keys table hides idle keys behind a toggle, filters by text, collapses groups and shows 20 rows per group before "Show all"; quick periods stay relative in the URL (`period=30d`), custom ones are `from`/`to`. Traffic details wait for the report and filter `/api/reports` by its resolved key ids (at most 50), or read it unfiltered for every key.
- `reports-summary-cards`, `daily-detail-table`, `cost-per-day-chart` and `tokens-per-day-chart` are no longer used by Reports but stay while `features/key-reports` imports them; PR 3a removes them with the old key report.

## Implementation notes (PR 4a)

- Branch `feat/keys-page` from `main`; OpenSpec change `keys-page`, archived as `openspec/changes/archive/2026-10-29-keys-page/`.
- Reads: `GET /api/keys` runs one analytics snapshot through `Store.KeyList`: the groups and the keys with their active limits (`listGroupRefs`, `listKeyRows`) and the per-key day costs through the key usage report's reader (`newKeyUsageSums`, `keyUsage`), plus the shell status; 15 statements for 30 and for 300 keys (`TestKeysListUsesAStatementCountIndependentOfScale`). `GET /api/keys/ids` reads only the keys and groups unless it is sorted by spend (3 or 9 statements). While the rollups are unusable only today is read from raw events and the 7- and 30-day spend are null.
- Filters: the status follows the directory's (disabled before expired); near limit counts only active keys, like the overview's group fill and attention keys, so the overview's links agree with the list; `expiring_days` and `inactive_days` accept 1 to 365 days (the toolbar offers 7, and 7, 30 or 90); `q` matches substrings of the name or prefix; an empty parameter value is absent; unknown or repeated parameters are 400 `invalid_key_filters`. A key without a value for the sort key (no limit, never used, unknown period spend) sorts last in both directions; ties are ordered by id. The matching ids accept the sort and order too, so "Select all matching" beyond 1 000 keys takes the first 1 000 in list order.
- Bulk: `SetAPIKeysActive` changes up to 25 keys per job with one `UPDATE … RETURNING id`; `MoveAPIKeyToGroup` and `SetAPIKeyLimits` change one key per job with the limit synchronisation of `SaveAPIKey` (`TestMoveAPIKeyToGroupHasTheEffectOfSavingTheKey`, `TestSetAPIKeyLimitsHasTheEffectOfSavingTheKey`). Jobs after a failed job still run; after cancellation the remaining keys are `not_applied`. Codes: `not_found`, `key_in_group`, `invalid_group`, `conflict`, `not_applied`, `internal_error`. `groupId` belongs only to `move_group` (null is no group) and `limits` only to `set_limits` (an empty list removes the limits); malformed JSON is 400 `invalid_request`, another invalid body 400 `invalid_bulk_request`, invalid limit rules 422 `invalid_limit`.
- Measured (i9-14900K, modernc, warm, `BenchmarkDashboardPages`, 300 keys, 50 groups, 100 accounts, 1.51 M events over 30 days): keys list sorted by 30-day spend 16–18 ms (UTC), 44 ms (Europe/Prague), 222–261 ms (Asia/Kolkata, the fractional-zone cost of the report reader: the all-keys 30-day report took 226 ms in the same run); the near-limit filter sorted by fill 17, 45 and 244 ms; matching ids by name 1.3 ms. `TestKeysListMeetsItsLatencyBudgetAtTargetScale` asserts the list under 300 ms (median 8.9 ms on 8 days of data).
- Web: `/keys` carries the shell status, so its only periodic request is `GET /api/keys`; the directory loads once for the group picker and for naming failed keys of other pages. `invalidateEntityViews` also marks the keys list stale, so key, group and account mutations on any page refresh it. Failed bulk keys stay selected and are listed with their reason; Set limits is offered for any selection and grouped keys fail with `key_in_group`; "Select all n matching" adds up to 1 000 ids to the selection. The row menu has Edit (`ApiKeyEditLoader`, now shared with the key page), Disable (confirmed), Enable, Regenerate (confirmed) and Delete (confirmed). `features/apis` is removed: `api-key-limit-usage.tsx` moved to `features/api-keys`, the schemas of the deprecated trends and 7-day usage endpoints moved to the test mocks (the endpoints stay for one release), and the unused `apis.*` and `apiKeys.overview.*` strings were dropped. Settings keeps `ApiKeysSection` until PR 4b.
- The production-copy latency run of the keys page is open with checklist item 33.

## Implementation notes (PR 4b)

- Branch `feat/groups-page` from `main`; OpenSpec change `groups-page`, archived as `openspec/changes/archive/2026-10-29-groups-page/`.
- Reads: the summary reads the overview's entity snapshot plus `group_limits` in one statement (`GroupsSummaryEntities`), one analytics snapshot with the overview's usage and the 22-day span before it (`GroupsSummaryUsage`), the quota history and the shell status: 29 SQL statements in Europe/Prague and 30 in Asia/Kolkata, equal for 5 groups / 30 keys / 10 accounts and 50 / 300 / 100 (`TestGroupsSummaryStatementCountIsIndependentOfScale`). The span reads at most 21 UTC-day rows and 24 hour rows per key in every zone (`TestGroupsSummarySpanSlicePlanUsesDayRows`) and equals the raw evaluation in UTC, Europe/Prague, America/New_York, Asia/Tokyo and Asia/Kolkata in all four fold states.
- Measured (i9-14900K, modernc, warm, `BenchmarkDashboardPages` with 1.51 M events over 30 days, 300 keys, 50 groups, 100 accounts): groups summary 64 ms (UTC), 67 ms (Europe/Prague), 182 ms (Asia/Kolkata), against the overview's 50, 52 and 143 ms in the same run; `TestGroupsSummaryMeetsItsLatencyBudgetAtTargetScale` asserts under 300 ms on 12 days of data (median 46 ms).
- Rows: the group rows and the ungrouped row are the overview's (`assembleOverview` returns the response with the derived accounts, keys and raw alerts; `buildOverview` is unchanged). The pool row is a group row over every ChatGPT account and key whose spend and trend are the overview pool summary's (all reported traffic); its attention comes from whole-pool alerts, which include re-login accounts. Members are the non-deleted accounts of the group, external ones included; the ungrouped row and the pool carry no members and no templates. Additions over the planned types: `generatedAt`, `timezone`, `accounts` (the member picker without `/api/accounts`) and `topKeys7d` (top 20 by 7-day spend, the sheet's key list).
- Web: one Save sends name, members and limits in one `PUT /api/account-groups/{id}`; Create group opens the same sheet empty and selects the new group. The page does not request `/api/account-groups/` (`useAccountGroupMutations` holds the mutations without the list), `/api/accounts` or `/api/dashboard/status` (`/groups` carries the shell status). The runway card reuses the `dashboard.weeklyPace.*` strings of the removed overview card. Group, key and account mutations invalidate `["groups","summary"]` through `invalidateEntityViews`.
- Settings: `ApiKeysSection` and `AccountGroupsSection` leave Settings (the latter is deleted; `ApiKeysSection` stays in `features/api-keys` for PR 4a to remove with the APIs page, its flows now tested on the section itself); `ApiKeyAccessSettings` keeps both toggles; the refresh-rate and burn-projection appearance settings, their store fields and locale strings are removed, as nothing read them. `/settings?group=<id>#account-groups` redirects to `/groups?selected=<id>`, so the key page's group links keep working.

## Implementation notes (PR 4c)

- Branch `feat/accounts-management` from `main`; OpenSpec change `accounts-management`, archived as `openspec/changes/archive/2026-10-29-accounts-management/`. The `frontend-architecture` delta MODIFIES the five list requirements of use case 8 plus "Account list presents a single add-account entry point" (the toolbar button) and "Accounts page can sort by available reset credits" (no longer the default), and REMOVES "Accounts list respects compact row appearance preference", "AccountListItem displays a reset-credits count badge" and "Account list status filter shares the help row"; OpenSpec keeps every scenario name of a MODIFIED requirement, so the old scenario titles carry table-layout bodies.
- Reads: `Store.ListAccountViews` runs in one analytics snapshot: the overview's account health read (accounts, quotas, credits, refusals of quota-exceeded accounts, memberships) plus additional quotas, `usage_totals` of scope `account` and the token ciphertexts (external keys are not selected), then the last 7 × 24 hours per account: folded legacy hours, the raw partial hours at both edges summed per account in SQL (spans passed as one JSON array, so the statement text is fixed), the unfolded tail and the journal, and one slice statement over `usage_rollup_slice` kind `account` (UTC-day rows inside the window, hour rows at the edges). 15 statements at 9 and at 100 accounts (`TestListAccountViewsStatementCountIsIndependentOfScale`); the raw path is the oracle in every fold state (`TestAccountViewsUsageMatchesRawEventsInEveryFoldState`). The handler adds `LoadSettings` and the shell status (6 statements) and reads the reset-credit snapshot in memory (`AccountUsageService.CachedResetCredits`), without the per-account `GetAccount` of `ResetCreditsSnapshot`.
- `GET /api/accounts` keeps every field: `accounts_list_test.go` keeps the go-v1.4.0 per-account handler as an oracle and compares every field of a fixture with aliases, duplicate e-mails, all windows with an expired one, balance, exhausted and unlimited credits, additional quotas with a routing override, groups, present, expired, unreadable and missing tokens, an external and a deleted account and reset-credit snapshots; it adds `groups`, `usage7d` (`requests`, `totalTokens` = input + output, `costUsd`), `resetCreditsObservedAt` and top-level `shell`, and sends `Server-Timing`. `resetCreditsObservedAt` (the snapshot time, kept in memory with the snapshot) is an addition to the plan: it is the "as of" of "available N as of HH:MM".
- Measured (i9-14900K, modernc, warm, `BenchmarkDashboardPages`, 300 keys, 50 groups, 100 accounts, 1.51 M events over 30 days): accounts list 5.2 ms (overview 49 ms, directory 1.0 ms, shell 0.6 ms in the same run). The whole handler on a small store: 0.8 ms at 9 accounts and 2.6 ms at 100 (`TestAccountsListReadsDoNotGrowWithAccounts`, which also pins the repository reads per request: `ListAccountViews` 1, `LoadSettings` 2, `ShellSnapshot` 1, no per-account read).
- Web: the default sort is status severity (re-login, deactivated, quota exceeded, rate limited, paused, active; then next reset); the old reset-latest and both name directions stay as modes. The table shows a monthly column only when an account has a monthly window, keeps the actions column sticky on narrow screens and opens the sheet from a row click or the label; the URL keeps `?selected=<id>`. Group names and the group filter come from `GET /api/directory`; the groups editor saves through `PUT /api/accounts/{id}/groups` from `features/accounts`, so the Accounts page no longer imports `features/account-groups`. Mutations patch the row in the list cache and then refetch it; the page-wide "Updating account" overlay is gone. Check now is a mutation (`useCheckResetCredits`), not a query, so nothing can refetch the provider; the usage reset after a check clears the checked value. The compact-row appearance preference is no longer read by Accounts (Settings still offers it; PR 4b may drop it with the other unused appearance settings).
- `/accounts` carries the shell status (`PAGES_CARRYING_SHELL_STATUS`), so its only periodic request is `GET /api/accounts` every 30 s; the page still reads `/api/settings` once for the reset-credit expiry badge.

## Production-copy measurements (go-v1.5.0)

Measured 2026-10-29 with the release binary on a `VACUUM INTO` copy of the production database (845 k events, 28 keys, 6 groups, 9 accounts; the busiest key has 116 k events in 7 days), in an isolated network namespace at low priority; the production service was not touched and the copy was deleted. Warm medians with the `Server-Timing` server time:

| Endpoint | Warm | Server |
|---|---:|---:|
| `GET /api/keys` default page / by 30-day spend / near limit | 13 / 12 / 18 ms | 9.9 / 8.9 / 17 ms |
| `GET /api/keys/ids` | 2.4 ms | 1.1 ms |
| `GET /api/groups/summary` | 127 ms | 123 ms |
| `GET /api/dashboard/projections` (pool runway card) | 221 ms | — |
| `GET /api/accounts` | 25 ms | 21 ms |

- The main endpoints of the Keys, Groups and Accounts pages meet R49; whole page loads take 72 ms (keys), 105 ms (accounts) and 297 ms (groups) warm, and 0.06–0.24 s with a dropped page cache after the startup verifier.
- The groups page is bounded by the pool runway card: `GET /api/dashboard/projections` reads 30 days of quota observations and 7 days of positive quota deltas from raw rows. Earlier copies measured it at about 28 ms only because their quota observations were stale and the weekly pace was not computed.

## Production-copy measurements (go-v1.4.0)

Measured 2026-10-29 with the release binary on a `VACUUM INTO` copy of the production database (815 k events, 1.46 M reservations, 28 keys, 6 groups, 9 accounts; the busiest key has 92.5 k events in 7 days and 291 k in 30), server and client in an isolated network namespace at low CPU and I/O priority; the production service was not touched and the copy was deleted. Warm medians of `curl` total time, with the `Server-Timing` server time:

| Endpoint | Warm | Server |
|---|---:|---:|
| `GET /api/overview` | 95 ms | 91 ms |
| `GET /api/request-logs` 24 h / whole history | 26 / 7.9 ms | — |
| `GET /api/request-logs/options` 24 h / whole history | 25 / 4.4 ms | — |
| `GET /api/api-keys/{busiest}/page` 7 / 30 / 90 d | 116 / 179 / 208 ms | 104 / 174 / 200 ms |
| `GET /api/key-reports/page` 7 / 30 d | 130 / 200 ms | 127 / 196 ms |
| `GET /api/api-keys/{id}/requests` / errors only | 12 / 33 ms | 7.5 / 32 ms |
| `GET /api/reports/keys` all keys 7 / 30 / 90 d | 10 / 82 / 101 ms | 7 / 80 / 96 ms |
| `GET /api/reports/keys/export.csv` 90 d | 111 ms | 107 ms |

- Every main page endpoint answers within the 300 ms budget of R49 on production data; whole page loads (parallel requests as the browser issues them) take 172 ms (overview), 96 ms (requests), 187 ms (busiest key page) and 93 ms (reports) warm.
- With the page cache dropped and the startup verifier finished, the key page takes 0.25–0.87 s, the owner page 0.47–0.79 s, the overview 0.16–0.23 s and a 30-day report 0.15 s. A cold read issued while the startup verifier is still reading raw events took up to 8.6 s; this contention lasts only for the first seconds after a start.
- No rollup rebuild or mismatch occurred; R9 of `usage-rollups.md` (request-log options under 300 ms) is met at 4.4 ms.

## Decision log

- 2026-10-28: The overview is organized by account groups, never lists all keys and has no period picker; every figure carries a fixed period label. This removes the three independent period pickers and the four duplicated quota views.
- 2026-10-28: Group projections use the accounts' whole burn rate and reset times (`ProjectRunway`), not the spend of the group's keys, because shared accounts drain from every group's traffic; the ungrouped row uses the whole pool because ungrouped keys may use every permitted account.
- 2026-10-28: Group-limit fill is derived from member keys' own counters because `group_limits` are templates copied into each key; no group counter or schema change is introduced.
- 2026-10-28: Alerts are evaluated on every overview read from domain constants and are never stored or sent; acknowledgement and notifications are out of scope.
- 2026-10-28: The shell status travels inside each page's poll response, so each page has exactly one periodic request; `/api/dashboard/status` remains for pages without a poll.
- 2026-10-28: One key page component serves the administrator and the owner; owner data comes from new routes under `/api/key-reports/` so the existing report cookie path, firewall and revalidation rules apply unchanged; `/v1/usage/reports` keeps its shape instead of growing the new sections.
- 2026-10-28: Reports per key read rollup slices; the model filter is not offered on the per-key report because a model filter across many keys would scan the cube; model, user-agent and latency details load lazily from the existing `/api/reports`.
- 2026-10-28: Keys filtering and sorting run in memory after a fixed set of batch reads, because spend lives in the rollups and cannot be ordered by SQL over `api_keys`; bounded by Q10.
- 2026-10-28: Bulk actions are applied in short atomic write jobs with per-key results instead of one transaction, to respect the short-transaction rule; partial failure is explicit.
- 2026-10-28: Admin endpoints the UI stops calling stay for one release, because tabs opened before a seamless switch keep calling them; public key-usage endpoints never change.
- 2026-10-28: The request log and conversations move to a Requests page with one period picker defaulting to 24 hours instead of staying on the overview.
- 2026-10-28: Spec actions: `usage-rollups.md` reused (two additions requested, Q7); `dashboard-delivery.md` use case 4 marked outdated by PR 2; `go-runtime` amended by three OpenSpec changes; `frontend-architecture` receives only REMOVED and location-only MODIFIED deltas.
- 2026-10-28 (owner): Q1–Q5 and Q8–Q10 take the defaults above. Q6: the total-latency histogram is added to rollup layout 1 in PR 1 (no rebuild later). Q7: `idx_usage_rollup_error_key` and `histQuantile` are part of PR 1.
- 2026-10-29: PR 3 is split into PR 3a (key page, OpenSpec change `key-page`) and PR 3b (reports for key users, OpenSpec change `key-usage-reports`): they share no files apart from routing and locales, so they are built and reviewed in parallel. Following Q6, R22 now includes total-latency p50 and p95.
- 2026-10-29: PR 3a reads the key page in one analytics snapshot through one store method (`KeyPageUsage`) instead of three, so the period, its error codes, the limit pace and the first page of recent requests are consistent; percentiles use the PR 1 interpolated `histQuantile` on both read paths.
- 2026-10-29 (PR 3b): Reports sum only the resolved current keys, so the summary, the tables and the CSV always agree; the export reuses the report's snapshot read instead of a separate streaming reader and writes after the snapshot closes, bounded by keys × days; the daily series carry a top-level series list with compact points.
- 2026-10-29: The production-copy run with go-v1.4.0 confirmed R49 for the overview, requests, key and owner pages and reports; the cold read during the startup verifier is recorded as an observation, not a requirement change. The OpenSpec changes `overview-groups`, `request-log-pending-index`, `key-page` and `key-usage-reports` are archived and synced into `go-runtime` and `frontend-architecture`.
- 2026-10-29: PR 4 is split into PR 4a (keys page, OpenSpec change `keys-page`), PR 4b (groups page and the Settings rework, `groups-page`) and PR 4c (accounts management, `accounts-management`), built in parallel; they share only routing, locales and mocks. The Settings rework (R41) belongs to PR 4b so that only one slice edits the Settings page. Q2 keeps its default: the weekly credits pace becomes the pool runway card of the Groups page, so `GET /api/dashboard/projections` stays.
- 2026-10-29 (PR 4a): Bulk moves and limits run targeted per-key write jobs that reuse the limit synchronisation of `SaveAPIKey` (`groupRulesTx`, `syncKeyLimitsTx`) instead of reading the key and saving it whole, so a bulk job never overwrites a concurrent edit or the proxy's `last_used_at`; the resulting limits and account scope equal saving the key.
- 2026-10-29 (PR 4a): The keys list reads its usage through the key usage report's reader in the same analytics snapshot as the keys, limits and groups, instead of an entity snapshot plus the overview's reader, which is planned for 8 days; the 7- and 30-day spend are null while the rollups are unusable, like the overview's trends.
- 2026-10-29 (PR 4c): The accounts list reads every account in one analytics snapshot and its last 7 days from the account slices; the response gains `resetCreditsObservedAt` for the "as of" of the reset-credit snapshot, and the table sorts by status severity by default so accounts needing the operator come first.
- 2026-10-29: The production-copy run with go-v1.5.0 confirmed R49 for the Keys, Groups and Accounts pages; the pool runway card's `GET /api/dashboard/projections` (221 ms) is recorded as the next speed-up candidate, not as a requirement change. The OpenSpec changes `keys-page`, `groups-page` and `accounts-management` are archived and synced.
- 2026-10-29: `GET /api/dashboard/projections` stops computing the weekly pace's key attribution (OpenSpec change `projections-without-key-attribution`, archived as `openspec/changes/archive/2026-10-29-projections-without-key-attribution/`): no page shows it since the redesign, and it grouped every raw event of the last two hours (20 k in production, most of the endpoint's 221 ms). An exact sliding two-hour window cannot be served cheaper from hour rollups, about half of it lies in partial hours; the deprecated overview keeps the attribution until it is removed.
