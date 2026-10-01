# Account groups context

See [the specification](spec.md) for requirements.

## Shared membership

One account can appear in several groups. Membership is identified by the `(account_id, group_id)` pair; the existing group editor's account picker needs no change. Saving or deleting one group changes only its own memberships.

For example, groups Research and Development can both contain account A. A key in Research uses Research's accounts and limits; a key in Development uses Development's. Removing A from Research does not remove it from Development. The account's upstream quota remains shared, not multiplied by the number of groups. This feature does not put one API key in several groups or merge group budgets.

## Migration and rollback

The forward migration changes only the membership primary key, preserving the existing rows, foreign keys and index. It does not rewrite API keys, limits, reservations, or reports. Downgrade refuses to restore exclusive membership if any account still belongs to multiple groups. An administrator must explicitly resolve those memberships first; the migration never chooses a surviving group or silently deletes access.

The existing name-conflict, unknown-account, write-permission, and in-use group-deletion checks still apply. Live scope refresh and per-key limit accounting use the existing mechanisms.
