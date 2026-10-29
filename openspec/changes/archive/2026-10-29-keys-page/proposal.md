## Why

The operator manages tens to hundreds of keys, but `/keys` still renders the APIs list: a client-side list over `GET /api/api-keys/`, which builds every key with four point queries plus a lifetime usage sum, next to lifetime cards that answer no current question. Finding the keys that are near a limit, expiring, idle or spending the most, and changing many of them at once, needs server-side filtering, sorting by spend of labelled periods, paging and bulk actions. The usage rollups make per-key spend of today, 7 and 30 days cheap enough to compute for every key on each read.

## What Changes

- New `GET /api/keys` (administrator, gzip, `Server-Timing`) returns one page of key rows with the shell status: search by name and prefix, filters by group (one group or ungrouped), status, near limit, expiring within N days and inactive for N days, sorting by name, creation, spend today, 7 days and 30 days, fullest limit and last use, with a stable tie-break by id, `offset` and `limit` (at most 100, default 50) and the total before paging. The server reads the keys with their active limits and group names, and the per-key spend of the local days from the key slices of the usage rollups (raw events for today only while the rollups are unusable), in a fixed number of statements, then filters, sorts and pages in memory.
- New `GET /api/keys/ids` returns the ids of every key matching the same filters, at most 1 000, for "Select all matching".
- New `POST /api/keys/bulk` applies `disable`, `enable`, `move_group` or `set_limits` to at most 1 000 keys in short atomic write jobs (25 keys per job for disable and enable, one key per job for move and limits) and returns a result per key in request order; unknown, deleted and internal ids fail per key, `set_limits` on a grouped key fails with `key_in_group`, a moved key gets its new group's limits exactly as saving the key does.
- The dashboard's `/keys` becomes the Keys page: a toolbar with the search and filters in the URL, a table of 50 rows per page with a sticky header, row checkboxes, sortable spend, limit and last-use columns and a row menu, a bulk bar with confirmation, and pagination. It polls only its current page every 60 seconds while visible and keeps the selection across polls and pages. The overview's links (`near_limit=1`, `expiring_days=7`, `group=<id|ungrouped>`) open the matching filters.
- The APIs page and its lifetime overview cards are removed. `GET /api/api-keys/` is deprecated: unchanged in this release, removed no earlier than the next one.

## Capabilities

### Modified Capabilities

- `go-runtime`: keys list, matching ids, bulk key actions, the Keys page, key links and the deprecation of the key list endpoint.
- `frontend-architecture`: the lifetime API key overview is removed.

## Impact

- Code: `internal/domain` (keys list contracts), `internal/application` (`KeysListService`, `KeysBulkService`), `internal/adapters/sqlite` (key list entities, targeted per-key write jobs reusing the key limit synchronisation), `internal/adapters/httpapi` (three routes), `web/` (`features/keys`, route `/keys`, APIs page removed).
- Schema: none.
- API: additive endpoints; `GET /api/api-keys/` keeps answering for one release.
- Operations: no new settings.
