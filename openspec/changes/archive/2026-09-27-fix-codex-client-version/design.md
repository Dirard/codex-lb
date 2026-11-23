# Fix the Codex client version used by Luna probes

- Last edited with skill pack: `0.2.2`

## Title and scope

Make the Luna probe use the supported Codex identity already used by the installed CLI, and let the administrator change it in Settings without updating the service. No model substitution, accounting repair or unrelated quota changes.

## Planning anchor

`POST /api/accounts/{id}/probe` still reports `probe_failed` after changing the default model. The archived request names Luna; upstream detail classifies it as unsupported for a ChatGPT account. On the same credentials, read-only catalog GETs omit GPT-6 for client 0.144.0 and include it for 0.156.0. Amend the active `openspec/specs/go-runtime/{spec.md,context.md}`; reuse the archived probe/weekly-quota repair unchanged. The lifecycle directory is not a solution specification.

## Connected groups or observed existing logic

- Entry: the Accounts UI sends no model; the authenticated probe route calls `WarmupService`, which pins one request using Luna and preserves uncertain accounting.
- Transport: `provider.New` supplies the version consumed by Responses HTTP/WS, compact and other subscription operations. `NewModelCatalogClient` separately hard-codes the same stale version for discovery. `cmd/codex-lb/runtime.go` uses both defaults.
- Persistence and presentation: add `codex_client_version` to the existing settings row through migration 33 and expose `codexClientVersion` in the existing authenticated settings contract. A small Settings form saves only this field and the expected revision. Unknown probe reservations remain untouched.
- Validation: extend existing catalog, HTTP/WS bridge and authenticated probe tests using synthetic local upstreams. Do not run live generations.
- Specs: amend the existing Go runtime contract and context; archived previous fixes remain historical. Reverse documentation is unnecessary because the focused call path is established.

## Use cases

### 1. Discover and request Luna with one client identity

configured adapters --resolve the Codex client version--> consistent subscription identity --fetch catalog or dispatch the selected model--> upstream response with unchanged settlement

Implementation Logic:

Use a domain setting/default and pure format validator. Wire a narrow SQLite version reader into catalog/provider adapters; read one version per operation instead of adding an in-memory settings cache. Preserve explicit constructor versions when no resolver is provided; failed reads stop dispatch. Reuse one provider header helper for Responses, compact/files and realtime. Do not introduce a runtime version downloader. Keep catalog bootstrap minimum-version metadata unchanged: it describes older models, not this runtime's outgoing identity.

The settings save validates before its existing CAS transaction. After a successful version change, signal the catalog's owned poller through a coalescing channel; do not start an unowned goroutine or wait for network I/O in the settings handler. The existing polling lifecycle handles refresh/cancellation. HTTP requests and new WS handshakes use the new value. Existing connection-bound WS sessions deliberately retain their handshake identity and continuation state until normal closure; no forced disconnect or replay.

Tests:

- description: A catalog request with no override sends version 0.156.0 in both query and User-Agent and preserves a returned Luna model.
- description: The authenticated probe fake upstream rejects stale Version/User-Agent headers with the observed model-not-supported response; the repaired route makes exactly one Luna call and retains all existing known/unknown usage assertions.
- description: Existing HTTP and WebSocket bridge tests check default and explicit client-version headers without changing payload semantics.
- description: Settings API validation, stale revision rejection, migration and restart preserve the version; changing it in a live fixture updates catalog/HTTP and new WS headers without reconstructing adapters.
- description: The dashboard saves a valid version with expected revision, blocks invalid input and explains the active-connection exception.

### 2. Update the authorized local instance

verified candidate --back up the installed binary and consistent data--> recoverable installation --replace binary and restart the existing unit--> ready service with refreshed model catalog

Logic Details:

Use the established local unit and atomic binary replacement. Preserve encryption key, account/key counts, quota data and unresolved reservations. Check readiness and the normal catalog refresh, without invoking a generation. The free catalog comparison confirms version gating, not end-to-end generation acceptance; retain that limitation in the handoff. Migration 33 is additive but the older runtime rejects a newer schema, so rollback needs the consistent database backup as well as the saved binary.

## Implementation checklist

1. [x] Add the delta contract, persisted validated version setting and live adapter resolution.
2. [x] Add the Settings form and boundary regressions, run focused/full Go and frontend checks and build.
3. [x] Sync the Go runtime spec/context, back up and update the authorized local instance, verify readiness/catalog and archive the verified change.

## Open questions

None blocking. Live inference verification is deliberately left to an operator-triggered probe.

## Decision log

- Keep Luna exactly as requested. A model fallback would hide the client-compatibility defect and could change costs.
- Use the observed installed version as the initial value rather than inventing a future version. The user explicitly requested changing it without a service update; choose the manual admin setting instead of automatic unverified upgrades.
- Version gating is proven for catalog discovery; the provider request fix is checked locally. Catalog access alone does not prove successful live generation.
- No changes to generic upstream-error disclosure or unknown-usage settlement.
- Skip automated specification review loops for this narrow compatibility repair; validate the use cases and OpenSpec contract locally.
- A first full Go run overlapped Vite's replacement of embedded assets and failed to find generated files. Run the final build and full tests sequentially. Whole-repository strict OpenSpec validation reports pre-existing placeholder Purpose sections in 22 legacy specs; validate the changed capability strictly without expanding this fix.
- Verification: the sequential full Go suite, focused race tests (catalog/provider/SQLite/HTTP settings/probe and owned refresh poller), go vet, 151 settings UI tests, typecheck, targeted ESLint, build and 10 real-binary integration tests passed. Browser smoke on an empty isolated instance rejected an invalid value, saved 0.157.0 and retained it after reload; no usage reservations were created. The changed Go runtime spec and change pass strict OpenSpec validation.
- Deployment: local port 2456 runs `go-local-e2e-codex-client-settings`, schema 33 and saved client version 0.156.0. Readiness and SQLite quick_check passed; both accounts, four keys, two pending reservations and the unchanged encryption key were retained. The normal catalog refresh now includes GPT-6 Astra/Sol/Luna; weekly-only quota remains secondary/10080 minutes. Rollback binary/database/key are in `/home/dirard/.local/share/codex-lb-go-local/rollback-pre-client-version-settings.AFhZcN`.
- All 60 main specs pass ordinary validation; strict warnings in untouched legacy specs remain outside scope. No paid probe was dispatched. The isolated browser test reused the loopback hostname and replaced the dashboard session cookie, requiring another admin login; credentials themselves were not changed. For future browser smoke use a separate hostname/context as well as a separate database. The temporary instance was stopped and its data moved to trash; its browser tabs were closed.
