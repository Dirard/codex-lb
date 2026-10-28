## Why

The updater required the candidate to have exactly the running database schema. Every release with a migration (go-v1.1.0 adds schemas 34–40) therefore could not be applied from the dashboard; operators had to replace the binary by hand on the server.

## What Changes

- Accept candidates with an equal or newer schema; keep refusing older schemas. Rollback and boot recovery still require an equal schema.
- Restore the existing pre-switch backup before restarting the previous version when a migrating candidate fails after it started.
- Explain in the status and dashboard why a previous version with an older schema is not offered as rollback.

## Capabilities

### Modified Capabilities

- `runtime-updates`: schema compatibility for updates and failure recovery.

## Impact

`internal/application/runtime_updates.go`, the `RuntimeUpdateHost` port and `cmd/codex-lb` host (`RestoreBackup`), status JSON (`rollbackNote`) and the runtime updates card. No schema change.
