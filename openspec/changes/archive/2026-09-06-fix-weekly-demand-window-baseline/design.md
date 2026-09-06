## Context

`positive_used_percent_deltas_by_account` currently applies `recorded_at >= since` before its window function. The first in-window row therefore has a `NULL` predecessor, and a positive jump from just before `since` is omitted. The weekly-runway recommendation consumes that aggregate as quota-week demand.

## Goals / Non-Goals

**Goals:**

- Preserve all existing window/reset semantics while adding one baseline row per account and normalized window.
- Keep the query bounded to the requested rows plus that single baseline row per group.
- Prove the repository aggregate and dashboard consumer behavior.

**Non-Goals:**

- Changing runway fields, attribution, or frontend rendering.
- Backfilling historical demand.

## Decisions

- Build the window-function input from rows through `until`, selecting all rows at or after `since` plus the newest older row per account/normalized window. This supplies `lag` without scanning all history.
- Compute `lag` over `(recorded_at, id)` within account and normalized window, then aggregate positive deltas only where the current row is at or after `since`. This preserves reset behavior and prevents the pre-window baseline itself from contributing.
- Keep normalized-window matching in SQL so `NULL` primary rows and requested primary windows remain equivalent.

## Risks / Trade-offs

- More complex SQL than the current range predicate → one focused repository regression covering boundary, reset, until, and normalized-window behavior.
- Database-specific window functions → the repository already relies on window functions for `lag`, so no new database capability is introduced.
