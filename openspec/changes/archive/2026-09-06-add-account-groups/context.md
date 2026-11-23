# Optional account groups

The single administrator wants named account pools with centrally editable limits, not multiple tenants or server instances. Reports already filter by accounts and remain unchanged. Models, reasoning, transport policy, administrator login, and server routing settings are outside group policy.

Groups are opt-in per key. Their limits are identical allowances for each key: with a 1000-token rule, key A consuming 300 leaves A with 700 and unused key B with 1000. It is not a combined 1000-token group wallet. Existing `credits` rules are presentation values, not usage-decremented enforcement counters; grouping does not introduce credit accounting.

The existing per-key limit ledger remains responsible for reservations and settlement. Updating a group's amount must not re-create counters. Empty membership must fail closed; removing a required owner must not cause an unapproved upstream replay. Grouped-key scope is stronger than the historical ungrouped continuation exception.

Existing installations migrate with no groups and no key associations. No running container or production database is modified during development. Group membership and configuration are not exposed through a second administrator context or a global current-project switch.

The normative contract is in [the group specification](specs/account-groups/spec.md) and [the key association specification](specs/api-keys/spec.md).
