## ADDED Requirements

### Requirement: The keys list filters, sorts and pages keys on the server in one bounded read

`GET /api/keys` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts, each at most once, `q` (at most 256 characters, matched case-insensitively as a substring of the key name or prefix), `group` (a group id or `ungrouped`), `status` (`active`, `disabled` or `expired`), `near_limit` (`1`, `true`, `0` or `false`), `expiring_days` and `inactive_days` (whole days from 1 to 365), `sort` (`name` by default, `created`, `spend_today`, `spend_7d`, `spend_30d`, `limit_fill` or `last_used`), `order` (`asc` or `desc`; by default `asc` for `name` and `desc` for every other sort), `offset` (from 0, default 0), `limit` (1 to 100, default 50) and `timezone` (IANA, default UTC). Any other or repeated parameter and any invalid value SHALL return 400 `invalid_key_filters`; an unknown zone SHALL return 400 `invalid_timezone`. The response SHALL carry the shell status, the number of matching keys before paging as `total`, the applied `offset` and `limit`, and one row per key of the page with its id, name, prefix, group id and group name, status, expiry, last use, creation, the estimated spend in USD of the current local day, of the 7 local days ending today and of the 30 local days ending today, the fullest current limit with its percent and reset, and the type, window, model filter and maximum of each active limit. Only non-deleted, non-internal keys SHALL be listed, and the filters SHALL combine with AND. Rows SHALL be ordered by the sort key and then by id ascending; keys without a value for the sort key (no limit, never used, unknown period spend) SHALL follow the keys with a value in either order. The server SHALL read the keys with their active limits, the group names and the per-key spend of the 30 local days ending today in one analytics snapshot, the spend from the key slices of the usage rollups with folded legacy hours, the journal, the unfolded tail and raw partial hours, then filter, sort and page in memory, with a number of SQL statements independent of the number of keys; while the rollups are unusable it SHALL read the raw events of the current local day only and return the 7- and 30-day spend as null.

#### Scenario: Statement count does not grow with the keys

- **WHEN** the keys list is read for 30 and for 300 keys
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Ties keep a stable order across pages

- **WHEN** several keys have the same spend today and the list is sorted by spend today and read page by page
- **THEN** the tied keys are ordered by id and every key appears on exactly one page

#### Scenario: Paging total

- **WHEN** 120 keys match and the second page of 50 is requested
- **THEN** the response has `total` 120, `offset` 50 and 50 rows

#### Scenario: Rollups unusable

- **WHEN** the rollups are not usable after an upgrade
- **THEN** the spend today of every key equals the raw evaluation of today's events and its 7- and 30-day spend are null

### Requirement: Key list filters have exact boundaries

For the keys list and its matching ids, status `disabled` SHALL mean the key is disabled, `expired` an enabled key with `expires_at ≤ now` and `active` an enabled key that has not expired; "near limit" SHALL mean an active key whose fullest active limit, with an expired window counted as zero, is at or above 90 %; "expiring within N days" SHALL mean `now < expires_at ≤ now + N × 24 h`; "inactive for N days" SHALL mean `last_used_at` absent or earlier than `now − N × 24 h`. Deleted keys and the internal keys SHALL never be listed or counted.

#### Scenario: Inactive boundary

- **WHEN** one key was last used exactly 7 days ago, one 7 days and one millisecond ago and one never
- **THEN** `inactive_days=7` lists the last two

#### Scenario: Expiry boundary

- **WHEN** one key expires exactly 7 days from now, one a millisecond later and one expired a moment ago
- **THEN** `expiring_days=7` lists only the first and `status=expired` lists only the last

#### Scenario: Deleted and internal keys

- **WHEN** a key is deleted
- **THEN** neither it nor the internal keys appear in any filter or in `total`

### Requirement: Select all matching returns the ids of the matching keys

`GET /api/keys/ids` SHALL be an administrator endpoint behind JSON compression that sends `Server-Timing` and accepts the filter, `sort`, `order` and `timezone` parameters of `GET /api/keys`, not `offset` and `limit`, with the same validation. It SHALL return `{ids, total}`: the ids of the matching keys in list order, at most the first 1 000, and the number of matching keys.

#### Scenario: More than a thousand matches

- **WHEN** 1 200 keys match
- **THEN** the response lists the first 1 000 ids in list order and `total` is 1 200

### Requirement: Bulk key actions apply in short atomic jobs with per-key results

