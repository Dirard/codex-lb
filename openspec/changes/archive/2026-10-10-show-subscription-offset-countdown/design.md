## Context

The Accounts list, dashboard cards and dashboard list already share `SubscriptionRemaining`, including one minute timer and localized date formatting. The administrator needs the recorded subscription end and a second reference two weeks later.

## Goals / Non-Goals

- Show both remaining durations and both exact dates everywhere the shared component is used.
- Preserve missing-date and elapsed-period semantics.
- Do not change stored dates, account eligibility, quota handling, backend contracts, timers or dependencies.

## Decisions

Derive the second timestamp as the recorded end plus 14 × 24 hours during rendering. Reuse the existing clock and formatters. Separate the labels with `/` and mark the second `+14d`; the tooltip explains that this is a reference, not a confirmed extension. Evaluate expiry independently for each timestamp.

Permit the dashboard list's subscription line to wrap within its existing column so the additional countdown remains visible.

## Risks / Trade-offs

Saved authorization metadata may lag an actual renewal; keep that warning. The offset is elapsed time, independent of daylight-saving changes. No data migration or backend rollout is required.

## Verification

Extend the shared component and all three consumer tests for both countdowns. Check minute updates, independent expiry, missing/invalid dates, year rollover, translations and date-display preferences. Run frontend tests and production build.
