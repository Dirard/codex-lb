## MODIFIED Requirements

### Requirement: Optional administrator-managed account groups

The dashboard SHALL allow its administrator to list, create, update, and delete named account groups using the existing dashboard authentication and write-access protections. A group SHALL contain account membership and limit rules of the same types, units, windows, and model filters supported for individual API keys. An account MAY belong to multiple groups. Each account/group pair MUST be unique; duplicate account IDs in one save request MUST be normalized to a single membership. Unknown account IDs and invalid limits MUST be rejected without partially modifying a group. Membership in another group MUST NOT cause a conflict. Group-name conflicts MUST return 409. Group use SHALL be optional without requiring a new server setting; existing data and keys SHALL remain ungrouped after the original group-introduction migration.

#### Scenario: Existing installation after upgrade
- **WHEN** the original group-introduction migration upgrades an installation without groups
- **THEN** existing keys retain their accounts, limits, consumption, and authentication behavior

#### Scenario: Create and update a group
- **WHEN** an authorized administrator saves a valid group name, account list, and limit rules
- **THEN** the group is persisted and listed with those settings and its number of linked keys

#### Scenario: Share an account between groups
- **WHEN** an administrator creates or updates a group with an account already in another group
- **THEN** the operation succeeds and both groups retain that account
- **AND** repeated account IDs inside the request create only one membership in the saved group

#### Scenario: Reject conflicting membership
- **WHEN** a database write attempts to insert the same account/group pair twice
- **THEN** the duplicate pair is rejected without preventing membership in a different group

#### Scenario: Preserve other groups when membership is removed
- **WHEN** an administrator removes a shared account from one group or deletes that group without linked keys
- **THEN** other groups retain the account and their settings

#### Scenario: Group management respects dashboard access
- **WHEN** a caller without dashboard write access attempts to mutate a group
- **THEN** the operation is rejected using the existing dashboard authentication and permission rules

## ADDED Requirements

### Requirement: Shared accounts preserve group-local key policies

Sharing an account MUST NOT merge group limits, key consumption, or account scopes. An API key SHALL continue to use only its own group's current membership and limit rules. Updating one group's membership or limits MUST NOT change another group's membership, limits, or key usage. Shared accounts SHALL retain their existing provider-quota accounting rather than gaining extra quota from group membership.

#### Scenario: Shared account and independent keys
- **WHEN** two groups share an account and a key in each group sends requests
- **THEN** both keys may use that account under their respective policies
- **AND** changing the first group's limits or emptying its account list does not change the second key's policy or consumption

### Requirement: Shared membership migration preserves data

The migration enabling shared accounts SHALL preserve every existing membership, group setting, API key and usage ledger while allowing unique account/group pairs. Referential integrity and cascading deletion of memberships SHALL remain enforced. Downgrade to exclusive membership MUST reject accounts that still belong to multiple groups before changing their data or schema; after extra memberships are explicitly removed, downgrade and subsequent upgrade SHALL preserve the remaining data.

#### Scenario: Upgrade existing membership
- **WHEN** an installation with exclusive group memberships is upgraded
- **THEN** the memberships and key ledgers are unchanged and the same account can be added to another group
- **AND** duplicate pairs remain rejected by the database

#### Scenario: Prevent lossy downgrade
- **WHEN** downgrade is requested while an account belongs to two groups
- **THEN** it fails with an actionable error and neither membership is removed
