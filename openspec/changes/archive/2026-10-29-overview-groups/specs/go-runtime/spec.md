## ADDED Requirements

### Requirement: Admin pages poll once and read the shell status from that poll

Every administrator page SHALL issue at most one periodic request: its page endpoint, or `GET /api/dashboard/status` every 60 seconds while visible on a page without one. `GET /api/dashboard/status` SHALL return the shell status: `ready` (the readiness of `/health/ready`), `lastSyncAt` (the newest quota observation of non-deleted ChatGPT accounts), `usageFoldedAt` and `rollupsUsable` (the usage rollup state), `reauthRequiredAccounts`, `availableResetCredits` (from the in-memory reset-credit snapshot), `showResetCreditBadges`, `apiKeyAuthEnabled` and the routing settings shown in the status bar. The shell status SHALL be computed from in-memory state and a fixed number of batch reads, independent of the number of accounts and keys. A page endpoint that carries the shell status SHALL return it as `shell`; the header badge, the API-key authentication warning and the status bar SHALL read it from that response or from `GET /api/dashboard/status` and SHALL NOT poll `/api/accounts`, `/api/settings` or `/health/ready`.

#### Scenario: Overview carries the shell status

- **WHEN** an administrator keeps the Overview page open
- **THEN** the only periodic request is `GET /api/overview`, whose `shell` updates the header badge and the status bar
- **AND** no request is made to `/api/accounts` or `/health/ready`

#### Scenario: Page without its own poll

- **WHEN** an administrator keeps the Settings page open
- **THEN** `GET /api/dashboard/status` is requested every 60 seconds while the tab is visible

#### Scenario: Status cost does not grow with the installation

- **WHEN** the shell status is computed for 9 or for 100 accounts
- **THEN** the same number of SQL statements is executed

### Requirement: Global search finds keys, groups and accounts

`GET /api/directory` SHALL return the non-deleted, non-internal API keys (id, name, prefix, group id, status `active`, `disabled` or `expired`), the account groups with their non-deleted member account count and non-deleted key count, and the non-deleted accounts (id, label as alias or else e-mail, e-mail, status, kind) in a number of statements independent of their count. The dashboard search, opened with Ctrl+K or Cmd+K, SHALL match keys by name and prefix, groups by name and accounts by alias, e-mail and id case-insensitively, SHALL show at most 20 results grouped by kind and SHALL navigate to the entity's page; an input shaped like a request id SHALL offer to open `/requests?request_id=<id>`. The directory SHALL be cached for 5 minutes and invalidated by key, group and account mutations.

#### Scenario: Prefix match

- **WHEN** the operator types the first characters of a key prefix
- **THEN** that key is listed under Keys and selecting it opens the key

#### Scenario: Deleted keys are not listed

- **WHEN** a key is deleted or is an internal key
- **THEN** it is absent from `GET /api/directory` and from the group's key count

### Requirement: Replaced dashboard routes redirect

`/` and `/dashboard` SHALL redirect to `/overview`, `/apis` SHALL redirect to `/keys` with its query string, and `/groups` SHALL open the account-group management that exists until a Groups page replaces it. The navigation SHALL list Overview, Groups, Accounts, Keys, Reports, Automations and Settings in that order, and the Accounts entry SHALL carry a badge with the number of accounts needing re-login, or otherwise the available reset credits when reset-credit badges are enabled.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows `/keys?selected=key_1`

### Requirement: The overview summarizes pool, groups, alerts and attention keys in one bounded read

`GET /api/overview?timezone=<IANA>` (default UTC; an unknown zone SHALL return 400 `invalid_timezone`) SHALL return the pool summary, one row per account group, a row for ungrouped keys, the alerts and at most 10 keys needing attention for the requested zone in one response, using a number of SQL statements independent of the number of keys, groups and accounts. Spend SHALL be estimated cost in USD for the current local day and for each of the 7 local days ending today, read from the usage rollups combined with the journal, the unfolded tail and raw partial hours; while the rollups are usable the overview SHALL NOT read raw usage events beyond those sources. Group spend SHALL be the sum over the group's current non-deleted, non-internal member keys; the ungrouped row SHALL sum the keys without a group and SHALL use the whole account pool. Pool spend, requests and error rates SHALL include all reported traffic. While the rollups are unusable the overview SHALL compute today's values and the error windows from raw events of at most the last 26 hours only and SHALL return null 7-day trends and a null spend at the same time yesterday.

#### Scenario: Key moved between groups

- **WHEN** a key that spent yesterday in group A now belongs to group B
- **THEN** its spend of every day counts in group B and not in group A

#### Scenario: Statement count at scale

- **WHEN** the overview is computed for 30 and for 300 keys with the same number of days
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Rollups catching up

- **WHEN** the rollups are not usable after an upgrade
- **THEN** today's spend equals the raw evaluation of today's events and every 7-day trend is null

