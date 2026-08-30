## Why

The existing hard-continuation quota bypass is applied during initial account selection but is lost when an active HTTP bridge reconnects. A reconnect can therefore reject the thread from local `RATE_LIMITED` or `QUOTA_EXCEEDED` state without sending its normal request to ChatGPT, then hold an in-flight bridge creation until later retries fail as local overload.

## What Changes

- Preserve required continuity-owner provenance when a hard HTTP bridge reconnect is bound to its current account.
- Let only that existing owner-bound thread ignore local standard-quota status; fresh, unowned, and soft-affinity requests keep normal quota filtering.
- Send no synthetic probe: the existing thread's normal request is the only upstream attempt.
- Surface a real ChatGPT API rejection through the existing error and cooldown path instead of converting it into an indefinite bridge-creation wait.
- Add regression coverage at the HTTP bridge reconnect path.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `sticky-session-operations`: Extend the bounded same-owner quota bypass to hard HTTP bridge reconnects and explicitly keep new and soft requests excluded.

## Impact

- HTTP bridge reconnect account selection
- Sticky-session continuity requirements
- Focused proxy/HTTP bridge regression tests
- No setting, schema, migration, dependency, or cross-account replay change
