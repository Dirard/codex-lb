## Why

The first real WebRTC creation attempt returned HTTP 400. Official Codex selects native Realtime protocols with `OpenAI-Alpha: quicksilver=v1/v2`, but Go drops that header. Original codex-lb also supports `/v1/realtime?call_id=...` for v1/v2 sidebands; Go only implements v3 path-based live sidebands.

## What Changes

- Forward bounded, explicitly typed OpenAI Alpha/Beta Realtime negotiation headers on call creation and sidebands, filtering Responses-only Beta tokens as original does.
- Restore the legacy query-based Realtime sideband with strict key-scoped call ownership, explicit protocol selection and duplicate/mixed call-ID rejection before upgrade.
- Preserve existing v3 paths, private error handling, frame bounds and ownership rules.
- Repeat isolated live WebRTC media and sideband verification after matching the native client contract.

## Capabilities

### Modified Capabilities
- `go-runtime`: Realtime protocol negotiation and legacy sideband compatibility.

## Impact

Existing operation request DTOs, HTTP handlers, provider headers/endpoint selection and tests. No generic header passthrough, schema, dependency, automatic retry or deployment.
