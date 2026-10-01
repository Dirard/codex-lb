# Nullable Codex catalog defaults

- Last edited with skill pack: `0.2.2`

## Title and scope

Let Codex load an authorized external-source catalog without invalid empty enum strings.

## Planning anchor

The installed CLI rejects the LB response supplied through documented `model_catalog_json` with `unknown variant ''`, expecting low/medium/high. The captured GLM metadata has default reasoning max, verbosity support false, and an empty default verbosity. Original `app/modules/model_sources/catalog.py` uses nullable defaults. Amend the Go runtime contract.

## Connected groups or observed existing logic

- `domain.CatalogModel` intentionally represents absent strings as Go zero values; persistence and provider routing use this model independently of Codex JSON.
- `httpapi.codexEntry` overwrites raw fields with those strings. This is the shared canonical and versioned-alias serialization boundary; OpenAI-compatible metadata has its own existing shape.
- `models_test.go` already exercises authenticated published catalogs and forwarding-override privacy. Extend that route coverage and test both valid and absent/invalid defaults.
- The isolated E2E runner can retrieve the real scoped catalog, pass it to CLI 0.156.0 and execute GLM tools/resume without touching the working installation.

The failing path is established by the real client and focused source comparison; separate reverse documentation or broad mapping is unnecessary for this boundary-only repair.

## Use cases

### 1. Publish nullable optional fields

scoped catalog model --validate known optional enums--> declared or absent defaults --encode nullable fields--> client-readable Codex entry

Implementation Logic:
Keep domain strings and stored configuration unchanged. At `codexEntry`, encode empty optional defaults/version as null. Retain the existing recognized reasoning vocabulary; accept only low/medium/high for the verbosity default. Unknown defaults become absent, never a guessed low/medium value. Preserve canonical/alias authentication and metadata filtering.

Tests:
- description: Request canonical, slash and client-version catalog routes for source models without optional defaults.
  expected outcome: Fields are explicitly null, metadata remains scoped and forwarding overrides remain private.
- description: Serialize valid and malformed optional enum defaults.
  expected outcome: Valid values survive; absent or malformed enum defaults are null.
- description: Load the actual isolated LB catalog with Codex CLI and run GLM answer/tool/resume.
  expected outcome: No JSON catalog parse failure or missing-model-metadata warning; usage settles normally.

## Implementation checklist

1. [x] Reproduce the real parse failure and compare the original nullable contract.
2. [x] Repair the shared JSON boundary and route regression.
3. [x] Run tests and live isolated CLI verification, then synchronize and archive.

## Open questions

Custom-provider automatic discovery remains a separate client behavior; loading this documented explicit catalog must work regardless. No working-service replacement is authorized.

## Decision log

- Keep nullable serialization at the HTTP boundary rather than changing domain/persistence types or inventing provider defaults. The change needs no new setting or dependency.
- 2026-09-27: CLI 0.156.0 loaded the actual rebuilt LB catalog through `model_catalog_json` and completed GLM answer/tool/resume without the missing-model-metadata warning. Four real upstream requests settled normally (21,402 tokens, 11,511 microdollars), with no uncertain reservation. Explicit WS support still produces the expected client HTTP-fallback notice for this HTTP source. Targeted route/default tests and race checks pass.