`POST /api/keys/bulk` SHALL be an administrator endpoint that accepts `{ids, action, groupId, limits}`: 1 to 1 000 distinct non-empty ids of at most 256 characters, `action` one of `disable`, `enable`, `move_group` and `set_limits`, `groupId` (required for `move_group` and only there: a group id, or null for no group) and `limits` (required for `set_limits` and only there: the key limit rules, where an empty list removes every limit). A malformed JSON body SHALL return 400 `invalid_request`, any other invalid body 400 `invalid_bulk_request` and invalid limit rules 422 `invalid_limit`. A valid request SHALL return 200 `{results: [{id, ok, code}]}` with one result per id in request order, where `code` is present only for a failed key. Unknown, deleted and internal ids SHALL fail with `not_found`. `disable` and `enable` SHALL run in write jobs of at most 25 keys, and `move_group` and `set_limits` in one write job per key, because saving a key may backfill its new limit windows; every job SHALL be atomic, a failed job SHALL fail only its own keys and SHALL NOT undo earlier jobs, and later jobs SHALL still run unless the request is cancelled, after which the remaining keys fail with `not_applied`. `move_group` SHALL set the key's group and apply the target group's limits, and enable the account-assignment scope for a group, exactly as saving the key with that group does; leaving a group SHALL keep the key's last effective limits, and an unknown target group SHALL fail with `invalid_group`. `set_limits` SHALL replace the limits of an ungrouped key exactly as saving the key with those limits does and SHALL fail with `key_in_group` for a grouped key. A job SHALL change only the active state, the group with its implied account-assignment scope or the limits of a key, and SHALL NOT overwrite its other fields or its last use.

#### Scenario: Failure in the middle of a move

- **WHEN** five keys are moved to a group and the third key's job fails
- **THEN** the first two keys stay moved with the group's limits, the third reports its failure code and the last two are moved

#### Scenario: Limits for a grouped key

- **WHEN** `set_limits` is applied to one ungrouped and one grouped key
- **THEN** the ungrouped key has the new limits and the grouped key fails with `key_in_group` and keeps its group limits

#### Scenario: Unknown ids

- **WHEN** the ids include an unknown id, a deleted key and an internal key
- **THEN** each of them fails with `not_found` in its request position and the other keys are changed

### Requirement: The Keys page manages hundreds of keys

`/keys` SHALL show the Keys page: a toolbar with the search, the group filter (any, a group of the directory or ungrouped), the status filter, near limit, expiring within 7 days and inactive for 7, 30 or 90 days, Create key and the number of matching keys; a table of at most 50 rows per page with a sticky header, a checkbox per row and one for the page, and the columns name with prefix, group, status, spend today, 7 days and 30 days, fullest limit with reset, limits, expiry, last use and creation, sortable by name, the three spend columns, fullest limit, last use and creation; previous and next page with "page n of m". A row click SHALL open `/keys/<id>` and a row menu SHALL offer Edit, Disable or Enable, Regenerate and Delete. Search, filters, sort and page SHALL live in the URL under the parameter names of `GET /api/keys`, so that `/keys?near_limit=1`, `/keys?expiring_days=7` and `/keys?group=<id|ungrouped>` open those filters. The page SHALL request `GET /api/keys` for its current page only, every 60 seconds while visible, SHALL take the shell status from that response and SHALL NOT request `GET /api/api-keys/`. The selection SHALL survive polls, sorting and page changes until it is cleared, and "Select all n matching" SHALL select the ids returned by `GET /api/keys/ids`. With keys selected, a bulk bar SHALL offer Disable, Enable, Move to group (including no group), Set limits for ungrouped keys and Clear; every bulk action SHALL ask for a confirmation that names the action and the number of keys, SHALL list the keys that failed with their reason, and SHALL refresh the keys list, the directory and the overview afterwards. While the first page loads the table SHALL show a skeleton with a usable toolbar; a failed refetch SHALL keep the previous page with a retryable error; an empty result SHALL offer to reset the filters, or to create the first key when no key exists.

#### Scenario: Overview link

- **WHEN** the operator follows the overview's link to keys near their limit
- **THEN** `/keys?near_limit=1` opens with the near-limit filter set and requests `GET /api/keys` with `near_limit=1`

#### Scenario: Selection across a poll and a page change

- **WHEN** the operator selects two keys, the page is polled and the operator opens the next page and selects one more
- **THEN** the bulk bar counts three selected keys

#### Scenario: Bulk confirmation

- **WHEN** the operator chooses Disable with 12 keys selected
- **THEN** a confirmation naming the action and 12 keys is shown, and only after confirming one `POST /api/keys/bulk` is sent

### Requirement: The key list endpoint is deprecated for one release

`GET /api/api-keys/` SHALL stay unchanged in the release that ships the Keys page, whose embedded dashboard pages no longer call it, and SHALL be removed no earlier than the following release; the release notes SHALL list the deprecation.

#### Scenario: Tab opened before the update

- **WHEN** a dashboard tab loaded before the update still requests `GET /api/api-keys/`
- **THEN** it answers as before

## MODIFIED Requirements

### Requirement: Key links open the key page

Key links from the global search, the overview and the Keys page SHALL open `/keys/<id>`; `/keys?selected=<id>` and `/apis?selected=<id>` SHALL redirect to `/keys/<id>`, `/apis` with other parameters SHALL redirect to `/keys` with them, and `/keys` without a selection SHALL show the Keys page.

#### Scenario: Old bookmark

- **WHEN** an operator opens `/apis?selected=key_1`
- **THEN** the dashboard shows the key page `/keys/key_1`

#### Scenario: Old list bookmark

- **WHEN** an operator opens `/apis?group=grp_1`
- **THEN** the dashboard shows the Keys page `/keys?group=grp_1` filtered to that group
