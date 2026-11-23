## Why

The full-runtime lifecycle check exposed the intentional local keyless ingress principal. Realtime currently uses that generic authenticator too, so disabling general key authentication lets trusted local callers create/attach calls under one shared internal key. Original Realtime requires an actual API key regardless of the general keyless setting.

## What Changes

- Require the existing strict Bearer authenticator for Realtime call creation and sideband attachment.
- Preserve ordinary local/CIDR keyless Responses behavior.
- Verify the complete server ingress, not only a standalone operation handler, including cross-key call isolation.

## Capabilities

### Modified Capabilities
- `go-runtime`: Realtime authentication and key-scoped call ownership.

## Impact

Two HTTP entrypoints and focused full-ingress tests. No storage, provider, deployment or global auth-policy change.
