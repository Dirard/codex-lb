## ADDED Requirements

### Requirement: Forks are shown in their parent's group

Every administrator read that groups or filters keys by group SHALL place a fork in its parent's group, although the fork stores no group of its own: groups summary and its key lists, overview group rows and attention keys, the keys list with its group and "No group" filters, key usage reports and their export, the directory, and every key count per group. A fork SHALL NOT be counted or listed among the ungrouped keys while its parent has a group. Group key lists SHALL mark a fork with its parent's id and name. Resetting a group's usage SHALL also reset the limits of its keys' forks.

On a key holder's own pages and reports, a fork SHALL show its parent's group and that group's account pool, governed by the parent's usage sections. Its holder SHALL see only the fork among the group's keys, and other holders in the group SHALL NOT see it.

#### Scenario: A fork of a grouped key on the groups page
- **WHEN** a key in group "Team" has a fork
- **THEN** the groups summary lists the fork in "Team" with its parent's name, and the ungrouped row does not contain it

#### Scenario: Filtering the keys list by group
- **WHEN** the administrator filters the keys list by "Team"
- **THEN** the fork is listed with group "Team", and the "No group" filter does not list it

#### Scenario: A fork holder's report
- **WHEN** the fork's holder opens their key page
- **THEN** it shows group "Team" and lists only the fork among the group's keys
