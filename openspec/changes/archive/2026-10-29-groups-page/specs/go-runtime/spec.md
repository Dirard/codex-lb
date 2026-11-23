## ADDED Requirements

### Requirement: The groups summary reads every group's membership, limits, spend and projections in one bounded read

`GET /api/groups/summary?timezone=<IANA>` (default UTC; an unknown zone SHALL return 400 `invalid_timezone`) SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and returns the shell status, a pool row, one row per account group plus the ungrouped row, and the non-deleted accounts. The group rows and the ungrouped row SHALL be computed by the same functions as the overview's group rows and SHALL equal them for the same data and instant, in the same order: account health counts, capacity-weighted 5 h and weekly windows with next reset and projected exhaustion, limit fill, key counts, spend today, 7-day spend and trend, problem accounts, top keys by spend today and attention. Each row SHALL add its 30-day spend, the estimated cost of the 30 local days ending today summed over the row's current non-deleted, non-internal member keys; its limit templates (type, window, model filter and maximum, empty for the ungrouped row); its member account ids among the non-deleted accounts (empty for the ungrouped row); and at most 20 member keys ranked by 7-day spend, then spend today, then name, with status, spend today, 7 d and 30 d, the fullest limit percent and last use. The pool row SHALL cover every ChatGPT account and every non-deleted, non-internal key, SHALL take spend today, 7 d, 30 d and the 7-day trend from all reported traffic as the overview's pool summary does, SHALL take its attention from the alerts that concern the whole pool and its list of member keys by 7-day spend SHALL be empty. Each account, ChatGPT or external, SHALL carry its id, label (alias, else e-mail), e-mail, kind, status, health and 5 h and weekly remaining percent. The summary SHALL read one entity snapshot that includes the group limit templates in one statement and one analytics snapshot, with a number of SQL statements independent of the number of groups, keys and accounts; while the rollups are usable it SHALL NOT read raw usage events beyond the journal, the unfolded tail and partial hours, and while they are unusable every 7-day and 30-day spend SHALL be null.

#### Scenario: Rows match the overview

- **WHEN** the groups summary and the overview are computed for the same data, zone and instant
- **THEN** every group row and the ungrouped row of the summary equal the overview's rows in the same order

#### Scenario: Thirty-day spend

- **WHEN** a member key of a group spent 2 USD twenty local days ago and 1 USD today
- **THEN** the group's 30-day spend includes 3 USD from that key and its 7-day spend only 1 USD

#### Scenario: Statement count at scale

- **WHEN** the summary is computed for 5 groups, 30 keys and 10 accounts and for 50 groups, 300 keys and 100 accounts
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Latency at target scale

- **WHEN** `GET /api/groups/summary` is measured warm on the synthetic store with 50 groups, 300 keys, 100 accounts and 1.5 million events over 30 days
- **THEN** its server time is under 300 ms

### Requirement: The Groups page manages groups from one table and a detail sheet

The Groups page at `/groups` SHALL show a toolbar with a name search and Create group, the pool row, a pool runway card built from `GET /api/dashboard/projections` (weekly credit headroom, runway, next relief and, when the pool runs dry before relief, the throttle-to and add-Pro recommendations), and one table row per group plus the ungrouped row with the columns name and attention, accounts (count and health bar), limit templates, keys active/total, limit fill (highest percent and keys at or above 90 %), spend today, 7 d and 30 d, 5 h and weekly remaining with next reset, and projected exhaustion before reset. Every column SHALL be sortable and the default order SHALL be the summary's order. Opening a group, by its row or through `/groups?selected=<id>`, SHALL show a detail sheet with the group's name, its members as an account multi-select that shows each account's status and remaining, its limit templates in the limit-rules editor, its top keys by 7-day spend (at most 20) with a link to `/keys?group=<id>`, Reset period behind the existing confirmation and Delete behind a confirmation. Save SHALL send one `PUT /api/account-groups/{id}` carrying the name, the members and the limits, so that membership and limit templates change atomically and member keys are re-synced as before; Delete SHALL be refused while keys reference the group. Create group SHALL use `POST /api/account-groups/`. The page SHALL poll only `GET /api/groups/summary`, every 60 seconds while the tab is visible, SHALL request the projections once per visit and SHALL keep an open sheet's unsaved draft across polls. Loading SHALL show a table skeleton, a failed refetch SHALL keep the previous data with a retry action, and without groups the page SHALL state that ungrouped keys use the whole pool and offer Create group.

#### Scenario: Edit members and limits

- **WHEN** the operator adds two accounts and a weekly cost limit in a group's sheet and saves
- **THEN** one `PUT /api/account-groups/{id}` carries the new members and limits and the summary is requested again

#### Scenario: Draft survives a poll

- **WHEN** the summary poll returns while the sheet has unsaved member changes
- **THEN** the sheet still shows the unsaved selection

#### Scenario: Group with keys

- **WHEN** the operator confirms Delete on a group that keys still reference
- **THEN** the group is kept and the refusal is shown

#### Scenario: One periodic request

- **WHEN** the Groups page stays open and visible
- **THEN** the only periodic request is `GET /api/groups/summary` every 60 seconds and `GET /api/dashboard/projections` is requested once

### Requirement: Settings keeps only installation-wide settings

Once the Groups and Keys pages exist, the Settings page SHALL NOT contain the API key section (key table, create, edit, delete, regenerate) or the account-group section, SHALL keep the API key authentication toggle and the upstream-quota privacy toggle in an API-key access section, and SHALL NOT offer the dashboard refresh-rate and account burn-projection appearance settings, which no page reads.

#### Scenario: Open Settings

- **WHEN** an operator opens the Settings page
- **THEN** no API key table or account-group list is shown
- **AND** the API key authentication and upstream-quota privacy toggles are shown and save `apiKeyAuthEnabled` and `hideUpstreamQuotaFromApiKeys` through `PUT /api/settings`

## MODIFIED Requirements

### Requirement: Replaced dashboard routes redirect

`/` and `/dashboard` SHALL redirect to `/overview`, `/apis` SHALL redirect to `/keys` with its query string, `/groups` SHALL open the Groups page, `/groups?selected=<id>` SHALL open that group's detail sheet, and `/settings#account-groups` SHALL redirect to `/groups`, carrying its `group` parameter as `selected`. The navigation SHALL list Overview, Groups, Accounts, Keys, Reports, Automations and Settings in that order, and the Accounts entry SHALL carry a badge with the number of accounts needing re-login, or otherwise the available reset credits when reset-credit badges are enabled.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows `/keys?selected=key_1`

#### Scenario: Old group link

- **WHEN** an operator opens `/settings?group=grp_1#account-groups`
- **THEN** the dashboard shows `/groups?selected=grp_1` with that group's sheet open

### Requirement: Replaced admin endpoints are deprecated for one release

An admin endpoint that the embedded dashboard stops calling SHALL stay unchanged in the release that ships its replacement and SHALL be removed no earlier than the following release; release notes SHALL list each deprecation and removal. `GET /api/dashboard/overview` SHALL be deprecated by the overview. `GET /api/dashboard/projections` SHALL stay as the source of the Groups page pool runway card and SHALL NOT be removed while that card uses it.

#### Scenario: Tab opened before the update

- **WHEN** a browser tab loaded before the update still polls `GET /api/dashboard/overview`
- **THEN** the endpoint answers as before until the release after the one that deprecated it

#### Scenario: Pool runway card

- **WHEN** the Groups page loads
- **THEN** it requests `GET /api/dashboard/projections`, which answers as before
