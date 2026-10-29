## MODIFIED Requirements

### Requirement: Reports page renders English user-facing labels

The dashboard SHALL render `/reports` with the following exact page-owned English labels for the reports for API-key users:

- `Reports`
- `Usage by API key and group`
- `All keys`
- `Ungrouped keys`
- `Copy link`
- `Export CSV`
- `Requests`
- `Tokens`
- `Cost`
- `Errors`
- `Active keys`
- `Groups`
- `Keys`
- `Daily usage`
- `Traffic details`
- `Distribution by Model`
- `Distribution by UserAgent`
- `No usage in this period`
- `Copy owner link`
- `Retry`

Backend-provided strings, key, group, model and user-agent values and raw server error payload text SHALL remain out of scope unless `/reports` renders page-owned labels around them.

#### Scenario: Reports page shows English labels

- **WHEN** an authenticated operator opens `/reports`
- **THEN** the page title is `Reports` with the subtitle `Usage by API key and group`
- **AND** the summary includes `Requests`, `Tokens`, `Cost`, `Errors` and `Active keys`
- **AND** the section titles include `Keys`, `Daily usage` and `Traffic details`

#### Scenario: Reports page state labels are English

- **WHEN** `/reports` renders a period without usage or a failed report
- **THEN** the empty state is `No usage in this period`
- **AND** a failed report offers `Retry`

### Requirement: Reports page loads report data from the reports endpoint

The `/reports` page SHALL load and refetch its report from `GET /api/reports/keys` and SHALL load traffic details from `GET /api/reports` only after the operator expands them.

#### Scenario: Reports page loads from reports endpoint

- **WHEN** an authenticated operator opens `/reports`
- **THEN** the page loads its report from `GET /api/reports/keys`
- **AND** it does not request `GET /api/reports` before the traffic details are expanded

#### Scenario: Reports page refetches from reports endpoint

- **WHEN** an authenticated operator changes the selection or the period on `/reports`
- **THEN** the page requests `GET /api/reports/keys` for the new selection and period

### Requirement: Reports page exposes visible filter controls

The `/reports` page SHALL expose a selection bar with a searchable multi-select over the directory's groups and keys that also offers `All keys` and `Ungrouped keys`, quick periods `Today`, `Yesterday`, `7d`, `30d`, `90d`, `This month` and `Last month`, custom start and end dates, the time zone label, `Copy link` and `Export CSV`. The selected quick period SHALL be highlighted; editing a custom date SHALL clear the highlight. The date inputs SHALL disallow dates later than the browser's current local calendar date. The selection and the period SHALL be kept in the page URL.

#### Scenario: Reports page shows report filter controls

- **WHEN** an authenticated operator opens `/reports`
- **THEN** the page shows the scope picker, the quick periods, the custom dates, `Copy link` and `Export CSV`

#### Scenario: Quick preset highlight follows the selected preset

- **WHEN** an authenticated operator clicks `30d`
- **THEN** `30d` is highlighted and the URL and the report use the last 30 local days

#### Scenario: Quick preset highlight clears after manual date edits

- **WHEN** the operator edits a custom date after choosing a quick period
- **THEN** no quick period is highlighted and the edited dates are kept in the URL

#### Scenario: Report date inputs disallow future dates

- **WHEN** an authenticated operator opens the custom dates on `/reports`
- **THEN** the start date and end date inputs prevent selecting a date later than the browser's current local calendar date

### Requirement: Reports API is accessible through the dashboard route map

The dashboard surface SHALL expose the reports page at route `/reports`, loading its report from `GET /api/reports/keys` with the `group_id`, `api_key_id`, `ungrouped`, `start_date`, `end_date` and `timezone` parameters and its traffic details from `GET /api/reports`.

#### Scenario: Dashboard reports page uses `/api/reports`

- **WHEN** an authenticated operator opens `/reports`
- **THEN** the page loads the report for the selection from `GET /api/reports/keys`
- **AND** uses it to render the summary, the groups and keys tables and the daily chart
- **AND** renders the model and user-agent distribution donuts from `GET /api/reports` when the traffic details are expanded

### Requirement: Reports daily charts fill missing selected days with zero-value rows

The dashboard SHALL render the `/reports` daily usage chart and the traffic-details latency charts from a continuous daily series covering every selected day. When a response omits a selected date, the page SHALL insert a zero-value point for that date before rendering.

#### Scenario: Missing API dates render as zero-value chart points

- **WHEN** an authenticated operator views `/reports` and a daily response omits one or more selected dates
- **THEN** the daily usage chart includes a point for every selected day
- **AND** each omitted date renders with zero cost, requests and tokens

### Requirement: Reports daily charts use symmetric horizontal padding

The `/reports` daily usage chart SHALL use equal left and right horizontal plot padding within its card.

#### Scenario: Daily charts render with balanced left and right inset

- **WHEN** an authenticated operator opens `/reports`
- **THEN** the daily usage chart renders with equal left and right horizontal padding around the plotted area

### Requirement: Reports summary cards show previous-window deltas conservatively

`GET /api/reports` SHALL expose a `comparison` block that includes `canCompare` plus the previous-window totals for cost, tokens and requests; the current and previous windows SHALL use equal calendar-window lengths and `canCompare` SHALL be `true` only when eligible report history fully covers the immediately preceding window. The `/reports` summary SHALL compare with `previous` of `GET /api/reports/keys`: it SHALL hide every previous-period indicator when `previous` is null and SHALL hide a metric's indicator when that metric's previous total is zero.

