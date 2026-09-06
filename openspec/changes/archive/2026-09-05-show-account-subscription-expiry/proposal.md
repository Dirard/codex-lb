## Why

Operators cannot see how much time remains in each account's subscription period. The saved ID-token already contains an upstream subscription end timestamp, so the account views can display it without an additional upstream request.

## What Changes

- Expose nullable `subscriptionActiveUntil` in account summaries used by Accounts and the dashboard.
- Show the remaining subscription time beside each account, with the recorded end date and its refresh limitation in a tooltip.
- Distinguish an unknown date and an elapsed recorded period without changing account eligibility.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-identity`: expose the upstream subscription period timestamp independently of token expiry and quota reset times.
- `frontend-architecture`: display subscription time in account rows and dashboard cards/list rows.

## Impact

ID-token metadata parsing, the shared account-summary mapper/schema, frontend account presentation and focused tests. No database migration, new dependency, upstream polling, routing change, or deployment is required.
