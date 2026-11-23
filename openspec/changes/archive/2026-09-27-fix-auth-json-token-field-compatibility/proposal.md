# Accept standard Codex auth.json token fields

- Last edited with skill pack: `0.2.2`

## Why

The shared Go import parser accepts only camelCase token names, while Codex and the runtime's own Codex export use snake_case. A valid document therefore produces `Invalid auth.json payload` through either file selection or paste.

## What Changes

- Accept the standard snake_case token fields and optional account ID alongside existing camelCase fields.
- Retain required token validation, reject conflicting alias values and preserve the existing identity/encryption path.
- Cover the real multipart import route and export/import round trip using synthetic credentials.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: auth.json token naming compatibility in account import.

## Impact

Shared application parser and regression tests. No frontend workaround, schema migration, provider calls or deployment.