#### Scenario: Reports summary cards show previous-window increase

- **WHEN** `GET /api/reports/keys` returns `previous` with a lower cost than the current cost
- **THEN** the cost figure renders a visible percentage increase

#### Scenario: Zero previous total suppresses the matching card indicator

- **WHEN** `GET /api/reports/keys` returns `previous` whose total for one metric is `0`, or `previous: null`
- **THEN** that metric, or every metric when `previous` is null, renders no previous-period indicator

#### Scenario: Incomplete previous window suppresses comparison

- **WHEN** the earliest eligible report activity is later than the start of the immediately preceding window
- **THEN** `GET /api/reports` returns `comparison.canCompare: false`

### Requirement: Reports date controls prevent, explain, and recover from inverted ranges

The `/reports` custom start-date input MUST use the earlier of the browser-local current day and a present end date as its native `max`, and the end-date input MUST use a present start date as its native `min` while retaining the browser-local current day as its `max`. If the start date is later than the end date, both controls MUST expose `aria-invalid` and reference the same localized inline corrective message through an accessible description, and the page MUST NOT request `GET /api/reports/keys` or `GET /api/reports`. Correcting either bound MUST clear the invalid state and request the report for the corrected period.

#### Scenario: Reciprocal native bounds prevent routine inverted selection

- **GIVEN** `/reports` has a custom start date and end date
- **THEN** the start-date control's `max` is the earlier of the end date and the browser-local current day
- **AND** the end-date control's `min` is the start date and its `max` the browser-local current day

#### Scenario: Bypassed inverted input is accessible and sends no Reports request

- **WHEN** typed or URL-supplied dates have a start date later than the end date
- **THEN** both date controls expose `aria-invalid` and reference one visible localized corrective message
- **AND** no report request is sent

#### Scenario: Retry while inverted only retries Accounts

- **GIVEN** `/reports` has an inverted date range and the directory failed to load
- **WHEN** the operator activates Retry
- **THEN** only the directory is requested again and no report request is sent

#### Scenario: Correcting either invalid bound resumes Reports queries

- **GIVEN** `/reports` has an inverted date range
- **WHEN** the operator corrects either bound
- **THEN** the corrective message is removed and one `GET /api/reports/keys` request is sent for the corrected period

## REMOVED Requirements

### Requirement: Reports exposes a persisted line-chart visibility filter

**Reason**: The Reports page shows one daily usage chart and loads the latency charts with the traffic details on demand, so there is nothing left to hide.

**Migration**: Expand the traffic details for the TTFT and tokens-per-second charts; the queue-wait chart is dropped because queue wait is near zero in normal operation.

### Requirement: Reports safely persists visibility

**Reason**: The chart-visibility filter is removed.

**Migration**: None; the `codex-lb-reports-visible-charts` storage key is no longer read.

### Requirement: Visibility does not narrow data fetching

**Reason**: The chart-visibility filter is removed.

**Migration**: Traffic details request the complete `GET /api/reports` payload when expanded.

### Requirement: Reports page exposes a visible user-agent filter

**Reason**: A model or user-agent filter across hundreds of keys would scan the usage cube; the page reports on keys and groups.

**Migration**: The user-agent distribution of the selection is in the traffic details; `GET /api/reports` still accepts `useragent_group`.

### Requirement: Reports page preserves reports query parameter names

**Reason**: The page no longer filters `GET /api/reports` by account or model.

**Migration**: The page's own URL carries the selection and the period; requests use the documented `start_date`, `end_date`, `timezone`, `group_id`, `api_key_id` and `ungrouped` parameters.

### Requirement: Daily Breakdown supports explicit visible-column sorting

**Reason**: The daily breakdown table is replaced by the sortable keys table and the daily usage chart.

**Migration**: Per-day and per-key figures are in `Export CSV`; the keys table sorts by every measure.

### Requirement: Daily Breakdown shows cached input tokens inline

**Reason**: The daily breakdown table is removed.

**Migration**: The summary and the keys table show cached input tokens.

### Requirement: Daily Breakdown CSV export stays chronological

**Reason**: The daily breakdown table and its CSV are removed.

**Migration**: `Export CSV` downloads `GET /api/reports/keys/export.csv`, ordered by date.

### Requirement: Daily Breakdown sortable headers show visible sort state

**Reason**: The daily breakdown table is removed.

**Migration**: The keys table shows its sort state on its headers.

### Requirement: Reports daily breakdown renders a continuous calendar window

**Reason**: The daily breakdown table is removed.

**Migration**: The daily usage chart covers every selected day.

### Requirement: Report conversation columns sort and export

**Reason**: The daily breakdown table is removed and the key slices do not count conversations.

**Migration**: `GET /api/reports` keeps its daily and total conversation counts; the Requests page lists conversations.

### Requirement: Dashboard and report metrics count distinct conversations

**Reason**: The dashboard statistics grid was replaced by the Overview page, and the Reports summary reports on keys from slices that do not count conversations.

**Migration**: `GET /api/reports` keeps counting distinct conversations per day and for the window; the Requests page lists conversations.
