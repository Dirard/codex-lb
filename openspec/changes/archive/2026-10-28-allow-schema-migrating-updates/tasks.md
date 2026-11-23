## 1. Implementation

- [x] 1.1 `compatibleUpdate` for installation; equal-schema check kept for rollback and recovery.
- [x] 1.2 `RestoreBackup` on the host; restore before restarting the previous version after a migrating candidate started.
- [x] 1.3 `rollbackNote` in status and the dashboard.

## 2. Verification

- [x] 2.1 Updater tests: migrating success, failure at startup and activation restores the backup first, same-schema failure does not restore, older schema rejected; host test restores the file and drops the WAL.
- [x] 2.2 Sync into `openspec/specs/runtime-updates/spec.md` and archive after merge.
