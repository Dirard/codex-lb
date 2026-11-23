# Roll back Google Antigravity accounts with a forward release

- Last edited with skill pack: `0.2.2`

## Title and scope

The owner rolled back the Antigravity accounts: «откати все изменения и релиз до версии "до антигравити"… это была плохая идея», done as a forward release («можно через форвард релиз»). go-v1.20.0 restores the go-v1.16.0 code and specifications, keeps the race-gate change of #57, and retires the Antigravity sources that installations created on go-v1.17.0 – go-v1.19.0.

## Planning anchor

- Entrypoints: merges #56 (Antigravity sources), #58 (client version setting), the direct commit `9a3ec581` (User-Agent change that also deleted the whole `openspec/` tree), and #59 (Chrome 123 transport); migrations 53 and 54.
- Changed assumption: Antigravity sources are a supported model source kind. Production ran go-v1.19.0 at schema 54 with one active Antigravity source when the owner decided.
- Non-local: every package the reverted merges touched, the SQLite migration runner, OpenSpec `go-runtime`, release notes.
- OpenSpec change `retire-antigravity-sources`, archived as `openspec/changes/archive/2026-11-23-retire-antigravity-sources/`: `go-runtime` ADDED «Withdrawn Antigravity sources are retired on upgrade».
- Impacted specs: `specs/antigravity-accounts.md` and `specs/antigravity-chrome-transport.md` are removed with the code (action: `archive`, kept in Git history at `10e8226e`). `specs/sticky-only-accounts.md` stays authoritative (action: `reuse`).

## Connected groups or observed existing logic

### Observed Existing Logic

- `git revert` of #59, `9a3ec581`, #58 and #56, newest first, applies cleanly; the result differs from go-v1.16.0 only by #57.
- `Store.migrate` runs SQL migrations, then Go steps keyed by the starting version (`migrateDeletedAccountsTx` for versions below 31).
- `markAccountDeletedTx` is the operator deletion boundary: it records the deletion and removes credentials, group and key scopes, quotas, outcomes, continuations, resource owners and affinity bindings.
- Without retirement the restored code would read an `antigravity` source as an ordinary one and send its stored refresh token as an API key.

## Use cases

### 1. Retire Antigravity sources on upgrade

installation at schema 53 or 54 --keep migrations 53 and 54--> unchanged columns --mark Antigravity sources deleted--> deleted sources --delete their accounts through the deletion boundary--> schema 55 without Antigravity sources or tokens

Requirements:

  R1:
Migrations 53 and 54 SHALL stay in the migration list unchanged. Migration 55 SHALL mark every not-yet-deleted `antigravity` source deleted, and the upgrade SHALL delete each Antigravity account through `markAccountDeletedTx` and remove any remaining Antigravity credential row. Other sources and the usage history SHALL NOT change. Migration 55 is additive.

Files And Functions:
 - planned: internal/adapters/sqlite/antigravity_retirement.go#schemaV55
 - planned: internal/adapters/sqlite/antigravity_retirement.go#retireAntigravitySourcesTx
 - existing: internal/adapters/sqlite/store.go#Store.migrate

Tests:
internal/adapters/sqlite/antigravity_retirement_test.go
 - description: a schema-54 database with a live and a deleted Antigravity source and a Z.AI source upgrades to 55; both Antigravity sources are gone with their credentials; the Z.AI source and all usage events stay
   requirements: R1

## Implementation checklist

1. [x] Revert #59, `9a3ec581`, #58 and #56; restore the `openspec/` tree deleted by `9a3ec581`.
2. [x] Keep migrations 53 and 54; add migration 55 and the retirement step. (R1)
3. [x] Keep the published release notes of go-v1.17.0 – go-v1.19.0 as history.
4. [x] OpenSpec change `retire-antigravity-sources`, validated strictly.
5. [x] Gates, archive, release notes go-v1.20.0, merge and release; stop before prod.

## Open questions

None.

## Decision log

- 2026-11-23: forward release instead of deleting releases, because production already runs go-v1.19.0 at schema 54.
- The `openspec/` tree deleted by `9a3ec581` is restored: it came with the Antigravity changes, and `AGENTS.md` still requires OpenSpec.
- The race gate removal (#57) stays: it was a separate owner decision.
- The public Antigravity OAuth client values remain in Git history; nothing in the tree uses them.
