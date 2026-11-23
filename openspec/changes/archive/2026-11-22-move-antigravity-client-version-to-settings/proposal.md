## Why

The Antigravity client version in the Cloud Code User-Agent came only from the environment variable `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION`, so changing it meant editing the service unit and restarting. The Codex client version is a dashboard setting that applies at once; the owner asked for the same («Да, перенеси»).

## What Changes

- New runtime setting `antigravityClientVersion` (default `2.34.0`) in `GET`/`PUT /api/settings`, validated like the Codex client version.
- Every Cloud Code call reads the current value, so a saved version applies to the next call without restart.
- The Settings page gains an «Antigravity client» section.
- The environment variable `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION` is retired.

## Capabilities

### Modified Capabilities

- `go-runtime`: added requirement for the Antigravity client version setting.
- `frontend-architecture`: added requirement for the Settings section.

## Impact

- Migration 53 adds `runtime_settings.antigravity_client_version` (additive).
- `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION` is no longer read.
