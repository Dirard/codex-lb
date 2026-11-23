## Why

Codex CLI 0.156.0 rejects the LB's authenticated GLM catalog before inference: `default_verbosity` is an empty string instead of the original implementation's nullable value. Missing reasoning/version defaults have the same string-zero-value boundary problem.

## What Changes

- Publish JSON null for absent optional Codex default verbosity, default reasoning and minimum-client-version values.
- Preserve valid declared defaults; map unsupported default enum values to null instead of publishing invalid empty enum strings.
- Verify the actual catalog with Codex's documented `model_catalog_json` input and real GLM tool/resume traffic.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: client-readable optional Codex model catalog fields without fabricated defaults.

## Impact

Codex catalog HTTP serialization and route regressions only. No provider capability, pricing, key scope, persistence, dependency or working-service deployment changes. This does not claim that custom-provider automatic model discovery is supported by every Codex client.