### Requirement: The overview page has fixed periods and one poll

The Overview page SHALL render the alerts, the pool strip, the groups table and the keys needing attention in that order, SHALL have no period picker and SHALL label every metric with its fixed period (today, last hour, 7 days, current 5 h or weekly window). It SHALL poll only `GET /api/overview`, every 30 seconds while the tab is visible and on focus, and SHALL keep showing the previous data with a stale label and a retry action when a refetch fails. Expanding a group row SHALL show its problem accounts (at most 10) and its top keys by spend today (at most 5) from the same response.

#### Scenario: Failed refetch

- **WHEN** a refetch of the overview fails after data was shown
- **THEN** the previous data stays visible with a "stale since" label and a retry action

#### Scenario: Rollups unusable

- **WHEN** the response reports that the rollups are not usable
- **THEN** the page shows that usage analytics are catching up and hides 7-day trends

### Requirement: Group window health is capacity-weighted and projected from account burn

Pool and group window health SHALL be capacity-weighted over non-deleted ChatGPT accounts with a current observation of the window: remaining percent is the sum of remaining credits over the sum of known capacity, coverage SHALL be reported, and an unknown window SHALL be null, never zero. An account in several groups SHALL count fully in each group and once in the pool. The burn of an account SHALL be the positive used-percent deltas within one reset window over the trailing at most 5 hours of quota history, times capacity over 100, per hour, and SHALL be unknown with fewer than two observations. The projected exhaustion SHALL follow the runway $R(t) = \sum_a R_a - B\,(t - t_0) + \sum_{a:\ t_0 < \rho_a \le t} \min(C_a, C_a - R_a + b_a(\rho_a - t_0))$ over the accounts with known burn, and a group SHALL be marked as running out before reset only when the runway reaches zero before the latest reset of its covered accounts; no projection SHALL be made when the burn is unknown or zero. Account health SHALL be `reauth`, `paused`, `off` (deactivated), `limited` (rate limited, quota exceeded, or a window at 0 % without usable purchased credits), `low` (a window at or below 10 % remaining) or `ok`.

#### Scenario: A reset arrives before exhaustion

- **WHEN** an account of the group resets its window before the pooled remaining reaches zero
- **THEN** the projection adds back that account's used credits at its reset time

#### Scenario: No burn

- **WHEN** no account of the group has two observations in the last 5 hours
- **THEN** the window has no projected exhaustion and is not marked as running out

### Requirement: Group rows are ordered by attention

Group rows, including the ungrouped row, SHALL be ordered by attention score (2 with a critical alert, 1 with a warning alert, else 0), then by the number of problem accounts, then by member keys at or above 90 % of a limit, then by spend today, then by name. Limit fill of a key SHALL be the highest `current / max` over its active limits with expired windows counted as zero without changing storage; group fill SHALL be the highest member fill with the counts of members at or above 90 % and at or above 100 %.

#### Scenario: Critical group first

- **WHEN** one group has an account needing re-login and another spends more today
- **THEN** the group with the account needing re-login is listed first

### Requirement: Overview alerts follow fixed rules and are never stored

The overview SHALL evaluate exactly these rules on every read and SHALL NOT persist, acknowledge or send alerts:

| Rule | Subject | Warning | Critical | Action |
|---|---|---|---|---|
| `account_reauth` | account | — | status `reauth_required` | re-login |
| `quota_runway` | pool and each group | runs out before reset in the 5 h or weekly window | exhaustion within 1 h (5 h window) or 12 h (weekly), or remaining ≤ 5 % | open the group, or Accounts for the pool |
| `key_limit` | key | an active limit ≥ 90 % | an active limit ≥ 100 % | edit limits, or the group's limits for a grouped key |
| `error_spike` | pool | last hour: error rate ≥ 5 %, ≥ 20 errors and ≥ 3 × the trailing 24 h rate (floor 1 %) | last hour: error rate ≥ 20 % with ≥ 20 errors | open the failed requests of the last hour |
| `key_error_spike` | key | last hour: ≥ 20 errors and error rate ≥ 25 % | — | open the key's failed requests |
| `key_expiry` | key | an active key expires within 7 days | within 24 hours | extend the expiry by 30 days |
| `quota_data_stale` | pool | newest quota observation older than 10 min while ChatGPT accounts exist | older than 60 min | open Accounts |

Errors SHALL exclude `cancelled` requests and the `client_disconnected` error code; the last hour SHALL be the previous whole hour plus the current partial hour and the trailing window the 24 hours before it. The ungrouped row SHALL raise no `quota_runway` alert of its own. Every alert SHALL have a stable id `rule:subjectType:subjectId:window` and exactly one action that the page performs or navigates to without a request other than the action's own. Alerts SHALL be sorted critical first, then by the rule order of the table, then by value, and more than five subjects of one rule and severity SHALL collapse into one summary alert whose action opens the filtered list.

#### Scenario: Limit boundary

