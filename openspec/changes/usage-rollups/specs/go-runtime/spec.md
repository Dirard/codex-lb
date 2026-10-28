## MODIFIED Requirements

### Requirement: Reports and request logs use bounded queries

Admin report responses SHALL be computed with a number of database statements independent of the number of days, and their totals, daily sums, breakdowns, distinct conversation and account counts and comparisons SHALL equal an evaluation over the retained raw events and folded history for every filter, time zone and window. Daily median TTFT, queue latency and tokens per second SHALL come from fixed log-linear histograms and be within 1/64 of the exact median plus rounding; costs SHALL be summed as integer microdollars before conversion. Request-log pages SHALL be ordered through an index on `(requested_at, request_id)`, and the request-log API SHALL accept an optional opaque keyset `cursor`, returned as `nextCursor` when more rows exist, that returns rows strictly after the last returned row in the existing descending order, while offset paging remains supported and the total, conversation aggregate and filter options ignore the cursor. Open reservations SHALL use a partial index on reserved rows.

#### Scenario: Daylight-saving week
- **WHEN** a report window spans a daylight-saving transition in the selected time zone
- **THEN** daily sums equal the raw evaluation and daily medians are within the histogram bound

#### Scenario: Keyset page
- **WHEN** a client passes a page's `nextCursor` as `cursor`
- **THEN** the response equals the next offset page without scanning the skipped rows

## ADDED Requirements

### Requirement: Analytics read incremental usage rollups

The dashboard overview, admin reports, key reports, key usage and trends, request-log filter options and totals, and the conversation list and details SHALL be computed from hour-bucket rollups combined, in one read snapshot, with journal rows not yet folded, events after the fold cursor and raw events of partial hours, so their cost is bounded by the rollup rows of the window rather than by the number of events. When the unfolded tail or journal exceeds its bound, or the rollup layout, status, cursor anchor or import watermark does not match, they SHALL use the raw-event evaluation, which SHALL return identical results; request-log search and conversation filters, and conversation reads while removals of conversation events are pending, SHALL use it too.

#### Scenario: Seven-day dashboard on production volume
- **WHEN** the dashboard overview is requested for seven days with about 200 thousand events in that window
- **THEN** it reads rollup rows of the window and the raw events of at most four partial hours

#### Scenario: Retention removed conversation events
- **WHEN** journal rows that remove events of a conversation are not folded yet
- **THEN** the conversation list and details come from the raw evaluation, with exact first and last requests

#### Scenario: Folder behind after an upgrade
- **WHEN** existing events have not been backfilled yet
- **THEN** analytics responses come from the raw evaluation and equal what the rollups return once folded

### Requirement: Usage rollups stay consistent with every writer

A background folder run only by the active worker SHALL fold events after its cursor in bounded batches through the batch writer, each batch atomic with its cursor. Updates and deletions of folded events SHALL be journaled by triggers in the same transaction by every worker version, including repricing, retention and account history deletion. Inserts by any worker, including an older one during an update overlap, SHALL be folded after their commit. A cursor anchor mismatch, a changed import watermark, a negative measure or a verifier mismatch SHALL make the folder rebuild the rollups while reads use the raw evaluation. Migration 42 SHALL create only empty tables, triggers and the state row.

#### Scenario: Old worker writes during an overlap
- **WHEN** a previous worker settles requests and runs retention while the active worker folds
- **THEN** analytics responses equal the raw evaluation after every step

#### Scenario: Repricing folded history
- **WHEN** a model price change reprices events already folded
- **THEN** the cost delta is journaled in the repricing transaction and reports reflect it immediately
