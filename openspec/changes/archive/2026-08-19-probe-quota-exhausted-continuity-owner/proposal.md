## Why

An account-wide quota block can prevent a proven hard-continuation owner from being retried after reconnect even though the upstream may still admit work for an already-running thread. Fresh requests should avoid exhausted accounts, but an existing owner-bound continuation must be allowed to ask its only valid owner after the normal cooldown instead of being rejected solely by local quota state.

## What Changes

- Let a required hard-continuation owner bypass standard quota status during owner-only selection while preserving the existing cooldown and every non-quota eligibility gate.
- Keep fresh requests, soft affinity, and unowned requests subject to normal quota filtering.
- Never move an owner-bound delta or account-scoped payload to another account; verified account-neutral full-resend failover remains unchanged.
- Treat a real upstream quota response as authoritative and reuse the existing cooldown before another owner probe.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `sticky-session-operations`: Permit bounded same-owner probing for quota-blocked hard continuations without weakening ownership or other eligibility constraints.

## Impact

- Proxy load-balancer required-owner selection
- Focused load-balancer and proxy regression tests
- No new timer, setting, persistence field, cross-account replay, or dependency
