## Why

The owner decided that adding Google Antigravity accounts was a bad idea and asked to roll everything back to the version before Antigravity through a forward release («откати все изменения и релиз до версии "до антигравити"», «можно через форвард релиз»). Production already runs go-v1.19.0 with schema 54 and one signed-in Antigravity source.

## What Changes

- The Antigravity code (sign-in, Cloud Code translation, Chrome transport, settings and dashboard) is removed; the runtime behaves as go-v1.16.0 again.
- Migrations 53 and 54 stay (append-only); their column is no longer read.
- Migration 55 retires every Antigravity source on upgrade: the source and its account are deleted like an operator deletion, including the Google refresh token; usage history stays.

## Capabilities

### Modified Capabilities

- `go-runtime`: added requirement for retiring Antigravity sources on upgrade.

## Impact

- Schema 55 (additive).
- Antigravity sources disappear from the dashboard and routing after the upgrade.