- **WHEN** a key's fullest active limit is at 89.9 %, 90 % or 100 %
- **THEN** it raises no alert, a warning or a critical `key_limit` alert respectively

#### Scenario: Many keys near their limits

- **WHEN** six keys are at or above 90 % of a limit
- **THEN** one summary warning with count 6 replaces their six alerts

### Requirement: Keys needing attention are ranked and actionable

The overview SHALL list at most 10 active, non-deleted, non-internal keys with at least one reason — `limit_blocked` (an active limit ≥ 100 %, critical), `limit_near` (≥ 90 %, warning), `error_spike` (the `key_error_spike` rule), `expiring` (within 7 days, critical within 24 hours) or `spend_spike` (spend today ≥ 2 × the average of the previous 7 local days and ≥ 10 USD) — ranked by highest severity, then reason order, then the reason's value, then spend today, and SHALL report how many keys have a reason in total. The primary action of the top reason SHALL be available inline; Extend SHALL set `expiresAt` to 30 days after the later of now and the current expiry and Disable SHALL set `isActive=false`, each after a confirmation and through `PATCH /api/api-keys/{id}`, after which the overview, the directory and the shell status are refetched. `GET /api/api-keys/{id}` SHALL return one key for the edit dialog.

#### Scenario: Extend an expiring key

- **WHEN** the operator confirms Extend on a key expiring in two days
- **THEN** the dashboard sends `PATCH /api/api-keys/{id}` with `expiresAt` 30 days after that expiry and refetches the overview

#### Scenario: Spend spike floor

- **WHEN** a key spends 8 USD today after spending 1 USD per day
- **THEN** it has no `spend_spike` reason

### Requirement: The Requests page holds the request log and conversations

The request log and the conversation list SHALL live on a Requests page with one period picker shared by both views (1 h, 24 h default, 7 d, 30 d), a Live toggle that is off by default, and no polling unless Live is on (30 seconds, first page only). Filter options SHALL be fetched only for the selected timeframe and only when the filter panel opens; key and account names SHALL come from the directory. The URL parameters `timeframe`, `status`, `api_key_id`, `account_id`, `request_id` and `conversation_id` SHALL prefill the filters. `GET /api/request-logs` and its options SHALL accept `timeframe=30d`, and `GET /api/conversations` SHALL accept `timeframe=1h` and `timeframe=24h`, in addition to their existing values.

#### Scenario: Default view

- **WHEN** an operator opens `/requests`
- **THEN** the request log of the last 24 hours is requested once and no further request follows while Live is off

#### Scenario: Alert link

- **WHEN** the operator follows the error-spike alert
- **THEN** the Requests page opens with the error status and the last hour selected

### Requirement: Public key-usage contracts stay unchanged

`GET /v1/usage`, `GET /api/codex/usage` and `GET /v1/usage/reports` SHALL keep their request parameters, response fields, authentication, caching headers and response encoding unchanged by the dashboard redesign.

#### Scenario: Existing client tools

- **WHEN** a key holder's tool calls `GET /v1/usage` after the upgrade
- **THEN** it receives the same fields with the same authentication and caching as before

### Requirement: Replaced admin endpoints are deprecated for one release

An admin endpoint that the embedded dashboard stops calling SHALL stay unchanged in the release that ships its replacement and SHALL be removed no earlier than the following release; release notes SHALL list each deprecation and removal. `GET /api/dashboard/overview` and `GET /api/dashboard/projections` SHALL be deprecated by this change.

#### Scenario: Tab opened before the update

- **WHEN** a browser tab loaded before the update still polls `GET /api/dashboard/overview`
- **THEN** the endpoint answers as before until the release after the one that deprecated it

### Requirement: New admin endpoints follow the admin routing conventions

New administrator endpoints SHALL be registered behind administrator authentication and JSON compression with the existing trailing-slash conventions, and new key-owner endpoints SHALL live under `/api/key-reports/` so that the report cookie path covers them.

#### Scenario: Anonymous request

- **WHEN** `GET /api/overview` or `GET /api/directory` is requested without an administrator session
- **THEN** the response is 401 and no data is returned

### Requirement: Page endpoints meet a latency budget and report server time

With usable rollups, the main endpoint of each dashboard page SHALL answer in under 300 ms of server time, warm, on a synthetic store with 300 keys, 50 groups, 100 accounts and at least 1.5 million events over 30 days, and on a copy of production data. Page endpoints SHALL send `Server-Timing: app;dur=<ms>`, which proxy routes SHALL NOT send. While the rollups are usable, no request issued on page load SHALL read raw usage events outside the rollup journal, the unfolded tail and partial hours, except recent-request reads bounded by their limit and the request-log page bounded by its timeframe.

#### Scenario: Overview at target scale

- **WHEN** `GET /api/overview` is measured warm on the synthetic store at target scale
- **THEN** its server time is under 300 ms and the response carries `Server-Timing`
