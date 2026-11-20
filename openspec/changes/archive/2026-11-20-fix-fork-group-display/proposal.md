## Why

On go-v1.15.0 the owner saw forks under "Ungrouped keys": «форк ключа отображается криво как ungroup keys». A fork stores no group of its own, because its scope is read from its parent at runtime. The dashboard's reads bucket keys by the stored `api_keys.group_id`, so every fork of a grouped key landed among the ungrouped keys. Request handling was not affected: account selection and reservations already use the parent's group.

## What Changes

- Every dashboard read places a fork in its parent's group:
  - the groups page and its key lists;
  - the overview's group rows and attention keys;
  - the keys list, its group filter and the "No group" filter;
  - reports and their CSV export;
  - search, and the key counts per group.
- The groups page marks a fork with its parent's name.
- Resetting a group's usage also resets its keys' forks.
- On a key holder's own pages and reports, a fork shows its parent's group and account pool with the parent's usage sections. It lists only itself among the group's keys, and other holders in the group do not see it.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for the group a fork is shown in.

## Impact

- No schema change. The groups summary gains `parentId` and `parentName` on its keys.
