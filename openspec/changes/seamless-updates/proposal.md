## Why

A managed update waits up to ten minutes for a moment with no active request, then stops the server, takes a backup and starts the new version. On a busy installation — long reasoning streams, Codex WebSocket sessions, warm-ups — such a moment may never come, the update is deferred, and when it does happen every client sees a short outage and reconnects. The launcher that runs this logic is also frozen at the version of the manually installed bootstrap binary, so improvements to the update mechanism never reach an installation that updates only from the dashboard.

## What Changes

- The launcher keeps the listening socket and the database lock for the life of the service and lends both to its workers as inherited descriptors, together with a lifeline pipe and a per-process instance id.
- A seamless switch starts the new version beside the running one (online backup first), lets it migrate and verify the database, makes it accept connections on the shared socket, then retires the previous worker: it stops accepting and stops background work, closes idle client WebSockets so they reconnect, finishes every request it accepted (up to 30 minutes) and exits. No connection is refused and no active generation is cancelled by the switch.
- While two workers run, each counts the other's active and queued generations, per-account creates/streams and per-key streams against its own limits, and lowers its Go memory limit by the other's resident memory. Only the active worker runs background loops, metrics, cleanup and reprice resumption.
- Both workers write one SQLite database safely: write transactions begin `IMMEDIATE`, reservations record their owner instance and startup marks only reservations of dead processes as interrupted, and the database records a schema floor so older handoff-aware code can run on additively migrated schemas (also enabling rollback without a backup restore).
- A token refresh that lost a race against another process returns the token that process stored.
- A bootstrap launcher re-executes the installed newer release as the launcher (with a crash guard), so update logic follows the installed version from the next start.
- The classic idle-gated switch remains for older workers and for releases with non-additive migrations.
- The dashboard says whether switching is seamless and labels the new phases.

## Capabilities

### Modified Capabilities

- `runtime-updates`: shared-socket handoff, retirement, shared admission, single background owner, widened rollback compatibility, launcher delegation, administrator disclosure.
- `go-runtime`: multi-process write transactions, owner-scoped interrupted reservations, newer additive schema opening, refresh race recovery.

## Impact

- Code: `internal/domain` (descriptor fields, switch decision), `internal/application` (runtime update flow, worker load, proxy peer load and retirement, token refresh), `internal/adapters/sqlite` (DSN, migration 41, reservations, floor, reprice resumption), `internal/adapters/httpapi` (WebSocket retirement), `cmd/codex-lb` (launcher descriptors and relay, worker handoff endpoints, delegation), `web/src/features/runtime` and locales.
- Schema: migration 41 adds `usage_reservations.owner_instance` and `schema_compatibility`; additive. go-v1.1.x refuses schema 41, so returning to it needs the pre-update backup as before.
- API: `update-info` gains optional `handoff` and `schemaFloor`; the update status gains `seamless`; phases `preparing` and `switching`. No new settings or environment variables for operators.
- Operations: installations whose bootstrap binary is go-v1.1.1 or older keep the classic switch until the bootstrap binary is replaced once by a release containing this change.
