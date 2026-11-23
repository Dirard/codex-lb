## Why

The trailing seven-day weekly-demand calculation filters to the request window before computing each account's `lag`, so the first in-window increase has no baseline and is silently omitted. This understates the dashboard's add-capacity recommendation exactly when demand crossed the window boundary.

## What Changes

- Include exactly one latest pre-window baseline row per account and normalized usage window when computing positive used-percent deltas.
- Continue aggregating only current rows at or after the window start, preserving the until bound, reset handling (negative deltas remain excluded), `NULL`-window primary normalization, and `(recorded_at, id)` ordering.
- Add repository and dashboard-path regression coverage for demand that crosses the seven-day boundary.

## Capabilities

### New Capabilities

- None.

### Modified Capabilities

- `frontend-architecture`: weekly runway add-capacity guidance MUST account for the demand increase from the trailing-window baseline to the first sample inside the window.

## Impact

- `app/modules/usage/repository.py`
- Dashboard weekly-pace repository and overview/projections tests
- No API schema, dependency, migration, or frontend change
