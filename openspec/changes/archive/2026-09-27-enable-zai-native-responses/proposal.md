# Enable Z.AI native Responses

- Last edited with skill pack: `0.2.2`

## Why

Z.AI documents a native Responses endpoint, but the Go preset rejects it in the
dashboard and domain validation and forces Chat Completions in dispatch.

## What Changes

- Allow a Z.AI source to declare Responses or Chat Completions, preserving existing defaults.
- Use the existing native Responses adapter when Responses is enabled; apply GLM Chat customization only to Chat requests.
- Let administrators choose either protocol in the existing source form without rewriting their endpoint or credentials.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: Z.AI protocol selection and native Responses compatibility.

## Impact

Go source validation, provider capabilities, source create/edit form and their
regressions. Existing protocol fields and SQLite storage are sufficient; no new
dependencies, migrations, account changes or running-service updates are needed.
