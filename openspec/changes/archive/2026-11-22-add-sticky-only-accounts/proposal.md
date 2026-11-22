## Why

The owner wants some accounts to keep balancing but never move a started conversation elsewhere: «балансировка должна быть, но только в стики режиме, т.е. если чат начавшийся на одном аккаунте прервался на исчерпании лимитов - он не перескочит автоматически на другой». Today an explicit quota refusal of a conversation's owner replays the conversation on another account, in the same request and on later turns.

## What Changes

- Every account gets a sticky-only option (`stickyOnly`, stored in `accounts.sticky_only`).
- A conversation owned by a sticky-only account stays on it: the owner's quota refusal reaches the client, nothing is replayed elsewhere and the conversation is not marked as quota-refused. A conversation already marked is sent to the owner again.
- New conversations are still balanced onto and away from such accounts as before, and the refusal still counts as an account outcome.
- `PATCH /api/accounts/{id}` accepts `stickyOnly`; the dashboard shows the option on the account sheet and in the model-source dialog.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for sticky-only accounts.

## Impact

- Migration 52 adds `accounts.sticky_only` (additive).
- Admin API and dashboard gain one option; other accounts behave as before.
