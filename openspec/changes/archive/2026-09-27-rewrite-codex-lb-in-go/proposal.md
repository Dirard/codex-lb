## Why

The administrator wants an independently developed codex-lb, delivered as one Go binary, with clearer ownership of sessions and resources. Rewriting is not itself a fix for upstream timeouts or protocol bugs: the new implementation must preserve the selected behavior and verify the previously failing scenarios before replacing the running service.

## What Changes

- Keep this Git repository; relocate the existing implementation and its uncommitted changes into `legacy/`, then develop the Go implementation at the root with clean architecture.
- Preserve the selected account, group, key, quota, report, security, Codex transport, image-tool, voice, scheduled-ping, and reset-credit functionality before production cutover.
- Support ChatGPT subscription accounts and external Chat Completions/Responses providers, including Z.AI, with an integrated Go Responses-to-Chat-Completions adapter.
- Remove upstream install telemetry, multi-replica coordination, the quota phase planner, guest dashboard access, proxy pools, and the public Images API. Use SQLite, a binary/systemd installation and optionally one Docker image; do not port PostgreSQL, Helm/Kubernetes, Nix or distroless variants.
- Retain content archives only for errors, separately from bounded continuation state and content-free usage statistics for every request.
- Preserve the live installation until selected functionality, data migration, failure recovery, and resource measurements are verified. Deployment requires a separate user command.

## Capabilities

### New Capabilities

- `go-runtime`: single-binary architecture, selected feature boundary, local runtime and safe migration contracts.

### Modified Capabilities

None in the currently running Python implementation. Existing OpenSpec capabilities remain the legacy baseline; this change does not silently rewrite their runtime claims.

## Impact

Repository layout, build/release process, runtime implementation, persistence, provider adapters and regression tests. Existing source code, credentials, data and uncommitted work must not be lost. No provider traffic, database mutation, secret export, registry publication or server replacement is authorized merely by preparing this change.
