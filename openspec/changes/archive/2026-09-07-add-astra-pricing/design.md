## Context

The existing shared `ModelPrice` table serves request persistence, cost reservations, settlement and display breakdowns. Request reports read persisted `cost_usd` rather than recalculating all prices on read. Folded aggregates can outlive their source logs because of retention.

## Goals / Non-Goals

Recognize Astra in existing cost paths and correct retained history. Do not add another pricing engine, change other models, meter subscription credits, replay requests, or retrospectively debit active key limits.

## Decisions

- Add one built-in subscription Codex Astra entry and a versioned `gpt-6-astra-*` alias. Use the existing fields for standard, Codex Fast/priority and Flex; leave this entry's long-context fields unset. External model sources already provide independent explicit costs (zero when unpriced) to log persistence and key settlement, so no new pricing engine or provenance flag is needed. Test that boundary with the same Astra model slug and usage above 272K.
- Add a forward data migration after the current Alembic head. Restrict correction to retained subscription Astra rows with known input and output usage (using the existing reasoning-output fallback and cached-token clamp). Explicit model-source prices remain untouched. Pin the migration's rates to this release so a future tariff edit does not change old migration behavior.
- Update stored costs and propagate only the per-row differences to materialized report summaries according to their existing fold watermarks, dimensional keys and inclusion rules. Preserve aggregates for pruned data; do not reset a watermark or rebuild all history from the retained subset. Work in bounded batches and keep each corrected log and its aggregate adjustments in the same transaction. Reapplying the migration yields zero additional difference.
- Keep previously settled key counters/reservations unchanged. They are enforcement records, not retrospective report totals. New admissions and settlements use the new shared price entry automatically.

## Risks / Trade-offs

- Retention may have deleted some raw Astra usage → correct only recoverable rows and preserve the remaining historical totals; do not invent token distributions.
- Folded account summaries and time summaries use different inclusion/deduplication rules → reuse their current semantic boundaries and test both folded and live-tail records, aliases, errors, source prices and pruned contributions.
- Public API and Codex have different Astra Fast/context pricing → keep the Codex tariff and source references explicit; configured external source prices remain authoritative.

## Migration Plan

The data migration runs during a future normal upgrade, before the updated process serves traffic. Test it on temporary databases, including repeated application and downgrade/re-upgrade behavior. Downgrade retains corrected report costs rather than reintroducing missing costs. No production mutation or deployment is authorized by this implementation task.
