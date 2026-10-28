## Why

On production data (742 k usage events in 30 days) the dashboard overview takes 5.2 s for 7 days and 14.6 s for 30 days, reports 2.9–8.6 s and request-log filter options 7.4–15.8 s, because every request recomputes from raw events, and the dashboard repeats this every 15–30 s on a three-connection analytics pool. With hundreds of API keys and many groups this only grows.

## What Changes

- Hour-bucket usage rollups (totals, a key/account/model/user-agent cube, one-dimension slices at hour and day grain, error codes, conversation-hours, request-log facets and global facet values) are maintained by a background folder that advances a cursor over `usage_events` and by triggers that journal updates and deletions of folded rows; nothing runs on insert, so settlement is unchanged.
- Every analytics read combines rollups, pending journal rows, the unfolded tail and raw partial-hour edges in one snapshot, so results are exact except daily medians; when the folder is behind or the layout differs, reads use the raw implementation.
- Daily median TTFT, queue latency and tokens per second come from fixed log-linear histograms (≤ 1/64 relative error); costs are summed as integer microdollars. The raw implementation uses the same rules, so both paths return identical results.
- Migration 42 only creates empty tables, triggers and a state row (additive, milliseconds); existing events are backfilled by the folder in small batches after the upgrade.
- An hourly verifier compares both paths and rebuilds the rollups on a mismatch.

## Capabilities

### Modified Capabilities

- `go-runtime`: report and request-log query bounds, analytics read paths, report median semantics.

## Impact

- Code: `internal/adapters/sqlite` (rollup schema, folder, readers, raw-path median and cost rules), `internal/application` (rollup service), `cmd/codex-lb` (background wiring).
- Schema: migration 42, additive; schema floor stays 40.
- API: response shapes unchanged; daily medians become histogram approximations, costs may differ by 0.0001 on rounding ties.
- Operations: no new settings; after the upgrade the folder backfills existing events in about a minute while reads use the raw path.
