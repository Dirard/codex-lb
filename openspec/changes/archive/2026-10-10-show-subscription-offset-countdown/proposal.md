## Why

The administrator wants two subscription reference countdowns beside each account: the recorded end and that same timestamp plus two weeks.

## What Changes

- Show the current countdown and a clearly labeled +14-day countdown separated by a slash on every existing account subscription surface.
- Include both exact timestamps in the existing localized tooltip; handle expiry and missing metadata without claiming that access was extended.

## Capabilities

### Modified Capabilities
- `frontend-architecture`: account subscription date presentation.

## Impact

Shared React display component, three locale files and existing component/surface tests. No backend, persistence, routing, billing, new timers or dependencies. Release, commit and service updates are not included.
