## ADDED Requirements

### Requirement: Optional API key group association

API key creation and update SHALL accept an optional nullable `groupId`. A non-null group ID SHALL resolve to an existing account group or be rejected. API key responses SHALL include the current `groupId`, effective assigned accounts, and effective per-key limits with that key's consumption. When `groupId` is omitted on creation or remains null, the existing direct account-assignment and limit contracts SHALL apply unchanged. Omitting `groupId` on update MUST NOT change an existing association. A non-null group SHALL supersede the unscoped interpretation of omitted `assignedAccountIds` and SHALL apply the account-group contract instead. An update MUST NOT allow a grouped key to override group-managed accounts or limits unless the same update removes the association. Key regeneration SHALL preserve the group association and consumption.

#### Scenario: Create a key using a group
- **WHEN** an administrator creates a key with a valid `groupId` and no direct account or limit settings
- **THEN** the new key inherits the group's current account scope and limit configuration
- **AND** it receives its own usage counters

#### Scenario: Legacy create request
- **WHEN** an administrator creates a key using the existing request shape without `groupId`
- **THEN** its direct account assignment, limits, and returned plain-key behavior are unchanged

#### Scenario: Unrelated edit preserves inheritance
- **WHEN** an administrator changes only a grouped key's name or model restrictions
- **THEN** its group association and inherited account and limit settings remain active

#### Scenario: Reject group-managed override
- **WHEN** an administrator submits direct account assignments or limit rules for a key that remains grouped
- **THEN** the request is rejected without altering its group, consumption, or other settings
