## Why

The Luna administrative probe still fails because the runtime identifies as Codex 0.144.0. A read-only comparison against the same imported account returned no GPT-6 models for 0.144.0 and returned Astra, Sol and Luna for the locally installed Codex version 0.156.0.

## What Changes

- Use one current default Codex client version for subscription catalog discovery and provider requests, preserving explicit constructor overrides.
- Add a persisted administrator-editable Codex client version that applies to new HTTP requests and WebSocket connections without rebuilding or restarting, and requests a free catalog refresh.
- Check version-sensitive behavior at the catalog, administrative probe and HTTP/WebSocket adapter boundaries.
- Keep the requested model, one-attempt probe behavior, accounting, authentication and weekly-quota repair unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: compatible default client identity for subscription catalog and inference.

## Impact

Go ChatGPT catalog/provider adapters, the existing settings API and dashboard, one additive SQLite settings column and boundary regressions. No external-provider protocol changes, dependency or automatic version downloader. Rebuild and replace the authorized local service with a rollback backup; verify readiness and the normal free catalog refresh without generating paid traffic.
