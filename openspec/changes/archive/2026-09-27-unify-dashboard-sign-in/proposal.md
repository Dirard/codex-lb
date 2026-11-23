# Unified dashboard sign-in

- Last edited with skill pack: `0.2.2`

## Why

API-key holders must sign in on the same screen as administrators instead of using a separate key-report login page.

## What Changes

- Offer administrator password and API-key report access on the existing sign-in screen.
- Show only the scoped read-only report after key authentication; preserve password/TOTP and server-side authorization.
- Redirect the former `/key-reports` entry to the shared entry point and return key logout/revocation to the shared form.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: shared dashboard entry for administrator and API-key authentication.

## Impact

Frontend routing, authentication UI, report-session lifecycle, translations and tests. No database/API changes, dependencies, deployment, or real-provider calls.
