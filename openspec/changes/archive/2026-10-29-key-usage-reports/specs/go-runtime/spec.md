## ADDED Requirements

### Requirement: Reports for API-key users resolve a selection of groups and keys in one bounded read

`GET /api/reports/keys` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts only the repeatable `group_id` and `api_key_id` parameters, `ungrouped=1`, `start_date` and `end_date` (`YYYY-MM-DD`, default the 7 local days ending today) and `timezone` (IANA, default UTC). It SHALL resolve, in one analytics read snapshot, the selected groups to their current non-deleted, non-internal member keys and unite them with the explicitly selected keys of that kind and, with `ungrouped=1`, the keys without a group; an empty selection SHALL select every non-deleted, non-internal key, and unknown ids SHALL select nothing. It SHALL return the period with the previous period of the same length, the selection with its resolved key count, the totals (requests, input, output, cached input and reasoning tokens, estimated cost in USD, errors, cancelled), the previous period's totals or null when the previous period has no requests, the number of keys with requests, one row per group of the resolved keys and one for the ungrouped keys with key counts, measures and cost share, one row per resolved key with its group, status, measures, cost share, last use and fullest current limit, and one entry per local day with the day's totals and the measures of up to 8 series ranked by cost plus `other`: groups when the selection names more than one group (counting the ungrouped keys as one) or is empty and the resolved keys belong to more than one group, otherwise keys. Every measure of a key and local day SHALL equal the raw evaluation of the reported events and folded legacy hours of that key and day, cost up to the final rounding to six decimals; keys count in their current group for every day. While the rollups are usable the endpoint SHALL read key slices, folded legacy hours, the journal, the unfolded tail and raw partial hours only, with a number of SQL statements independent of the number of keys, groups and days. A period longer than 731 days, an inverted or malformed date SHALL return 400 `invalid_report_date_range`; an unknown parameter, a blank or longer than 256 characters id, more than 200 group ids or more than 1000 key ids or another `ungrouped` value SHALL return 400 `invalid_report_filters`; an unknown zone SHALL return 400 `invalid_timezone`; a selection whose resolved keys times days exceed 500 000 SHALL return 400 `report_too_large`.

#### Scenario: Group selected after a key moved

- **WHEN** a key that spent last week in group A now belongs to group B and the report selects group B
- **THEN** its spend of every day of the period counts in the report and in group B

#### Scenario: Ungrouped keys and pruned days

- **WHEN** the report selects only the ungrouped keys for a period whose first days were pruned into folded legacy hours
- **THEN** the per-key and per-day measures equal the raw evaluation of the ungrouped keys' events and legacy hours of each local day

#### Scenario: Statement count at scale

- **WHEN** the report is computed for 30 and for 300 keys over the same period
- **THEN** both evaluations execute the same number of SQL statements

#### Scenario: Several groups

- **WHEN** the report selects three groups
- **THEN** the daily series are those groups ranked by cost, with `other` only when more than 8 series have requests

### Requirement: Key usage export is a per-day CSV that matches the report

`GET /api/reports/keys/export.csv` SHALL accept the parameters of `GET /api/reports/keys` with the same validation and SHALL answer `text/csv; charset=utf-8` with `Content-Disposition: attachment`, the header `date,group_id,group_name,key_id,key_name,requests,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,errors,cancelled` and one row per local day and resolved key with requests, ordered by date, group name with ungrouped keys last, key name and key id. `reasoning_tokens` SHALL be empty when unknown, `cost_usd` SHALL carry six decimals and text cells starting with `=`, `+`, `-`, `@`, tab or carriage return SHALL be prefixed with an apostrophe. The rows SHALL be computed in one analytics read snapshot that is closed before the first row is written, and their sums SHALL equal the totals of `GET /api/reports/keys` for the same parameters.

#### Scenario: Sums match the report

- **WHEN** an operator exports the CSV for the parameters of a report
- **THEN** the sums of every numeric column equal the report's totals, cost up to the six-decimal rounding of each row

#### Scenario: Idle keys

- **WHEN** a selected key has no requests on a day
- **THEN** the CSV has no row for that key and day

### Requirement: The Reports page reports on key users by selection and period

The Reports page SHALL show the report of `GET /api/reports/keys` for a selection of groups and keys chosen in a searchable picker over the directory (every key when nothing is selected) and one period picker (Today, Yesterday, 7 days by default, 30 days, 90 days, This month, Last month, or custom dates up to 730 days ending no later than today), SHALL encode the selection and the period in its URL so that a copied link reopens the same report, SHALL offer Export CSV for the same parameters, and per key an Open action to the key page and a Copy owner link action whose link opens the key sign-in with the report period and carries no credential. It SHALL load model, user-agent and latency details from `GET /api/reports` only when the operator expands them, filtered by the resolved keys when at most 50 are selected, unfiltered for every key, and otherwise SHALL ask the operator to narrow the selection. It SHALL NOT poll on an interval and SHALL refetch on window focus only when the period includes today and the data are older than 60 seconds. It SHALL NOT request `/api/accounts` or `/api/request-logs/options`.

#### Scenario: Shared link

- **WHEN** an operator copies the link of a report on two groups for the last 30 days and opens it in another tab
- **THEN** the same two groups and the last 30 days are selected and one `GET /api/reports/keys` request is sent

#### Scenario: Traffic details on demand

- **WHEN** the Reports page loads
- **THEN** no `GET /api/reports` request is sent until the operator expands the traffic details

#### Scenario: Idle tab

- **WHEN** a Reports page for the last 7 days stays open and visible
- **THEN** no further report request is sent until the window regains focus at least 60 seconds after the last response
