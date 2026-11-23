## 1. Scope and preservation

- [x] 1.1 Record administrator decisions, connected legacy contracts and local codex-relay behavior without reading credentials or changing services.
- [x] 1.2 Validate the technical plan; relocate legacy source/build files without losing or exposing dirty, untracked or ignored data.

## 2. Go runtime

- [x] 2.1 Implement the single-process Go composition root, clean architecture boundaries, SQLite persistence, migration/version checks and embedded assets pipeline.
- [x] 2.2 Implement authentication, accounts, OAuth, groups, API keys, policy and quota/cost accounting with import fixtures.
- [x] 2.3 Implement Codex Responses/HTTP/SSE/WebSocket continuity, cancellation, capacity limits, retry safety and settlement with regression coverage.
- [x] 2.4 Implement integrated Z.AI and OpenAI-compatible provider translation, capabilities, model catalogs and dashboard-editable persisted pricing without restart/rebuild.
- [x] 2.5 Implement reports, error-only diagnostic archives, retention, warmups/scheduled pings, reset credits, Codex image/file/voice workflows and remaining selected APIs.
- [x] 2.6 Integrate the selected dashboard functionality; exclude removed controls without weakening security.

The selected implementation is complete. Final verification passes full Go
tests and race detection, go vet, TypeScript/Vite build, and all 1211 frontend
tests in 150 files, including nine contracts against the rebuilt static binary.
Offline import/migration and delivery lifecycle checks pass. Three alternating
runs of JSON, paced SSE and cancellation measurements show lower Go CPU/RSS;
the remaining SSE latency disadvantage is recorded in context, not hidden.
Active-change and main Go-runtime specs validate; the design has 22 structurally
valid use cases. Nine unchanged legacy specs still fail global strict keyword
validation and were not rewritten outside this change. No production data or real
provider accounts were used; services, commits, publication and cutover are unchanged.

## 3. Delivery

- [x] 3.1 Verify data import, single-binary/systemd/optional Docker delivery, graceful shutdown and rollback constraints.
- [x] 3.2 Verify all selected behavior and known failure cases; measure CPU/RSS/latency under the same workload rather than assuming a Go performance gain.
- [x] 3.3 Synchronize final specs and archive only after implementation is complete. Do not publish or replace the running installation without a separate command.
