# account-groups Specification

## Purpose
Define optional named account groups that centrally configure account access and identical independently accounted limits for their API keys.

## Requirements

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

### Requirement: Live group account scope

A grouped key SHALL use the current accounts of its group for subsequent request admissions, including subsequent response-creation requests on an open WebSocket. The group's scope MUST remain enabled when its account list is empty. Account selection, retries, failover, and continuation-owner reuse MUST NOT grant access to an account outside that group. A membership change SHALL NOT interrupt an already admitted response. A continuation whose required owner was removed SHALL fail closed without silently replaying the request on another account. Ungrouped keys SHALL retain their existing scope and continuity behavior. Existing model-source assignments and other key policies SHALL remain unchanged.

#### Scenario: Add an account once for all linked keys
- **WHEN** an administrator adds an account to a group with two linked keys
- **THEN** subsequent requests authenticated by either key can select that account without editing the keys or restarting the server

#### Scenario: Remove the last account
- **WHEN** the last account is removed from a group
- **THEN** the group's keys cannot select any subscription account, including accounts belonging to other groups

#### Scenario: Remove an open connection's owner
- **WHEN** a response is in flight on an account removed from its key's group
- **THEN** that response is allowed to finish
- **AND** a subsequent continuation requiring that removed account is rejected before upstream dispatch

### Requirement: Identical limits with independent key accounting

Group limit rules SHALL apply to every linked key as identical per-key limits, not as a shared group budget. Each key SHALL retain independent consumption, reset boundaries, and usage reservations using the existing limit accounting semantics. Group-limit changes SHALL become effective for subsequent request admissions without regenerating keys. Updating only a limit's amount MUST preserve existing consumption, its active reset boundary, and outstanding reservations. Adding or changing a rule's type, window, or model filter SHALL use existing current-window usage initialization semantics rather than silently resetting usage. A group's limits SHALL be authoritative while a key belongs to it; individual account or limit overrides MUST be rejected. Explicit per-key usage reset SHALL remain a separate administrator action and SHALL NOT reset other keys.

#### Scenario: Independent consumption
- **WHEN** a group configures a weekly limit of 1000 tokens and one linked key consumes 300 tokens
- **THEN** that key has 700 tokens remaining and another unused key still has 1000 tokens remaining

#### Scenario: Reduce a limit below consumption
- **WHEN** an administrator reduces a group's limit below a linked key's current consumption
- **THEN** subsequent requests by that key are denied by the existing limit error contract
- **AND** usage is not reset and another key below its own limit remains usable

#### Scenario: Update during an outstanding request
- **WHEN** an administrator changes a group's limit amount while a linked key has an outstanding usage reservation
- **THEN** the reservation is settled exactly once against that key's existing counter
- **AND** the other keys' counters are unchanged

### Requirement: Safe group lifecycle and dashboard controls

The existing dashboard SHALL provide group management and optional group selection when creating or editing a key, without introducing a new core navigation item. Grouped-key account and limit controls SHALL identify that their values are managed by the group; other key settings and existing consumption displays SHALL remain available. Removing a key's group association SHALL retain its last effective account scope and limit configuration as individual settings unless the same update explicitly supplies replacements. A group with linked keys MUST NOT be deleted and SHALL return 409. Existing reports and their filters SHALL remain unchanged.

#### Scenario: Leave a group safely
- **WHEN** an administrator clears a key's group without supplying replacement accounts or limits
- **THEN** the key retains the group's last effective account scope and limits as individual settings
- **AND** its existing consumption is preserved

#### Scenario: Delete an in-use group
- **WHEN** an administrator attempts to delete a group with linked keys
- **THEN** the operation returns 409 and the keys keep their existing access and limits

#### Scenario: Edit a grouped key
- **WHEN** an administrator opens a grouped key's edit dialog
- **THEN** it shows the selected group, inherited accounts and limits, and the key's own consumption
- **AND** unrelated model and transport settings remain individually editable

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
