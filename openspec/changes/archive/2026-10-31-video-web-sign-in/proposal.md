## Why

go-v1.8.0 adds Grok accounts by importing the CLI's `auth.json`. The owner rejected this: it needs a CLI on another machine and a token file on disk. Accounts must be added from the dashboard, as ChatGPT accounts are. A probe on 2026-10-31 confirmed that device authorization works without the CLI.

## What Changes

- **BREAKING** for the new function only: `POST /api/video/accounts/import` is removed.
- `POST /api/video/accounts/login`, `GET` and `DELETE /api/video/accounts/login/{id}` run a device sign-in.
- The Video page gets an Add account button and a dialog with xAI's link and code. An account that lost its session gets a Sign in again button.
- The official CLI's public client id becomes a constant in the xAI adapter, because xAI publishes no way to register a client.

## Capabilities

### Modified Capabilities

- `go-runtime`: Grok accounts are added through a dashboard sign-in.

## Impact

- Schema: none (44). Accounts imported with go-v1.8.0 keep working.
- Operations: a sign-in in progress lives in the worker's memory; during an update it must be started again.
