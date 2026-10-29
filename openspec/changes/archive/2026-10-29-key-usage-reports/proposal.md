## Why

The Reports page answers "how much did the installation spend" but not "how much did each key holder spend": it has no per-key breakdown, no group selection, a free-text model filter, a second catalog report and two history-wide option reads only to list names. At the planned scale of 300 keys and 50 groups the operator needs to choose groups or keys, see totals, per-group and per-key spend and a daily series, export them for billing and hand each key owner a link to their own page. The usage rollups make per-key local-day reads cheap enough to serve that from one request.

## What Changes

- New `GET /api/reports/keys?group_id&api_key_id&ungrouped&start_date&end_date&timezone` (admin, gzip, `Server-Timing`) resolves the selection to current keys (groups by current membership, explicit keys, the ungrouped keys; an empty selection is every key) and returns totals with the previous period of the same length, per-group and per-key measures, active keys and a daily series of the top 8 keys, or of the top 8 groups when several groups are selected, plus other. It reads key slices, folded legacy hours, the journal, the unfolded tail and raw partial hours in one analytics snapshot with a fixed number of statements; periods are at most 731 days.
- New `GET /api/reports/keys/export.csv` with the same parameters streams one CSV row per local day and key with requests after the read snapshot is closed; its sums equal the JSON report.
- The Reports page is rebuilt around the selection: a searchable scope picker over directory groups and keys, one period picker (Today, Yesterday, 7 d, 30 d, 90 d, This month, Last month, custom), selection and period in the URL, Copy link, Export CSV, a summary with the previous-period comparison, a groups table, a keys table grouped by group with subtotals and per-key Open and Copy owner link actions, a daily chart and traffic details (model and user-agent distributions, TTFT and tokens-per-second medians from the existing `GET /api/reports`) loaded only when expanded. The page polls nothing; it refetches on focus only when the period includes today and the data are older than 60 seconds.
- Removed from the page: the account, model and user-agent filters, the catalog report, the chart-visibility filter, the queue-wait chart, the cost and tokens per day charts and the daily breakdown table, key names from `/api/request-logs/options`, the duplicate `/api/accounts` request and the 60-second report poll. `GET /api/reports` itself is unchanged.

## Capabilities

### Modified Capabilities

- `go-runtime`: reports for API-key users, their CSV export and the Reports page contract.
- `frontend-architecture`: the Reports page requirements this change replaces or removes; the distribution, chart tooltip and time-zone requirements apply to traffic details.

## Impact

- Code: `internal/domain` (key selection and report types), `internal/application` (`KeyUsageReportService` with its own port), `internal/adapters/sqlite` (per-key local-day measures from key slices with legacy hours, tail, journal and raw partial hours; raw fallback), `internal/adapters/httpapi` (two routes), `web/src/features/reports` (rebuilt page).
- Schema: none.
- API: two additive admin endpoints; `GET /api/reports` and the public key-usage endpoints are unchanged; no endpoint is deprecated.
- Operations: no new settings.
