# Dashboard credits and credit-backed account eligibility

## Why

The dashboard omits the credit fields already expected by its account cards. New-session selection and the reservation gate also forbid purchased credits from covering an exhausted primary subscription window, despite Codex allowing credits after included limits.

## What Changes

- Supply known subscription balances/window metadata and persisted purchased-credit fields in dashboard account summaries; preserve unknown versus zero and unlimited.
- Allow usable purchased credits to cover exhausted included windows in selection and reservation without weakening account/key/group/model restrictions.
- Require credit evidence newer than a confirmed upstream quota refusal before it can override that block. Keep established-owner behavior unchanged.

## Capabilities

### Modified Capabilities
- `go-runtime`: dashboard account credits and purchased-credit routing.

## Impact

Go reporting, account quota rules and SQLite reservation checks; existing dashboard rendering. No schema migration, real credit redemption, service restart, publication or change to model prices.
