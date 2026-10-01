## ADDED Requirements

### Requirement: Empty group membership is visibly distinct from unrestricted accounts

The group creation and edit account picker SHALL describe an empty membership as no accounts selected, including the menu action that clears the selected accounts. Clearing membership SHALL continue to submit an empty account ID array, not expand it to every account. Direct API-key assignment and all-account automation pickers SHALL retain their existing unrestricted-selection label and behavior.

#### Scenario: Create or clear an empty group
- **WHEN** the administrator opens a new group or clears its selected accounts
- **THEN** the trigger and checked empty-selection menu item describe no accounts selected
- **AND** submission preserves the empty membership and the backend's closed-scope behavior

#### Scenario: An ungrouped key uses every permitted account
- **WHEN** the direct API-key account selector is empty
- **THEN** it continues to display All accounts with its existing assignment semantics
