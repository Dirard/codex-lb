## Context

See [proposal.md](proposal.md) for motivation. API keys already contain account scopes and per-key limit rows. Admissions reserve against those rows and completion settles the same row IDs and reset boundaries. WebSocket response creation already reloads key policy before each request; HTTP authentication uses an invalidatable key cache.

## Goals / Non-Goals

- Reuse the current account-selection and limit-accounting mechanisms, including explicit upstream-quota continuity rules.
- Keep group policy authoritative without changing per-key counters into group counters.
- No project contexts, new administrator identities, shared group budgets, new settings switches, report redesign, or deployments.

## Decisions

1. Add `AccountGroup`, group account membership, group limit templates, and nullable `ApiKey.group_id`. Account membership is exclusive across groups; the group-to-key relation is optional. Keep account membership separate from the large accounts table so its schema and data need not be rewritten. Protect in-use groups from deletion.
2. Resolve grouped account scope from its group's current membership when constructing authenticated key data and dashboard key responses. An empty group remains explicitly scoped. Existing direct assignments are still used for ungrouped keys. Clearing a key's group retains the last effective membership instead of restoring broad access accidentally.
3. Group templates own limit configuration; existing per-key limit rows remain the enforcement ledger. Synchronize affected key rows transactionally using existing rule matching and usage initialization, preserving IDs, counters, reset boundaries and outstanding reservations for unchanged rule identities. No independent group spending counter or alternative settlement path is introduced. Direct limit edits on grouped keys are rejected, so materialized enforcement values cannot become independently editable policy. Group and key mutations share transaction/locking conventions; never run concurrent work on one session.
4. Reuse the existing key-cache invalidation mechanisms after group mutation. Open WebSocket requests already refresh key data. Add an explicit grouped-key scope check where legacy continuation ownership can otherwise bypass ordinary account scope; reject a removed required owner before dispatch rather than silently replaying. Requests admitted before a change may finish with their existing reservation.
5. Dashboard API: `GET/POST /api/account-groups/`, `PUT/DELETE /api/account-groups/{id}`. Group representations contain `id`, `name`, `accountIds`, `limits` (existing limit-create shape), and `keyCount`; create/update bodies contain `name`, `accountIds`, and `limits`. API key request/response schemas gain nullable `groupId`. Unknown groups and conflicting group-managed overrides are validation errors; membership conflicts and deleting an in-use group return 409.
6. Mount group management alongside API keys in Settings and reuse account selection and limit-editor components. A key's optional group selector replaces direct account/limit editing while grouped; its other controls and usage indicators remain unchanged. Use the existing query cache and invalidate affected group/key queries after mutations.

## Risks / Trade-offs

- Concurrent limit changes and request settlements can overwrite usage if counters are reconstructed carelessly. Preserve existing rows for matching identities and update only configuration; verify an outstanding reservation through a group edit.
- Editing a rule's identity has the existing individual-key current-window initialization semantics, not a retrospective reconstruction of every possible historic policy. Reuse that contract rather than adding a second accounting system.
- Legacy continuation selection intentionally has scope exceptions. Apply a hard group boundary only for grouped keys and retain existing ungrouped behavior, including continued use at reported zero quota until upstream rejects it.
- Group mutations touch linked key configuration in one transaction. This is appropriate for the single-admin deployment; no asynchronous propagation job or eventual-consistency queue is needed.

## Migration Plan

Add a single Alembic revision after the current head. Existing keys receive null group IDs; their credentials, account assignments, counters, and logs are untouched. Verify upgrade, downgrade, re-upgrade, and data retention on temporary test databases. Schema downgrade must not silently widen access for grouped keys; materialize their current account scopes before removing group metadata. Do not change the running database or container as part of implementation.
