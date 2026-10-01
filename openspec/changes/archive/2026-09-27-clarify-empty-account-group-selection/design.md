# Describe empty group membership accurately

- Last edited with skill pack: `0.2.2`

## Title and scope

Correct the group picker label without changing membership or authorization.

## Planning anchor

Live UI E2E created an empty group while the picker displayed All accounts; the resulting group correctly showed zero accounts. Amend the Go runtime UI contract.

## Connected groups or observed existing logic

`AccountGroupForm` supplies `AccountMultiSelect` with an empty array. The shared picker already supports a placeholder but hardcodes its empty menu option. API-key create/edit use the default; automation explicitly uses All accounts because its empty array means all accounts. Focused inspection covers every caller; broader mapping and reverse documentation are unnecessary for this local copy correction.

## Use cases

### 1. Render the current selection meaning

picker props --derive empty selection label--> matching trigger and menu copy --clear selection--> unchanged empty array

Implementation Logic:
Use the existing placeholder-derived label for the empty menu item. Groups pass a translated No accounts selected value. Do not add state, effects, data fetching, settings or routing conditions. Rename the local clear callback to reflect its actual operation.

Tests:
- description: Create a group, select an account, clear it and submit.
  expected outcome: Both empty states say No accounts selected and the API receives accountIds as an empty array.
- description: Render the direct API-key picker and automation form.
  expected outcome: All accounts remains unchanged; existing picker and automation tests pass.

## Implementation checklist

1. [x] Record the observed mismatch and unchanged scope contract.
2. [x] Correct copy and translations; add focused UI regression.
3. [x] Validate tests/build and the rebuilt isolated browser; sync and archive.

## Open questions

None. The full-product verification goal and the prohibition on updating the working service remain unchanged.

## Decision log

- Reuse the existing placeholder; no new component option or state is necessary. Local copy correction does not require a separate complicated-plan review loop.
- 2026-09-27: 21 focused picker/group/automation/localization tests, TypeScript and Vite build pass. Rebuilt isolated browser shows No accounts selected both on the group trigger and the checked empty menu action. Main local service was not restarted or replaced.
