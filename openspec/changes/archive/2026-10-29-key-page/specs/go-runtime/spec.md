## ADDED Requirements

### Requirement: The key page reads one key's period in one bounded read

`GET /api/api-keys/{id}/page` (administrator) and `GET /api/key-reports/page` (key owner) SHALL accept `start_date` and `end_date` (`YYYY-MM-DD`, inclusive local days, default the 7 days ending today, at most 730 days) and `timezone` (IANA, default UTC; an unknown zone SHALL return 400 `invalid_timezone`; an invalid range SHALL return 400 `invalid_report_date_range`) and SHALL return, for that key and period: the key's properties and status, its current limits, the period summary (requests, input, output, cached and reasoning tokens, cost, errors, cancelled, TTFT p50 and p95, total latency p50 and p95) with the requests, tokens and cost of the previous period of equal length, one row per local day (requests, tokens, cost, errors, the top five models of the period plus the rest, TTFT p50 and p95, total latency p50 and p95, queue latency p95, tokens per second p50), one row per model (requests, tokens, cost, share, TTFT p50), errors by code (count, share, last seen hour), user-agent groups (requests, cost, share) and the first 50 recent requests with a `nextCursor`. Usage SHALL be read from the usage rollups for whole hours combined with raw partial hours, the unfolded tail, the journal and folded legacy hours, in one analytics snapshot, with a number of SQL statements independent of the period length; while the rollups are unusable the same values SHALL be computed from raw events of the key. Percentiles SHALL use the rollup histograms' interpolated quantile and their documented relative bound, also on the raw path. Folded legacy hours carry no latency, user-agent groups or error codes; the response SHALL report from when these details are complete. Reading a key page MUST NOT mutate limit counters, reservations or any other stored state.

#### Scenario: Statement count does not grow with the period

- **WHEN** the key page is read for 7 days and for 90 days of the same key
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Rollups and raw events agree

- **WHEN** the key page usage is read in Europe/Prague or Asia/Kolkata while the folder stopped at a random cursor or journal rows are pending
- **THEN** every day, model, user-agent, account and error-code row equals the evaluation of the key's raw events

#### Scenario: Pruned days

- **WHEN** the period includes days whose events were pruned into legacy hourly rows
- **THEN** their requests, tokens and cost are included and the response states that latency, user agents and error codes are complete only from the first unpruned instant

### Requirement: Key limits show their reset and projected exhaustion

