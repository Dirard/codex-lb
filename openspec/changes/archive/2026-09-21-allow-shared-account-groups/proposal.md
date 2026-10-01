## Why

An administrator needs to add one account to several groups. Group creation and updates currently reject shared membership, and the database primary key enforces the same restriction.

## What Changes

- Permit the same account in multiple groups, retaining one membership per account/group pair.
- Preserve existing memberships, group-local limits, per-key counters, and live account-scope refresh.
- Remove only the cross-group membership rejection; retain account validation, name conflicts, write authorization, and in-use deletion protection.
- Migrate the primary key without data loss. Reject downgrade while shared memberships exist instead of silently dropping them.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `account-groups`: shared accounts with independent membership and key policy.

## Impact

Account-group model, validation, one forward migration, existing API/proxy/migration tests, and account-group specifications. The existing account picker and API payloads already support the new behavior. No deployment, release, API-key multi-group association, or shared budgets.
