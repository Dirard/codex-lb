## Why

API-key report login shows traffic totals but not the key's current configured budget or its group's per-key usage. The administrator key page already has the desired limit widgets; key holders need a read-only, group-scoped view of those same counters.

## What Changes

- Show current personal limit usage on the key-report page when limits exist.
- For a grouped key, show a separate compact list of all non-deleted keys in its current group: name, status and personal limit usage, using the existing administrator-page presentation.
- Show aggregate subscription-quota usage across the group's accounts, weighted by capacity and separated by window, subject to existing upstream-visibility policy.
- Expose only safe limit metadata from the existing ledger. Historical traffic remains scoped to the authenticated key; no administrator access, secrets, accounts or peer request history are exposed.

## Capabilities

### Modified Capabilities
- `go-runtime`: read-only personal and same-group limit information in API-key reports.

## Impact

The existing `/v1/usage/reports` JSON gains additive fields. SQLite performs a scoped read; there is no migration or second accounting system. Existing React key-limit widgets are shared with report rendering. No server update, release or real-provider request is included.
