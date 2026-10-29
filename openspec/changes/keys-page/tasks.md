## 1. Reads and writes

- [x] 1.1 SQLite: the key list entities (non-deleted, non-internal keys with active limits and group names) in one read snapshot; per-key spend of the 30 local days ending today through the overview usage reader (key slices, folded legacy hours, journal, tail and raw partial hours; raw today only while the rollups are unusable); statement count independent of the number of keys.
- [x] 1.2 SQLite: bulk write jobs that set the active state of up to 25 keys, move one key to a group with the group's limits, and replace one ungrouped key's limits, reusing the key limit synchronisation of `SaveAPIKey`; unknown, deleted and internal keys reported per key.

## 2. Use cases and HTTP

- [x] 2.1 `KeysListService`: filters, sorts with a stable tie-break by id, paging with the total before paging, matching ids (at most 1 000); boundary tests for inactive, expiring, expired and near limit; deleted and internal keys excluded.
- [x] 2.2 `KeysBulkService`: validation, jobs of 25 keys for disable and enable and one key per job for move and limits, per-key results in request order, partial failure and cancellation.
- [x] 2.3 `GET /api/keys`, `GET /api/keys/ids` (gzip, `Server-Timing`) and `POST /api/keys/bulk` with parameter and body validation and administrator authentication.

## 3. Dashboard

- [x] 3.1 `features/keys`: `KeysPage`, `KeysTable`, `KeysFilters`, `BulkActionsBar` and the URL-backed `useKeysList` at `/keys`; polling of the current page every 60 s while visible; selection across polls and pages; "Select all matching".
- [x] 3.2 Bulk confirmation naming the action and the count, per-key failures, invalidation of the keys list, the directory and the overview; overview links mapped to the filters; `/apis` and `/keys?selected=` redirects kept.
- [x] 3.3 APIs page and `ApiKeysOverview` removed; locales `en`, `ko`, `zh-CN`; MSW handlers and coverage; component and flow tests.

## 4. Verification

- [ ] 4.1 Gates: Go build, vet, lint, tests, race; web lint, typecheck, tests, build; OpenSpec strict validation.
- [x] 4.2 Contract tests for the new endpoints; `BenchmarkDashboardPages` for the keys list at target scale.
