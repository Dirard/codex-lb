## Why

Browser E2E shows the group account picker says `All accounts` when its membership is empty, although the server correctly treats that group as allowing no accounts. The empty-selection menu action also uses the misleading label.

## What Changes

- Label empty group membership `No accounts selected` in the picker and its empty-selection menu item.
- Preserve the existing `All accounts` meaning for direct API-key assignments and all-account automations.
- Keep serialized membership, routing authorization and limits unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: make the group editor's empty-selection description match its existing closed scope.

## Impact

Reuse the account picker's placeholder in its empty menu item, supply a group-specific translation, and update focused UI tests. No backend, schema, dependency or working-service deployment changes.