Each active limit of the key page SHALL show its type, window, model filter, current and maximum values, percent and reset time. An expired window SHALL show zero usage and the advanced reset without writing storage. The page SHALL project the exhaustion time from the key's consumption of the limit's measure in the last 3 hours (tokens or cost, restricted to the limit's model filter), and SHALL report it only when that pace reaches the maximum before the reset; a credits limit, a zero pace or a limit already at its maximum SHALL have no projection.

#### Scenario: Runs out before reset

- **WHEN** a daily cost limit is at 80 % with 6 hours to its reset and the last 3 hours spent 10 % of the maximum
- **THEN** the projected exhaustion is 6 hours from now, when the remaining 20 % is spent at that pace, which is not after the reset

#### Scenario: Lasts until reset

- **WHEN** the same pace would reach the maximum only after the reset
- **THEN** no projected exhaustion is reported

### Requirement: Key recent requests page through the key index

`GET /api/api-keys/{id}/requests` and `GET /api/key-reports/requests` SHALL accept `cursor` (an opaque keyset cursor from a previous `nextCursor`), `limit` (1 to 100, default 50) and `errors_only` (`true` or `false`) and SHALL return `{items, nextCursor}` for the key, newest first in `(requested_at, request_id)` order, read through `idx_usage_events_key_time` and merged with the key's pending entries of the request log. Cursor pages SHALL equal the corresponding offset pages. With `errors_only=true` the read SHALL examine at most 5 000 of the key's rows per call and return the failed ones (status other than success, completed or cancelled), with a `nextCursor` that continues after the last examined row when the bound was reached.

#### Scenario: Load more

- **WHEN** the dashboard passes a page's `nextCursor` as `cursor`
- **THEN** the next rows equal the following offset page without scanning the skipped rows

#### Scenario: Rare errors

- **WHEN** a key has one failed request among 20 000 successful ones
- **THEN** each errors-only call reads at most 5 000 rows and returns a cursor until the failed request is found

### Requirement: The key page is one dashboard component for the administrator and the owner

The administrator page `/keys/:id` and the key owner's page after key sign-in SHALL be one React component with an administrator and an owner mode. It SHALL show, in order: the header (name, prefix, status, group, created, last used, expiry), one period picker (Today, 7 days by default, 30 days, 90 days, custom up to 730 days) reflected as `from` and `to` in the URL, the current limits with the pool or group remaining, the period KPIs with the previous-period comparison, usage by day and model, errors by code, latency per day, user agents, recent requests with an errors-only toggle and "load more", and the key properties; the administrator mode adds the actions Edit, Edit limits or Open group limits, Move to group, Disable or Enable, Regenerate, Reset period (for a grouped key, naming the whole group), Delete and Copy owner link, the account, client IP and conversation of each request with its details, cost by account and lifetime totals labelled "Lifetime". The page SHALL poll its page endpoint every 60 seconds while visible only when the period includes today. The owner mode SHALL NOT mount the administrator navigation or issue administrator requests, SHALL end the session on a 401 like the key report and SHALL keep showing the previous data with a retryable error on other failures.

#### Scenario: Historical period does not poll

- **WHEN** the operator selects a period that ends yesterday
- **THEN** the page endpoint is requested once and not polled

#### Scenario: Owner mode

- **WHEN** a key owner signs in with the key
- **THEN** the key page renders without the administrator navigation, actions, account columns, cost by account or lifetime totals, and no `/api/` request outside `/api/key-reports/` is made

### Requirement: Owner key page data is bound to the authenticated key and redacted

The owner routes SHALL bind every section to the key of the report session, SHALL accept only `start_date`, `end_date` and `timezone` (page) and `cursor`, `limit` and `errors_only` (requests), each at most once, and SHALL reject any other parameter with 400 `invalid_report_filters`. Owner responses SHALL omit account ids, e-mails and plan types, client IPs, full user-agent strings, conversation ids, error messages, model-source ids, cost by account, lifetime totals, the shell status and assigned account ids (only their counts remain), and SHALL include request ids, error codes and user-agent groups. A grouped owner key SHALL keep the same-group mini-list of the key report with the same safe fields, derived from the key's current membership in the read snapshot; a key that is disabled, expired or deleted by the time of that snapshot SHALL be rejected as unauthenticated.

#### Scenario: Key A cannot read key B

- **WHEN** an owner signed in with key A passes key B's id, account ids or any other selector as a parameter
- **THEN** the request is rejected with 400 and no data of key B is returned

#### Scenario: Omitted fields

- **WHEN** an owner reads the key page and its requests
- **THEN** the JSON contains no `accountId`, `clientIp`, `conversationId`, `useragent`, `errorMessage`, `byAccount`, `lifetime`, `shell` or `assignedAccounts` fields

### Requirement: Owner key page shows the upstream remaining only when visible

The pool or group remaining section (5-hour and weekly remaining with next reset and projected exhaustion, and the purchased-credit summary) SHALL appear in owner scope only when upstream quotas are visible to the key: `HideUpstreamQuotaFromKeys` is off and the key's usage sections include `account_pool_usage`. For an ungrouped key it SHALL cover the whole ChatGPT account pool, for a grouped key the group's non-deleted ChatGPT accounts; only aggregate values and counts SHALL be exposed.

#### Scenario: Hidden upstream quotas

- **WHEN** `HideUpstreamQuotaFromKeys` is on, or the key's usage sections omit `account_pool_usage`
- **THEN** the owner page response has `pool: null` for grouped and ungrouped keys, while the administrator page still shows it

### Requirement: Owner key page routes keep the key-report session rules

`GET /api/key-reports/page` and `GET /api/key-reports/requests` SHALL authenticate only with the report-session cookie, SHALL revalidate the key's active, expiry, internal status and credential fingerprint on every read, SHALL apply the proxy IP allowlist, SHALL send `Cache-Control: no-store` and `Vary: Cookie`, SHALL be gzip-compressed when the client accepts it, SHALL send `Server-Timing: app;dur=<ms>`, SHALL accept no mutation method and MUST NOT consume key quota, change limit counters or reservations or create administrator sessions.

#### Scenario: Key rotated mid-session

- **WHEN** the key of an open owner page is regenerated, disabled or deleted
- **THEN** the next page or requests read answers 401 and the dashboard ends the report session

#### Scenario: Firewall

- **WHEN** a client outside the proxy IP allowlist reads the owner page
- **THEN** the request is denied with 403 `ip_forbidden`

### Requirement: Owner links keep the period through key sign-in

The dashboard entry SHALL keep the `from` and `to` query parameters through key sign-in and SHALL open the owner key page for that period; when they are present the key sign-in method SHALL be preselected. A link copied from the administrator key page or from Reports SHALL be the dashboard origin with only `from` and `to` and SHALL NOT contain or grant any credential: the owner still signs in with the key.

#### Scenario: Link from the administrator

- **WHEN** the operator copies the owner link of a key for 2026-10-01 to 2026-10-28 and the owner opens it and signs in with the key
- **THEN** the owner page shows 2026-10-01 to 2026-10-28

### Requirement: Key links open the key page

Key links from the global search, the overview and the APIs list SHALL open `/keys/<id>`; `/keys?selected=<id>` and `/apis?selected=<id>` SHALL redirect to `/keys/<id>`, while `/keys` without a selection keeps the key list until the Keys page replaces it.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows the key page `/keys/key_1`

### Requirement: Endpoints replaced by the key page are deprecated for one release

`GET /api/api-keys/{id}/trends`, `GET /api/api-keys/{id}/usage-7d` and `GET /api/key-reports/reports` SHALL stay unchanged in the release that ships the key page, which the embedded dashboard no longer calls, and SHALL be removed no earlier than the following release; the release notes SHALL list the deprecation.

#### Scenario: Tab opened before the update

- **WHEN** a key report tab loaded before the update still requests `GET /api/key-reports/reports`
- **THEN** it answers as before
