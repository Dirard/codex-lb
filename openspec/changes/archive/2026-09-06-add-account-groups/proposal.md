## Why

An administrator currently repeats account assignments and identical per-key limit rules across several API keys. Optional named account groups should keep those two settings synchronized without introducing projects, separate servers, or shared spending counters.

## What Changes

- Add dashboard-managed account groups containing a name, account membership, and the existing kinds of limit rules.
- Allow an API key to opt into one group; keys without a group retain their existing behavior and API compatibility.
- Apply group membership and limits to every linked key automatically. Each key retains its own consumption, reset windows, and usage reservations.
- Preserve usage when a group's limit amount changes and enforce the current account boundary for subsequent HTTP and WebSocket requests.
- Keep existing reports, administrator authentication, model policies, and server settings unchanged.

## Capabilities

### New Capabilities

- `account-groups`: Optional group management, dynamic account membership, per-key limit inheritance, and dashboard controls.

### Modified Capabilities

- `api-keys`: Optional group association during creation and updates, with unchanged ungrouped-key contracts.

## Impact

Database models and an additive migration; account-group dashboard API; API-key creation, updates, limit configuration and authentication data; existing routing-scope checks; account and API-key dashboard controls and relevant tests. No new dependency, environment variable, report, release, or deployment is required.
