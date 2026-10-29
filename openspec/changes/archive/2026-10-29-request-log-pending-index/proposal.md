## Why

On a copy of production with usage rollups, `GET /api/request-logs/options` takes 903 ms warm and 4.7 s cold and `GET /api/request-logs` 625 ms and 5.0 s, although events come from the rollups. The 73.8 k reservations flagged for reconciliation among 1.6 M are turned into pending log entries row by row, each with an account-deletion and a usage-event probe and a table read, up to four times per options request, and every page sorts all of them to merge them with events.

## What Changes

- Migration 43 adds `idx_usage_reservations_pending`, a partial index on reservations that are reserved and flagged for reconciliation, covering account, generation, key, model, creation time and id. Ordinary reservations and their settlement never touch it. Additive: building it reads the reservations table once (0.16–0.24 s warm, 0.31–0.40 s cold for 1.6 M reservations in the benchmark).
- Pending entries are read from that index alone: filter options from (account, generation, key, model) groups, with the deleted-account projection evaluated per group and one usage-event probe per present group; the flagged reservations that already have a usage event are found once per request, subtracted from totals and dropped from page candidates; a page reads only the first `offset + limit + 1` pending entries. Conversation, reasoning-effort, non-pending status and effort model-option filters select no pending entry and read none.
- Responses are unchanged field for field, including ordering, cursors, totals, options and the conversation cost aggregate. This replaces the pending CTE that the usage-rollups design (§5) refers to.

## Capabilities

### Modified Capabilities

- `go-runtime`: request-log query bounds for pending entries.

## Impact

- Code: `internal/adapters/sqlite` (pending reader, request-log page, totals and options on both read paths).
- Schema: migration 43, additive; schema floor stays 40.
- API: unchanged.
- Operations: none; the index is built at upgrade while the previous worker serves.
