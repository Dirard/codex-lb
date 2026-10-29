## Why

Account groups are managed in Settings as a plain list with a dialog: the operator sees a group's key count, account count and limit summary, but not whether its accounts are healthy, how full its keys' limits are, what it spends or when its quota runs out. At the planned scale of 50 groups, 300 keys and 100 accounts that needs one table. The overview already computes these figures per group (use case 2.1 of the dashboard redesign), so the Groups page can reuse them, add the 30-day spend, the limit templates and the members, and become the place where groups are edited. The weekly credits pace card left the overview in go-v1.3.0; the owner keeps it as the pool runway card of the Groups page. Settings still holds the key and group sections that the Keys and Groups pages replace, and two appearance settings that no page reads any more.

## What Changes

- New `GET /api/groups/summary?timezone=` (administrator, gzip, `Server-Timing`) returns the shell status, a pool row, one row per group and the ungrouped row computed by the overview's group-row builder (same order and values), each with its 30-day spend, limit templates, member account ids and up to 20 member keys by 7-day spend, and the accounts with status, health and remaining for the member picker. It reads one entity snapshot (with the group limit templates in one statement) and one analytics snapshot (the overview's 8 local days plus the 22 days before them per key, from UTC-day key slices with hour rows at the edges), in a number of statements independent of the number of groups, keys and accounts.
- The Groups page at `/groups`: toolbar with search and Create group, the pool row, the pool runway card from `GET /api/dashboard/projections` (headroom, runway, next relief, throttle-to and add-Pro recommendations), a sortable groups table whose default order is the overview's, and a detail sheet with the name, members (account multi-select with status and remaining), limit templates, the top keys with a link to `/keys?group=<id>`, Reset period and Delete. Save sends one `PUT /api/account-groups/{id}`, so members and limits change atomically. The page polls only the summary, every 60 s while visible; an open sheet keeps its draft across polls. `/groups?selected=<id>` opens the sheet; `/settings#account-groups` redirects to `/groups`.
- Settings no longer shows the API key section and the account-group section; an API-key access section keeps the API-key authentication and upstream-quota privacy toggles. The appearance settings for the dashboard refresh rate and the account burn projection are removed, because no page reads them since the overview replaced the dashboard.
- `GET /api/dashboard/projections` is no longer deprecated: it serves the pool runway card. `GET /api/dashboard/overview` stays deprecated.

## Capabilities

### Modified Capabilities

- `go-runtime`: groups summary, Groups page, Settings content, the Groups route and the deprecation list.
- `frontend-architecture`: the Settings page requirements that named the API key management section.

## Impact

- Code: `internal/domain` (groups summary types), `internal/application` (`GroupsSummaryService` reusing the overview assembly), `internal/adapters/sqlite` (entity snapshot with group limits, 30-day key span from key slices with legacy hours, tail, journal and raw split hours), `internal/adapters/httpapi` (one route), `web/src/features/groups` (new page), `web/src/features/settings` (sections removed), `web/src/features/account-groups` (Settings section removed).
- Schema: none.
- API: one additive admin endpoint; `GET /api/dashboard/projections` kept; the public key-usage endpoints are unchanged.
- Operations: no new settings; stored browser preferences for the removed appearance settings are ignored.
