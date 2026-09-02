## Why

Direct WebSocket continuity remembers the last completed response for a Codex session and may inject it into a later full request. When OpenAI rejects that proxy-injected response with `previous_response_not_found`, the negative cache records the rejection but the continuity cache still injects the same response on every retry, permanently blocking the task until the proxy process restarts.

## What Changes

- Retire a proxy-injected response anchor from direct-WebSocket continuity when OpenAI explicitly returns `previous_response_not_found` for it.
- Do not alter client-supplied response chains or discard a newer continuity completion that raced with the rejected request.
- Allow the next full request to proceed without the rejected proxy anchor.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `sticky-session-operations`: Retire a rejected proxy-injected response anchor from live direct-WebSocket continuity.

## Impact

- Affected code: direct-WebSocket continuity error handling and in-memory continuity state.
- Affected behavior: retries after upstream `previous_response_not_found` for a proxy-injected anchor.
- No database migration, setting, dependency, dashboard, or transport retry is added.
