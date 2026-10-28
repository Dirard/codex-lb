# Usage rollups for fast analytics

- Last edited with skill pack: `0.2.2`

## Title and scope

First slice of the dashboard redesign: make every analytics read independent of the number of stored events. Hour-bucket rollups, maintained by a background folder and update/delete triggers, serve the dashboard overview, admin and key reports, key usage and trends, request-log options and totals, and conversations, with unchanged API shapes. The detailed technical design is [`openspec/changes/usage-rollups/design.md`](../openspec/changes/usage-rollups/design.md); this spec plans the slice and records its requirements. Page redesign (overview by groups, key page, reports for key users, keys/groups/accounts management) follows in later slices on top of these rollups.

## Planning anchor

- Entrypoints: `internal/adapters/sqlite/reports_dashboard.go#QueryDashboardTraffic`, `reports_aggregate.go#QueryReports` (`readRawEvents`, `readHourly`), `reports_logs.go#RequestLogOptions` / `#ListRequestLogs`, `reports_key.go`, `reports_conversations.go`, `usage_reports.go#insertUsageTx`, `reports_retention.go#FoldAndPruneRequestLogs`, `model_pricing_reprice.go`, `account_delete_history.go`, `cmd/codex-lb/runtime.go#startBackground`.
- Changed assumptions: analytics no longer read raw events beyond partial-hour edges and the unfolded tail; daily medians are histogram approximations; costs are integer-microdollar sums.
- Measured baseline on a production copy (`.local/review/pages-latency.md`): overview 5.2 s (7d) / 14.6 s (30d), reports 2.9–8.6 s, request-log options 7.4–15.8 s, key report 0.8–1.3 s.
- Non-local: SQLite adapter, application background service, runtime wiring, OpenSpec `go-runtime`.
- Impacted specs in `specs/`: `storage-growth-backups.md` (retention folding into `legacy_*` stays as is; rollups mirror only retained events). Action: none.
- Impacted OpenSpec capabilities: `go-runtime` — action `amend` through change `openspec/changes/usage-rollups/`.

## Connected groups or observed existing logic

1. Persistence: `usage_events` rows are inserted once at settlement and later changed only by repricing (cost), account history deletion (anonymize/discard) and retention (fold into `legacy_*`, then delete); reconciliation may insert events with an old `requested_at`.
2. Reports: one ordered pass over raw events plus `legacy_hourly_usage`, `legacy_conversation_hourly`, `legacy_hourly_errors`; predicates in `reports_query.go#rawReportWhere`; equivalence and reference tests exist.
3. Overlap: during a seamless update an older worker inserts and mutates events without knowing about rollups; only the active worker runs background work.

## Use cases

### 1. Fold events into rollups

new events --background folder advances the rowid cursor in atomic batches--> hour rollups --update/delete triggers journal changes of folded rows--> journal --folder applies journal--> consistent rollups

Requirements:

  R1:
Migration 42 SHALL create only empty rollup tables, the journal, the update/delete triggers and the state row, and SHALL be additive.

  R2:
The folder SHALL run only on the active worker, fold events after its cursor and drain the journal in batches through the batch writer, each batch atomic with its cursor and idempotent on retry, holding the write lock for tens of milliseconds and yielding between batches.

  R3:
Updates and deletions of folded events SHALL be journaled by triggers in the writing transaction for every worker version; inserts SHALL fire nothing.

  R4:
An anchor mismatch, a changed import watermark, a negative measure or a verifier mismatch SHALL rebuild the rollups while reads use the raw path.

### 2. Read analytics from rollups

analytics request --plan hours and raw edges--> sources --combine rollups, journal, tail and edges in one snapshot--> exact result --tail or journal over bound or layout mismatch--> raw evaluation

Requirements:

  R5:
The overview, reports, key reports, key usage and trends, request-log options and totals and conversations SHALL read rollups for whole hours and raw events only for partial hours, the unfolded tail and the hours containing local midnights of fractional-offset zones.

  R6:
Exact metrics SHALL equal the raw evaluation; daily medians SHALL be within 1/64 relative plus rounding; costs SHALL be integer-microdollar sums in both paths.

  R7:
When the tail or journal exceeds 20 000 rows, or the layout, status, cursor anchor or import watermark does not match, reads SHALL use the raw path; so SHALL request-log search and conversation filters and conversation reads while removals of conversation events are pending.

### 3. Verify and measure

hourly verifier --compare rollup and raw paths--> [match, mismatch] --log, count and rebuild on a mismatch--> consistent rollups

Requirements:

  R8:
An hourly verifier SHALL compare both paths for a one-day dashboard and a seven-day report and rebuild on a mismatch.

  R9:
On a production copy the seven-day overview, a thirty-day report and request-log options SHALL each answer in under 300 ms.

## Implementation checklist

1. [ ] OpenSpec change `usage-rollups` with design; strict validation.
2. [x] Histogram codec; raw path uses histogram medians and integer costs (R6).
3. [x] Migration 42, triggers and state; floor tests (R1, R3).
4. [x] Folder, rebuild, verifier and service wiring; fold, crash, late-event, rowid-reuse and two-store tests (R2, R4; R8 table check, its read-path comparisons land with item 5).
5. [x] Window planner, sources and rollup readers for every endpoint; randomized equivalence tests (R5–R7).
6. [ ] Benchmarks and production-copy latency run (R9).
7. [ ] Gates, PR, CI, release.

## Open questions

None blocking; Q1–Q7 of the design are answered during implementation or recorded as follow-ups.

## Decision log

- 2026-10-28: Owner decisions D1–D7 are recorded in the design: approximate histogram medians, integer-microdollar costs, exact fractional zones through raw split hours, current-membership group attribution, at most one fold interval of first/last staleness, rebuild on verifier mismatch, long-range legacy rollups later.
- 2026-10-28: Capture by a cursor-advancing folder plus update/delete triggers instead of updating rollups in settlement: an older worker inserts during an update overlap without rollup code, and triggers in the schema run in every version's transactions.
- 2026-10-28: No rollup switch setting: the raw fallback and the verifier cover failures without a new operator setting.
- 2026-10-28: Conversation reads use the raw path while removals of conversation events are pending, instead of showing first and last up to one fold interval stale (stricter than D5): the fallback lasts about one fold interval after retention or account deletion and keeps every reader exact.
