# Key reports and account group picker

- Last edited with skill pack: `0.2.2`

## Why

API-key holders need a read-only report page without administrator access. The
administrator also needs to assign one account to several groups in one operation
from its account card instead of editing every group separately.

## What Changes

- Add `/key-reports` with API-key sign-in and reports restricted server-side to that key.
- Reuse report calculations and display components, exposing no account identities, credentials or cross-key selectors.
- Add a multi-group picker to account details with one atomic membership update.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: isolated key-report self-service and account-card group membership editing.

## Impact

Bearer authentication, self-service report routes, dashboard routing, report UI,
account-card UI, group membership persistence and security/regression tests.
No database schema migration, provider request, deployment or new dependency.
