## MODIFIED Requirements

### Requirement: Reports and request logs use bounded queries

Admin report responses SHALL be computed with a number of database statements independent of the number of days, and their totals, daily sums, breakdowns, distinct conversation and account counts and comparisons SHALL equal an evaluation over the retained raw events and folded history for every filter, time zone and window. Daily median TTFT, queue latency and tokens per second SHALL come from fixed log-linear histograms and be within 1/64 of the exact median plus rounding; costs SHALL be summed as integer microdollars before conversion. Request-log pages SHALL be ordered through an index on `(requested_at, request_id)`, and the request-log API SHALL accept an optional opaque keyset `cursor`, returned as `nextCursor` when more rows exist, that returns rows strictly after the last returned row in the existing descending order, while offset paging remains supported and the total, conversation aggregate and filter options ignore the cursor. Open reservations SHALL use a partial index on reserved rows. Pending request-log entries, reservations still reserved, flagged for reconciliation and without a usage event, SHALL be filtered, counted, grouped into filter options and paged through a covering partial index on flagged reserved reservations, reading at most the first `offset + limit + 1` of them for a page, and their entries, filter options, totals and cursors SHALL equal an evaluation over every reservation.

#### Scenario: Daylight-saving week
- **WHEN** a report window spans a daylight-saving transition in the selected time zone
- **THEN** daily sums equal the raw evaluation and daily medians are within the histogram bound

#### Scenario: Keyset page
- **WHEN** a client passes a page's `nextCursor` as `cursor`
- **THEN** the response equals the next offset page without scanning the skipped rows

#### Scenario: Interrupted usage awaiting reconciliation
- **WHEN** about 74 thousand of 1.6 million reservations are flagged for reconciliation and the request-log options and first page are requested
- **THEN** the pending entries are read from the pending index, reservation rows only for the entries shown, and the entries, options, total and cursor equal the evaluation over every reservation
