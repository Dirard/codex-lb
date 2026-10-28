# Incremental usage rollups for codex-lb (schema 42): design

Status: proposal, 2026-10-28. Branch `feat/usage-rollups` (same as `main` at `d007370c`), DB schema 41. This was a read-only analysis and no code was changed. Row counts and latencies marked "est." are estimates. They are derived from the production measurements in `pages-latency.md`: about 3 µs per row per pass over `usage_events` with modernc. For rollup rows that are covered by their PK or index, I assume 1.5–3 µs per row.

## 0. Decisions on one page

| # | Decision |
|---|---|
| D-capture | **Inserts** are captured by a background folder that advances a cursor over `usage_events.rowid`. **UPDATE/DELETE of rows already folded** is captured by three triggers that write images of the changed row into `usage_rollup_journal`. Triggers live in the schema, so they run inside the transactions of every worker version, including go-v1.2.0 after a rollback. There is no INSERT trigger, so settlement cost is unchanged. |
| D-read | Every analytics read is one read transaction combining: rollups for whole hour buckets, plus the signed journal rows for those hours, plus the raw tail (`rowid > cursor`) for those hours, plus raw "edges" (partial hours). This is exact in every snapshot, not only once the folder has caught up. |
| D-scope | Rollups mirror the **retained** `usage_events`. Retention keeps folding into the `legacy_*` tables exactly as today, and the delete trigger subtracts the row from the rollups. Queries read the `legacy_*` tables exactly as today. Result: migration 42 is additive, old code's reports stay correct, and repricing or account deletion of folded history needs no rollup logic. |
| D-median | Daily median TTFT, queue and TPS come from fixed log-linear histograms with 32 sub-buckets per octave. The error bound is ≤ 1/64 relative, and values < 32 are exact. The **raw fallback path uses the same histograms**, so the rollup path and the raw path return bit-identical output. |
| D-cost | Cost is summed as integer microdollars. Legacy REAL costs and raw integer costs are combined the same way in both paths. Compared with today's float summation, the only possible difference is ±0.0001 on half-way rounding ties. |
| D-conv | Distinct conversations are exact. They come from a `(hour, conversation, dims)` table holding request counts, so rows can be subtracted when events are deleted. |
| D-tz | Buckets are hours. Viewers in whole-hour zones (including DST, for example Europe/Prague) are served exactly with no raw reads. Half-hour and 45-minute zones are also exact: the hour that contains each local midnight is read raw. |
| D-fallback | The current raw implementation stays. It is used when the tail exceeds 20,000 rows, the journal exceeds 20,000 rows, the layout or status does not match, or the kill switch `CODEX_LB_USAGE_ROLLUPS=off` is set. |
| D-backfill | Migration 42 is DDL only and takes milliseconds. The folder backfills the 742 k existing events in batches of 1–2 k rows, holding the write lock ≤ ~40 ms per batch (est.), about 1 minute wall time (est.). The raw path serves queries until the folder has caught up. |

## 1. Metrics and how each maps to rollups

### 1.1 Event predicates the rollups must reproduce exactly

| Name | SQL today | Used by |
|---|---|---|
| `in_reports` | `request_kind NOT IN ('warmup','limit_warmup') AND (legacy_request_id IS NULL OR requested_at >= coalesce(legacy_import_state.hourly_folded_through, MIN))` (`reports_query.go:64 rawReportWhere`) | reports, dashboard, key reports, key usage and trends, top error, report conversation counts |
| `in_conversations` | `conversation_id IS NOT NULL AND trim(conversation_id)!='' AND request_kind NOT IN (warmup kinds) AND legacy_deleted=0`. There is **no** watermark exclusion here (`reports_conversations.go:12`). | conversation list and details |
| `in_logs` | all rows | request-log options and totals |
| status class | `success`/`completed` → ok; `cancelled` → cancelled; anything else → error | everywhere |
| account/key dimension | `NULL`→`\x1f`; a value starting with `\x1f` → `\x1f`+value; anything else, including `''`, stays raw (`rawReportDimension`, `reports_aggregate.go:352`) | all rollup tables. Filter values are encoded the same way. The legacy tables keep `toLegacyDimension`. |
| UA / effort dimension | `NULL`→`\x1f`, `''` stays `''`, `\x1f`-escape otherwise. Display maps `\x1f` and `''` to `Missing User-Agent`. The `Missing User-Agent` filter matches `\x1f` only (that is the `IS NULL` semantics). | cube, slice, conversation, log, dim |
| conversation key | `trim(x)` removes spaces only, the same as `strings.Trim(x," ")` in `readRawEvents`. Pre-existing inconsistency: retention uses `strings.TrimSpace` for `legacy_conversation_hourly`. This is unchanged. | conversation table |

These predicates are written once as SQL fragments (`rollupInReportsSQL`, `rollupConversationSQL`) and shared by four readers: the raw path, the folder's tail query, the journal reader and the query-time tail reader. That way there is one definition only.

### 1.2 Endpoint metrics

**`GET /api/dashboard/overview`** (`QueryDashboardTraffic` plus `application/reports_dashboard.go`). Windows are `[now-W, now)` with millisecond precision. Buckets are UTC-aligned: 24×1 h, 28×6 h or 30×1 d.

| Metric | Mapping |
|---|---|
| summary.cost.totalUsd, metrics.requests, tokens (in+out), cachedInputTokens (clamped), errorCount, cancelledCount, errorRate, reasoning | exact: `usage_rollup_total_hour` + edges/tail/journal + `legacy_hourly_usage` (as today) |
| metrics.conversations (distinct), conversationRequests | exact: `usage_rollup_conversation_hour` (in_reports=1) ∪ `legacy_conversation_hourly`; `conversation_requests` measure + legacy sum |
| metrics.topError | exact: `usage_rollup_error_hour` + `legacy_hourly_errors`, `client_disconnected` excluded at query time |
| comparison.canCompare, previous.requests/tokens/costUsd | exact: total_hour over the previous window; EXISTS before previousStart |
| trends.requests/tokens/cost/errorRate/conversations per bucket | exact: total_hour rows grouped into buckets; conversations from the conversation table per bucket (this replaces the N+1 at `reports_dashboard.go:84-97`) |
| accounts[], windows, summary windows, lastSyncAt, depletion | not from usage_events (quota tables); unchanged |
| accounts[].requestUsage | `usage_totals`; unchanged |
| weeklyCreditPace (PositiveQuotaDeltas, history) | quota history; unchanged |
| weeklyCreditPace.topApiKeys (`WeeklyPaceTopKeys`, 2 h) | stays raw in PR 1 (2 h ≈ 3 k rows, 10–20 ms); can later move to the cube by (key, model) |
| additionalQuotas | always `[]` (dead); unchanged |

**`GET /api/reports`**, **`GET /api/key-reports/reports`**, **`GET /v1/usage/reports`** (`QueryReports`; windows are local midnights, previous window has the same number of days, up to 731 days).

| Metric | Mapping |
|---|---|
| summary.totalCostUsd/Input/Output/Reasoning/ReasoningUsageKnownRequests/CachedTokens/Requests/Cancelled/Errors | exact. Unfiltered: total_hour. Filtered: cube_hour. Plus legacy as today. |
| summary.totalConversations, activeAccounts | exact: conversation table / account slices or cube (distinct with net count > 0) ∪ legacy presence |
| summary.avgCostPerDay, avgRequestsPerDay | exact (computed in the application layer) |
| comparison.canCompare, previous.totalCostUsd/TotalTokens/TotalRequests | exact |
| daily[].requests/inputTokens/outputTokens/reasoningTokens (nullable)/cachedInputTokens/costUsd/cancelledCount/errorCount | exact |
| daily[].conversations, activeAccounts | exact |
| daily[].medianTtftMs, medianQueueMs, medianTps | **approximate**, see §1.3 |
| byModel (cost, requests, percentage) | exact: model slices (unfiltered) or cube (filtered) + legacy |
| byUseragent (+ `Historical (unattributed)`) | exact: useragent slices or cube; the legacy row is unchanged |
| byAccount (+ aliases) | exact: account slices or cube. Dead in the UI; the owner may drop it (§7). |
| key report limits/group | not usage_events; unchanged |

**`GET /api/request-logs/options`**: `accountIds`, `modelOptions` (model, effort), `apiKeys` (id, name, prefix), `statuses`, plus pending reservations. Exact. With no filter it comes from `usage_rollup_dim`. With a timeframe or filters it comes from `usage_rollup_log_hour`. Search and conversation-id filters stay raw (§5).

**`GET /api/request-logs`**: the page (keyset/offset) stays raw. `total` becomes exact from state/log cube (§5). `conversation` summary stays raw, because it uses `conversation_id=?` with an index.

**`GET /api/api-keys/{id}/usage-7d`**: totalRequests, totalTokens, cachedInputTokens, totalCostUsd (6 dp), accountCosts[] (account, email, cost, isDeleted). Exact from the cube using the key index, plus raw edges through `idx_usage_events_key_time`, plus legacy.

**`GET /api/api-keys/{id}/trends`**: 169 hourly cost and token points. Exact from key slices (hour grain). The first and last partial hours are read raw.

**`GET /api/accounts/{id}/trends`** and `/api/dashboard/projections`: from `account_quota_history`, not usage_events. Unchanged (20–30 ms).

**`GET /api/conversations`**: total, hasMore, and per conversation: first/last, requestCount, representative account/key/model, remainingAccountCount, remainingModelCount, totalTokens, cachedInputTokens (unclamped), totalCostUsd. Exact from the conversation table. Caveat: after a boundary event is deleted, first/last and the representative row can lag by at most one folder interval (≈2 s), until the folder recomputes them.

**`GET /api/conversations/{id}`**: start, latest, accountCount, totalElapsedTime, dominantUserAgentGroup, modelStats by (model, effort). Exact, same caveat. Equal-cost ties in modelStats get a defined effort tiebreak in both paths; today that order is undefined.

### 1.3 Medians → fixed histograms

- Codec (`rollup_hist.go`), HDR log-linear with `p=5`. For `v < 32` the index is `v` (exact). Otherwise `e=bits.Len64(v)-1`, `m=v>>(e-5)` and `index=(e-4)<<5 + (m-32)`. The bucket is `[m<<(e-5), (m+1)<<(e-5))`, and the representative value is `lower + (width-1)/2`.
- Samples: TTFT when `latency_first_token_ms>0`; queue when `queue_latency_ms>=0`; TPS when `ttft>0 && output>0 && total>ttft`, stored as `floor(tps*100)`. These are the guards in today's code.
- Storage: three BLOB columns on `usage_rollup_total_hour` and `usage_rollup_cube_hour`. Each is a sorted list of (uvarint index delta, uvarint count). A histogram is mergeable and **subtractable**, which the journal needs.
- Median: `n=Σcount`; take the representative values at ranks `(n+1)/2` and `(n+2)/2`, average them, then `roundReport(…,2)`. That is the rank rule of `referenceReportMedian`.
- **Error bound**: `|m̂ − m| ≤ m/64 + 0.005` for ms metrics, and `≤ m/64 + 0.015` tokens/s for TPS. Values below 32 ms (for example a queue latency of 0) are exact. Precision is recorded in `usage_rollup_state.layout`; a different `p` needs a rebuild.
- Rejected: t-digest and other sketches. They cannot be subtracted, and their merge is order-dependent. Exact per-value maps do not compress.

### 1.4 Distinct conversations

`usage_rollup_conversation_hour` stores, for each `(hour, trimmed conversation, account, key, model, effort, UA, deleted, in_reports)`, the request count, the sums and `first_at`/`last_at`/`last_request_id`.

- Distinct count over any union of hours: the number of conversation ids whose net count is > 0. This is exact, and stays exact under deletes because counts are signed.
- Window and per-bucket counts for the dashboard take one scan of the window's rows. This replaces the 24–30 statements of today's N+1.
- Rejected: HyperLogLog (approximate and cannot be subtracted), and a per-conversation first/last summary (cannot answer windowed or per-day counts).

### 1.5 Costs

Rollups store `cost_microdollars` (int64). Every scope computes `kbn(legacy cost_usd rows in query order) ⊕ rawMicro/1e6`, with one term added last. The raw path is changed to the same formula (`reportPass.window/days/models/accounts`, `aggregateReportWindow`, `reportWindowTotals`, `KeyUsage7Day`). After that, both paths agree bit for bit. Compared with today's float summation (`kbnSum` over `cost_microdollars/1e6`), `roundReport(x,4)` can differ by 0.0001 on half-way ties, roughly 0.1–0.3 % of rows. byUserAgent already sums integers and does not change.

### 1.6 Windows and time zones

`planRollupWindow(start,end,splits)` (in `rollup_window.go`) splits `[start,end)` into two parts:
- `[ceilHour(start), floorHour(end))` minus the "split hours": served by rollups + tail + journal;
- raw millisecond ranges for the edges and the split hours, read from every row regardless of rowid.

How this plays out:
- **Whole-hour zones, including DST** (Prague days of 23 or 25 hours): report bounds fall on UTC hours, so there are no raw edges at all. Dashboard windows (`now-7d`) have at most four partial-hour edges, ≤ 6 k raw rows.
- **Fractional zones** (Kolkata +5:30, Kathmandu +5:45, Lord Howe with its 30-minute DST, Adelaide, Newfoundland, Chatham): each local midnight inside the retained period falls in an hour that is read raw, and its rollup rows are skipped. Results are exact. The cost is one hour of raw rows per day boundary: about 1.5 k rows today, about 46 k rows for a 30-day report, about 0.1–0.2 s (est.). Pruned history (the legacy rows) keeps today's behaviour: each legacy hour belongs to the day containing the hour's start.
- Option if fractional-zone viewers matter at 300-key scale: add a 15-minute total table. That makes unfiltered daily sums and medians split-free. Distinct conversations and accounts can then be split exactly using `first_at < b` / `last_at ≥ b` presence.

## 2. Schema: migration 42 (`rollup_schema.go`, `schemaV42`, `additiveMigrations[42]=true`)

The migration is DDL on empty tables plus one state row. It runs in milliseconds. After it, `SchemaFloor()` stays 40 (41 and 42 are both additive). The SQL must be a frozen literal, not generated: it is applied once per database.

### 2.1 State, journal, triggers

```sql
CREATE TABLE usage_rollup_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 layout INTEGER NOT NULL,                 -- 1 = this design (hist p=5); readers/folders need equality
 status TEXT NOT NULL CHECK(status IN ('ready','rebuilding')),
 folded_rowid INTEGER NOT NULL,           -- every usage_events row with rowid<=this is in rollups(+journal)
 folded_rows INTEGER NOT NULL,            -- live rows with rowid<=folded_rowid (folder +, delete trigger -)
 anchor_request_id TEXT,                  -- request_id at folded_rowid when the cursor was set
 import_watermark INTEGER,                -- hourly_folded_through seen by the folder
 updated_at INTEGER NOT NULL);
INSERT INTO usage_rollup_state VALUES(1,1,'ready',0,0,NULL,NULL,0);

CREATE TABLE usage_rollup_journal (
 seq INTEGER PRIMARY KEY,
 sign INTEGER NOT NULL CHECK(sign IN (-1,0,1)),   -- 0: cost-only delta in cost_microdollars
 event_rowid INTEGER NOT NULL, request_id TEXT NOT NULL, requested_at INTEGER NOT NULL,
 api_key_id TEXT, account_id TEXT, model TEXT NOT NULL, request_kind TEXT NOT NULL,
 status TEXT NOT NULL, error_code TEXT NOT NULL, legacy_request_id TEXT, legacy_deleted INTEGER NOT NULL,
 conversation_id TEXT, useragent_group TEXT, reasoning_effort TEXT,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cached_input_tokens INTEGER NOT NULL,
 reasoning_tokens INTEGER NOT NULL, reasoning_tokens_known INTEGER NOT NULL,
 cost_microdollars INTEGER NOT NULL, queue_latency_ms INTEGER NOT NULL,
 total_latency_ms INTEGER NOT NULL, latency_first_token_ms INTEGER);
CREATE INDEX idx_usage_rollup_journal_time ON usage_rollup_journal(requested_at);

CREATE TRIGGER usage_rollup_events_delete AFTER DELETE ON usage_events
WHEN OLD.rowid <= (SELECT folded_rowid FROM usage_rollup_state WHERE id=1)
BEGIN
 INSERT INTO usage_rollup_journal(sign,event_rowid,request_id,requested_at,api_key_id,account_id,model,
  request_kind,status,error_code,legacy_request_id,legacy_deleted,conversation_id,useragent_group,
  reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
  cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
 VALUES(-1,OLD.rowid,OLD.request_id,OLD.requested_at,OLD.api_key_id,OLD.account_id,OLD.model,
  OLD.request_kind,OLD.status,OLD.error_code,OLD.legacy_request_id,OLD.legacy_deleted,OLD.conversation_id,
  OLD.useragent_group,OLD.reasoning_effort,OLD.input_tokens,OLD.output_tokens,OLD.cached_input_tokens,
  OLD.reasoning_tokens,OLD.reasoning_tokens_known,OLD.cost_microdollars,OLD.queue_latency_ms,
  OLD.total_latency_ms,OLD.latency_first_token_ms);
 UPDATE usage_rollup_state SET folded_rows=folded_rows-1 WHERE id=1;
 -- Deleting the maximum rowid would let the next INSERT reuse a folded rowid (max+1).
 UPDATE usage_rollup_state SET folded_rowid=(SELECT coalesce(max(rowid),0) FROM usage_events),
  anchor_request_id=(SELECT request_id FROM usage_events ORDER BY rowid DESC LIMIT 1)
 WHERE id=1 AND folded_rowid>(SELECT coalesce(max(rowid),0) FROM usage_events);
END;

-- <same> = NEW.request_id IS OLD.request_id AND NEW.requested_at IS OLD.requested_at AND
--  NEW.api_key_id IS OLD.api_key_id AND NEW.account_id IS OLD.account_id AND NEW.model IS OLD.model AND
--  NEW.request_kind IS OLD.request_kind AND NEW.status IS OLD.status AND NEW.error_code IS OLD.error_code AND
--  NEW.legacy_request_id IS OLD.legacy_request_id AND NEW.legacy_deleted IS OLD.legacy_deleted AND
--  NEW.conversation_id IS OLD.conversation_id AND NEW.useragent_group IS OLD.useragent_group AND
--  NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.input_tokens IS OLD.input_tokens AND
--  NEW.output_tokens IS OLD.output_tokens AND NEW.cached_input_tokens IS OLD.cached_input_tokens AND
--  NEW.reasoning_tokens IS OLD.reasoning_tokens AND NEW.reasoning_tokens_known IS OLD.reasoning_tokens_known AND
--  NEW.queue_latency_ms IS OLD.queue_latency_ms AND NEW.total_latency_ms IS OLD.total_latency_ms AND
--  NEW.latency_first_token_ms IS OLD.latency_first_token_ms      (written out literally in the migration)
CREATE TRIGGER usage_rollup_events_reprice AFTER UPDATE OF cost_microdollars ON usage_events
WHEN OLD.rowid <= (SELECT folded_rowid FROM usage_rollup_state WHERE id=1)
 AND NEW.cost_microdollars<>OLD.cost_microdollars AND <same>
BEGIN INSERT INTO usage_rollup_journal(<cols>) VALUES(0, OLD.rowid, OLD.request_id, OLD.requested_at, ...OLD dims...,
 NEW.cost_microdollars-OLD.cost_microdollars, ...); END;

CREATE TRIGGER usage_rollup_events_update AFTER UPDATE ON usage_events
WHEN OLD.rowid <= (SELECT folded_rowid FROM usage_rollup_state WHERE id=1) AND NOT (<same>)
BEGIN INSERT INTO usage_rollup_journal(<cols>) VALUES(-1, OLD...);
      INSERT INTO usage_rollup_journal(<cols>) VALUES(1, NEW...); END;
```

The two UPDATE triggers are mutually exclusive by their `WHEN` clauses. In today's code, the only statements that update or delete `usage_events` are:

- reprice: `model_pricing_reprice.go:373` (cost only, so a 1-row delta);
- anonymize: `account_delete_history.go:142` (a move, so −old and +new);
- discard: `account_delete_history.go:140`;
- retention: `reports_retention.go:71`.

The hot-path INSERT (`usage_reports.go:49 insertUsageTx`) fires nothing. Constraint: `INSERT OR REPLACE` must never be used on `usage_events`, because REPLACE deletes do not fire triggers without `recursive_triggers`.

### 2.2 Rollup tables

Measures **M** = `requests, errors, cancelled, input_tokens, output_tokens, reasoning_tokens, reasoning_known, cached_input_clamped, cost_microdollars`, all `INTEGER NOT NULL`. Rows are deleted when `requests` reaches 0. A negative measure after an update is an inconsistency and triggers a rebuild.

```sql
-- R0: in_reports events, no dimensions
CREATE TABLE usage_rollup_total_hour (bucket_epoch INTEGER PRIMARY KEY, M,
 conversation_requests INTEGER NOT NULL, ttft_hist BLOB, queue_hist BLOB, tps_hist BLOB);

-- cube: in_reports events, every report filter dimension (rowid table: rows carry blobs)
CREATE TABLE usage_rollup_cube_hour (id INTEGER PRIMARY KEY, bucket_epoch INTEGER NOT NULL,
 account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL, model TEXT NOT NULL, ua_dim TEXT NOT NULL,
 deleted INTEGER NOT NULL, M, ttft_hist BLOB, queue_hist BLOB, tps_hist BLOB,
 UNIQUE(bucket_epoch,account_dim,api_key_dim,model,ua_dim,deleted));
CREATE INDEX idx_usage_rollup_cube_key ON usage_rollup_cube_hour(api_key_dim,bucket_epoch);
CREATE INDEX idx_usage_rollup_cube_account ON usage_rollup_cube_hour(account_dim,bucket_epoch);
CREATE INDEX idx_usage_rollup_cube_model ON usage_rollup_cube_hour(model,bucket_epoch);

-- one-dimension slices, hour and UTC-day grain: breakdowns, key trends, top-N keys/accounts/groups
CREATE TABLE usage_rollup_slice (kind TEXT NOT NULL CHECK(kind IN ('account','key','model','useragent')),
 grain INTEGER NOT NULL CHECK(grain IN (3600,86400)), bucket_epoch INTEGER NOT NULL, dim TEXT NOT NULL, M,
 PRIMARY KEY(kind,grain,bucket_epoch,dim)) WITHOUT ROWID;
CREATE INDEX idx_usage_rollup_slice_dim ON usage_rollup_slice(kind,dim,grain,bucket_epoch);

-- in_reports error-status events with error_code<>''
CREATE TABLE usage_rollup_error_hour (bucket_epoch INTEGER NOT NULL, error_code TEXT NOT NULL,
 account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL, errors INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,error_code,account_dim,api_key_dim)) WITHOUT ROWID;

-- non-warmup events with a trimmed non-empty conversation (both in_reports values, both deleted values)
CREATE TABLE usage_rollup_conversation_hour (id INTEGER PRIMARY KEY, bucket_epoch INTEGER NOT NULL,
 conversation_id TEXT NOT NULL, account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL, model TEXT NOT NULL,
 effort_dim TEXT NOT NULL, ua_dim TEXT NOT NULL, deleted INTEGER NOT NULL, in_reports INTEGER NOT NULL,
 requests INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 cached_input_tokens INTEGER NOT NULL, cost_microdollars INTEGER NOT NULL, total_latency_ms INTEGER NOT NULL,
 first_at INTEGER NOT NULL, last_at INTEGER NOT NULL, last_request_id TEXT NOT NULL,
 UNIQUE(bucket_epoch,conversation_id,account_dim,api_key_dim,model,effort_dim,ua_dim,deleted,in_reports));
CREATE INDEX idx_usage_rollup_conversation_id ON usage_rollup_conversation_hour(conversation_id,bucket_epoch);
CREATE INDEX idx_usage_rollup_conversation_key ON usage_rollup_conversation_hour(api_key_dim,bucket_epoch);

-- all events: request-log facets and totals
CREATE TABLE usage_rollup_log_hour (bucket_epoch INTEGER NOT NULL, account_dim TEXT NOT NULL,
 api_key_dim TEXT NOT NULL, model TEXT NOT NULL, effort_dim TEXT NOT NULL, status TEXT NOT NULL,
 requests INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,account_dim,api_key_dim,model,effort_dim,status)) WITHOUT ROWID;

-- all events: global facet values (model rows use detail=effort_dim)
CREATE TABLE usage_rollup_dim (kind TEXT NOT NULL CHECK(kind IN ('account','key','model','status')),
 value TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', requests INTEGER NOT NULL,
 PRIMARY KEY(kind,value,detail)) WITHOUT ROWID;
```

`bucket_epoch` is in seconds, `requested_at/3600000*3600` (or `/86400000*86400` for day grain). That matches `legacy_hourly_usage`.

### 2.3 Expected rows

Now = 36 k events/day, 9 accounts, 28 keys. At scale = 300 keys and about 30 accounts (est.). Rollups only cover the retained window (30 days in production). With retention off (the default), multiply by the days of history.

| Table | Rows/day now (est.) | 30 d now | 30 d @300 keys (est.) | Driver |
|---|---:|---:|---:|---|
| total_hour | 24 | 720 | 720 | hours |
| cube_hour | 1.5–2.5 k | 45–75 k | 0.3–0.7 M | active (account,key,model,UA) per hour, ≤ events |
| slice (h + d) | ~850 | ~25 k | ~150 k | (accounts+keys+models+UAs) per hour + per day |
| error_hour | 0.1–0.3 k | 3–9 k | 30–90 k | error rate |
| conversation_hour | 2–5 k | 60–150 k | 0.5–1.5 M | conversation-hours (not measured: Q1) |
| log_hour | 2–3 k | 60–90 k | 0.4–0.9 M | cube × effort × status |
| dim | – | < 500 | < 2 k | distinct values |
| journal | transient | ≈0 | ≈0 | changes not yet folded |
| state | – | 1 | 1 | |

Storage is about 40–70 MB now (est.), roughly 20–30 % of `usage_events` (294 MB). Write amplification per live event is about 17 row updates. These are batched, so cost is per distinct key per batch, not per event.

## 3. Folder

### 3.1 Algorithm

Code: `rollup_fold.go`, `Store.FoldUsageRollups(ctx, maxRows, maxJournal) (RollupFold, error)`. It runs as one `s.write` job, so in the batch writer, inside an IMMEDIATE transaction and a savepoint.

1. Load state, check `layout==1 && status=='ready'`, check the anchor: `SELECT request_id FROM usage_events WHERE rowid=folded_rowid` must equal `anchor_request_id`, or no row may exist. Load `hourly_folded_through`. If it differs from `import_watermark` and `folded_rowid>0`, request a rebuild.
2. Journal: `SELECT … FROM usage_rollup_journal ORDER BY seq LIMIT ?`, add each image with its sign, then `DELETE … WHERE seq<=last`.
3. Tail: `SELECT rowid, <image cols>, <flags SQL> FROM usage_events NOT INDEXED WHERE rowid>? ORDER BY rowid LIMIT ?`. `NOT INDEXED` keeps this a rowid-range scan. Add each with +1.
4. `delta.applyTx`: one upsert per distinct table key (`ON CONFLICT DO UPDATE SET x=x+excluded.x`), `first_at=min`, `last_at=max`, and `last_request_id` by the (`last_at`, `request_id`) pair. Histogram rows are a SELECT, a merge in Go, then UPDATE or INSERT. Rows touched by a negative delta are deleted when `requests=0`. Any negative measure returns `errRollupInconsistent`, which leads to a rebuild.
5. Conversation rows that a negative delta touched but that still have `requests>0` get first/last/last_request_id recomputed. One query per affected hour over that hour's folded raw rows (`idx_usage_events_time_id`, `rowid<=newCursor`, about 1.5 k rows) rebuilds every affected key of that hour.
6. `UPDATE usage_rollup_state SET folded_rowid=?, folded_rows=folded_rows+?, anchor_request_id=?, import_watermark=?, updated_at=?`.

Everything above is atomic in one transaction. A crash or error rolls back to the savepoint, and the next run redoes the same batch, so the folder is idempotent. Two concurrent folders (for example the M3 relay bug) are serialized by IMMEDIATE, and each one re-reads the cursor.

Loop (`application/usage_rollups.go`, `UsageRollupService.Run`, started in `cmd/codex-lb/runtime.go startBackground` as a new joined goroutine, so only the active worker runs it): `RunPeriodic` every 2 s. Each run folds batches until there is no backlog. After each batch it sleeps `max(10 ms, batch duration)`, keeping the writer duty cycle ≤ 50 %. Batch size adapts between 500 and 5,000 rows, targeting about 25 ms per job (well under `writeExecutionTimeout` 5 s and `writeLockShare` 250 ms). During backlog it calls `CheckpointWAL` (PASSIVE) every 20 batches. The folder does not wait for `waitForSoleWorker`: live folding is tiny and it keeps the old worker's inserts out of the tail.

### 3.2 Interplay with the rest of the system

- **Old worker inserts**, the 30-minute overlap: inserts get `rowid > cursor` and are folded within 2 s. Until then they are in the raw tail.
- **Late events** (reconciliation inserting with an old `requested_at`): they get a new rowid, so they are folded into their old hour. If that hour is older than the retention cutoff, the next retention pass moves them into `legacy_*` and the journal subtracts them from the rollups.
- **Retention** (unchanged code, any version): fold into `legacy_*`, then DELETE, which journals −1. Between retention and the folder, a query sees legacy(+e) + rollup(+e) + journal(−e) = e. After the folder runs it is legacy(+e) + rollup(0).
- **Reprice**: raw rows UPDATE fires a cost-delta journal row in the same transaction. Repricing of `legacy_hourly_usage` rows (`repriceFoldedChunkTx`) touches only legacy, which queries read directly. A full-history reprice of a 742 k-event model writes about 742 k journal rows over its chunks, and the folder drains them at about the backfill rate. Queries apply pending rows through `idx_usage_rollup_journal_time`, or use the raw path if the journal exceeds 20 k rows.
- **Account deletion**: anonymize is a move (−old, +new into `\x1f`/deleted=1). Discard is −1. The legacy table moves are unchanged.
- **Legacy import** (empty destination only, `legacy_import.go:105`): inserts are folded like any rows. Rows below `hourly_folded_through` get `in_reports=0`, which keeps them out of the report tables but in the conversation, log and dim tables, matching today. `legacy_hourly_usage`, `legacy_hourly_errors` and `legacy_conversation_hourly` are **not imported** into rollups in PR 1. They stay small (about 20 k rows in production), are mutated by retention, reprice and account deletion, and are read exactly as today. §7 phase 3 covers long ranges.

### 3.3 Rowid stability

- Rowid reuse: SQLite assigns `max(rowid)+1`, so after the folded maximum row is deleted, the next insert could reuse a folded rowid. The delete trigger lowers the cursor to the remaining maximum in the same statement, which is safe because every row ≤ that maximum is already folded.
- `VACUUM` and restored `VACUUM INTO` backups (`backup.go:112`) may renumber rowids of a table without INTEGER PRIMARY KEY. Detection:
  - every batch checks the anchor;
  - folder start checks `folded_rows == (SELECT count(*) FROM usage_events INDEXED BY sqlite_autoindex_usage_events_1 WHERE rowid<=folded_rowid)`, a read-only index scan of ≤ 0.5 s.

  On a mismatch, the folder rebuilds.
- Rebuild (`RebuildUsageRollups`):
  1. one job sets `status='rebuilding', folded_rowid=0, folded_rows=0`, clears the journal (from here the triggers journal nothing and readers use raw);
  2. batched deletes of the rollup tables, 5 k rows per job;
  3. `status='ready'`;
  4. the backfill runs.

### 3.4 Failure modes

| Failure | Effect | Handling |
|---|---|---|
| Fold job error or crash | savepoint/transaction rollback, nothing applied | retry with backoff; metric `usage_rollup_fold_errors_total` |
| Background not running (e.g. handover bug m1) | tail grows | above 20 k rows queries switch to raw automatically (today's latency); lag metric and alert |
| Rollback to go-v1.2.0 | no folder; triggers keep journaling changes to folded rows (bounded by the number of such changes); new inserts stay in the tail | old code's reports are correct (raw); on re-upgrade the folder drains the journal and tail (about the backfill time); raw path until then |
| Newer layout in the database | this version neither folds nor reads | raw path; the newer version owns the rebuild |
| Inconsistency (negative measure, anchor/count/watermark mismatch, verifier mismatch) | – | rebuild; raw path meanwhile |
| Discard images in the journal | deleted rows' account/conversation ids stay in the journal until folded (seconds; longer after a rollback) | documented; same fields the row had |
| WAL growth during backfill | about 0.3–0.5 GB of WAL written (est.) | PASSIVE checkpoint every 20 batches; `journal_size_limit` already 64 MB |

### 3.5 Backfill

- **Fresh install**: one transaction runs migrations 1–42. With no events the rollups are usable immediately. A subsequent legacy import is folded like an upgrade.
- **Upgrade with 742 k events**: the migration takes milliseconds (the old worker's writes wait only for that transaction). Batches of 1–2 k rows cost about 20–40 ms each (est.): ~10 ms reading rows, ~2 ms aggregating, about 600–750 upserts at 15–30 µs each, plus histogram merges. That is 370–740 batches and about 12–16 s of total lock time, about 30–60 s wall time at ≤ 50 % duty (est.). With 5 M events (retention off), it scales linearly to about 7 minutes. Readers use raw until the tail is ≤ 20 k rows. To be measured with `BenchmarkUsageRollupBackfill` on a 742 k synthetic database and on the production copy.

### 3.6 Verification

`VerifyUsageRollups` runs hourly in the folder goroutine, read-only, in one snapshot. It computes `QueryDashboardTraffic(1d)` and an unfiltered 7-day UTC report through both paths and compares them exactly (the paths are bit-identical by construction). On a mismatch it logs `usage_rollup_mismatch`, increments a metric, and (owner decision D6) requests a rebuild.

## 4. Query rewrite

### 4.1 Common parts (`rollup_read.go`)

- `rollupSnapshot(tx)`:
  ```sql
  SELECT layout,status,folded_rowid,
   (SELECT coalesce(max(rowid),0) FROM usage_events),
   (SELECT count(*) FROM (SELECT 1 FROM usage_rollup_journal LIMIT 20001)),
   (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1)
  FROM usage_rollup_state WHERE id=1
  ```
  `usable = layout==1 && status='ready' && tail<=20000 && journal<=20000 && switch!=off`. Every Store read method dispatches to `…Rollup` or to the existing `…Raw` code, which is kept as the fallback and the test oracle.
- Sources, all inside the same read transaction:
  - raw ranges: today's `rawReportWhere` SQL over the ms ranges, `INDEXED BY idx_usage_events_time_id`, or `idx_usage_events_key_time` / `idx_usage_events_account_time` when filtered;
  - the tail (`NOT INDEXED WHERE rowid>?`, restricted to the rollup hours, with the flags computed in SQL);
  - journal rows in the rollup hours (`idx_usage_rollup_journal_time`).

  All three feed `reportPass.addImage(img, sign)`. Rollup rows feed `addRollupRow(hourStart, M, hists)`.
- `reportPass` changes (`reports_aggregate.go`): latency slices become per-day histograms; `conversations` and `accounts` sets become signed count maps plus a legacy presence set; cost uses the canonical formula.

### 4.2 Per endpoint

**Dashboard** (`reports_dashboard_rollup.go`):

```sql
SELECT bucket_epoch,requests,errors,cancelled,input_tokens,output_tokens,reasoning_tokens,
 cached_input_clamped,cost_microdollars,conversation_requests
FROM usage_rollup_total_hour WHERE bucket_epoch>=:prevFromHour AND bucket_epoch<:toHour;
SELECT error_code,sum(errors) FROM usage_rollup_error_hour
 WHERE bucket_epoch>=:fromHour AND bucket_epoch<:toHour AND error_code<>'client_disconnected' GROUP BY 1;
SELECT bucket_epoch,conversation_id FROM usage_rollup_conversation_hour
 WHERE bucket_epoch>=:fromHour AND bucket_epoch<:toHour AND in_reports=1;   -- window + buckets in one pass
```

Plus: the legacy parts exactly as today (`legacy_hourly_usage` facts, `legacy_conversation_hourly`, `legacy_hourly_errors`), at most four raw edge ranges, the tail and the journal. `canCompare` is the legacy EXISTS, or the first total_hour row with `bucket_epoch<floorHour(prevStart)` (net of journal), or the raw edge, or the tail.

**Reports and key reports** (`reports_rollup.go`):
- Unfiltered: `usage_rollup_total_hour` rows for `[PreviousStart, End)`, selecting histograms only when `bucket_epoch>=:start`. Slices for byModel and byUA use day grain for full UTC days and hour grain for the rest. Account slices use hour grain (per-day active accounts). Conversation table rows with `in_reports=1`.
- Filtered:
  ```sql
  SELECT … FROM usage_rollup_cube_hour INDEXED BY idx_usage_rollup_cube_key
  WHERE api_key_dim IN (…) AND bucket_epoch>=? AND bucket_epoch<?
  ```
  The index is `_account` or `_model` when that filter is the most selective; a PK range is used for a UA-only filter. Conversations are filtered by the same dimensions (`idx_usage_rollup_conversation_key` for key filters).
- Legacy reads are unchanged (`readHourly`, `readHourlyConversations`, folded user-agent totals, aliases).

**Key usage-7d / trends** (`reports_key_rollup.go`):
- trends: `SELECT bucket_epoch,sum(input_tokens+output_tokens),sum(cost_microdollars) FROM usage_rollup_slice WHERE kind='key' AND grain=3600 AND dim=? AND bucket_epoch>=? AND bucket_epoch<? GROUP BY 1` (served by the slice PK and index).
- usage-7d: `SELECT account_dim,deleted,sum(requests),sum(input_tokens),sum(output_tokens),sum(cached_input_clamped),sum(cost_microdollars) FROM usage_rollup_cube_hour INDEXED BY idx_usage_rollup_cube_key WHERE api_key_dim=? AND bucket_epoch>=? AND bucket_epoch<? GROUP BY 1,2`, then an email join.
- Both add legacy key rows as today, plus raw edges through `idx_usage_events_key_time`.

**Conversations** (`reports_conversations_rollup.go`):
- list:
  ```sql
  SELECT conversation_id,account_dim,api_key_dim,model,requests,input_tokens+output_tokens,
   cached_input_tokens,cost_microdollars,first_at,last_at,last_request_id
  FROM usage_rollup_conversation_hour WHERE bucket_epoch>=:ceilHour(since) AND deleted=0
   [AND conversation_id LIKE :pat ESCAPE '\']
  ```
  Plus the raw edge `[since, ceilHour(since))`, the tail and the journal. Aggregated by conversation in Go, then sorted `last DESC, id ASC`, paged, and key names looked up for the page (`SELECT id,name FROM api_keys WHERE id IN (…)`, which removes today's N+1 at `reports_conversations.go:67`). LIKE on the trimmed id is equivalent to LIKE on the raw id, because the search term is trimmed.
- details: `… INDEXED BY idx_usage_rollup_conversation_id WHERE conversation_id=? AND deleted=0`, plus tail and journal rows with `trim(conversation_id)=?`.

**Top-N keys and groups** (new; index-bounded on the slice PK `(kind,grain,bucket_epoch,dim)`):

```sql
SELECT dim,sum(cost_microdollars) FROM usage_rollup_slice
 WHERE kind='key' AND ((grain=86400 AND bucket_epoch>=:d0 AND bucket_epoch<:d1)
  OR (grain=3600 AND ((bucket_epoch>=:h0 AND bucket_epoch<:d0h) OR (bucket_epoch>=:d1h AND bucket_epoch<:h1))))
 GROUP BY dim;   -- + raw edges/tail/journal + legacy key rows in Go; rank in Go (≤ #keys rows)
```

- Key groups: join the per-key totals to `api_keys.group_id` (current membership).
- Account groups: join the account totals to `account_group_accounts` (many-to-many).
- Rows read are about `#keys × (days + ≤ 46 partial hours)`: roughly 10–15 k for 30 days at 300 keys, about 20–40 ms (est.).

**Expected latency** (production scale, modernc, warm, est.):

| Endpoint | Today (measured) | Rollups (est.) | Dominant cost |
|---|---:|---:|---|
| overview 1d | 0.98 s | 40–60 ms | edges + N+1 accounts |
| overview 7d | 5.19 s | 100–130 ms | conversation rows ≈ 17 k |
| overview 30d | 14.64 s | 0.25–0.35 s | conversation rows ≈ 75 k |
| reports 7d (Prague) | 2.89 s | 60–100 ms | conversations per day |
| reports 30d / 90d | 8.0 / 8.2 s | 0.2–0.3 s | conversations |
| reports 730d | 8.59 s | 0.3–0.4 s today; grows with `legacy_hourly_usage` (§7 phase 3) | legacy rows |
| key report 7d (busy key) | 0.84 s | 30–50 ms | about 3.8 k cube rows ×2 |
| usage-7d / trends (busy key) | 0.75 / 0.34 s | 10–15 / 5–10 ms | key rows |
| request-log options (default) | 7.4 s (15.8 s cold) | 2–5 ms | dim table |
| options 24h/7d + filters | – | 5–30 ms | log cube |
| request-logs total (default) | 0.75 s (5.65 s cold) | < 1 ms | state counters |
| conversations 7d / 30d | 1.35 s / – | 50–80 / 150–250 ms | conversation rows |
| conversation details | ≈ 3×86 ms native | < 2 ms | index |

Each request now holds an analytics connection for milliseconds instead of seconds, which removes the 3-connection head-of-line blocking in `pages-latency.md` (overview 5.2 s became 8.5 s in parallel there).

### 4.3 What still needs raw events

- request-log pages (keyset/offset), per-request detail, search, `conversation_id` filters and their totals/options, and the conversation summary;
- tail, journal, edges and split hours;
- `WeeklyPaceTopKeys` (in PR 1);
- `backfillLimitTx`, `pairUsageTotals`, reprice, retention, account cleanup, legacy import;
- the fallback path.

Request-log pages with a rare status or model filter still walk the time index backwards. A follow-up could add `(status, requested_at, request_id)`, but it cannot be built inside an additive migration.

## 5. Request-log options and totals without full scans

- Default (no timeframe, no filters):
  ```sql
  SELECT kind,value,detail,requests FROM usage_rollup_dim
  ```
  (about 100 rows). Apply the tail and journal; drop values whose net is ≤ 0; drop `\x1f`; decode. Add the pending CTE (`pendingRequestLogSource`, partial index `idx_usage_reservations_open`). Join `api_keys` for names and prefixes as today. Reproduce today's ordering in Go: NULL effort first, then BINARY order.
- Timeframe and/or account, key, model, effort or model-option filters (statuses never filter facets, which is today's rule):
  ```sql
  SELECT account_dim,api_key_dim,model,effort_dim,status,sum(requests) FROM usage_rollup_log_hour
  WHERE bucket_epoch>=? AND bucket_epoch<? AND <filters> GROUP BY 1,2,3,4,5
  ```
  Plus raw edges (≤ 2 partial hours), tail and journal. Facets are taken over net > 0 rows.
- `ListRequestLogs.Total`:
  - no filter: `folded_rows + (SELECT count(*) FROM usage_events NOT INDEXED WHERE rowid>folded_rowid) + pending`;
  - cube-expressible filters: `sum(requests)` from the log cube plus edges, tail, journal and pending;
  - search or conversation id: raw, as today.

## 6. Test plan

Files in `internal/adapters/sqlite/`:

1. **`rollup_equivalence_test.go`**: randomized generator. It extends `TestQueryReportsMatchesPerDayReference` with more rows and with:
   - accounts `nil`/`""`/`\x1f…`, statuses, agents `""`/`nil`, conversations with spaces, warmups, legacy rows below and above the watermark, `legacy_deleted`;
   - late events, legacy hourly, conversation and error rows.

   Tested states:
   - (a) fully folded;
   - (b) folder stopped at a random cursor;
   - (c) pending journal after random retention, reprice, anonymize and discard operations;
   - (d) the journal applied.

   For each state, every endpoint method is compared via `reflect.DeepEqual` between the rollup path and the raw path: dashboard 1d/7d/30d with random `now`, reports, key usage/trends, options, totals, conversations, details. The filter matrix is the one from the existing test. Zones: UTC, Europe/Prague (2026-03-29 / 10-25 DST), America/New_York, Asia/Kolkata, Asia/Kathmandu, Australia/Lord_Howe.
2. **`reports_reference_test.go`**: keep `referenceQueryReports` for the exact metrics. Costs are compared within 1e-4 against the old float semantics.
3. **`rollup_hist_test.go`**: codec round trip; associativity of merge and subtract; a property check that `|histMedian − exactMedian| ≤ exact/64 + 0.005` (TPS: `+0.015`) on log-normal, uniform, constant, two-point and <32 ms distributions, against `referenceReportMedian`.
4. **`rollup_fold_test.go`**:
   - batch-size independence: folds of 1, 7 and 5,000 rows produce identical table dumps;
   - crash: a failpoint hook `rollupFoldFailpoint` errors at statement k, state and tables stay unchanged, a rerun matches a clean fold;
   - idempotence;
   - late events;
   - rowid reuse: delete the folded maximum, insert, the new row is folded;
   - `VACUUM INTO` plus reopen: renumbering detected, rebuild, results equal;
   - watermark change leads to a rebuild;
   - negative-measure injection leads to a rebuild;
   - conversation first/last recomputed after a partial deletion.
5. **`rollup_shared_test.go`** (pattern from `shared_database_test.go`, `openPeerStore`): store A folds, store B never folds and stands in for the old writer. PR 1 does not change settlement, retention, reprice or cleanup SQL, so B runs exactly the v41 statements. Interleave B's `SettleUsage`, `FoldAndPruneRequestLogs`, `repriceModelHistory` and `CleanupDeletedAccounts` with A's folds and queries; after each step rollup == raw. Also cover a long "rollback" (B only, a large journal), then A drains, then equality.
6. **`schema_floor_test.go`**: 42 is additive and the floor stays 40. **`rollup_migration_test.go`**:
   - migration on a 742 k synthetic database takes < 1 s;
   - a peer store at v41 code paths keeps writing during the migration;
   - triggers fire for a store that never folds.
7. **`rollup_benchmark_test.go`**: `BenchmarkUsageRollupBackfill` on 742 k rows (lock hold per batch, p50/p99) and the read benchmarks from `BenchmarkAdminReports` with rollups. Rerun the `pages-latency.md` harness on the production copy.
8. `cmd/codex-lb` managed-switch test: v1.2.0 → new → rollback → new, with dashboards equal to raw at each stage.

## 7. Delivery

**PR 1** (API contracts unchanged), as ordered commits:

1. `rollup_hist.go`; canonical cost and histogram medians in the raw path. This is the one value change and needs D1.
2. `schemaV42` and floor tests.
3. Folder, `UsageRollupService`, runtime wiring, rebuild, verifier, metrics; fold and two-store tests.
4. Window planner and sources; read paths for dashboard, reports, key reports, `/v1/usage/reports`, options, totals, usage-7d, trends, conversations list and details; equivalence tests.
5. Benchmarks and the production-copy latency run.

Rollout switch `CODEX_LB_USAGE_ROLLUPS=off|shadow|on` (shadow: read raw and verify in the background). Default `on` after shadow verification on the production copy.

**PR 2** (changes API semantics; owner decides):

- drop `byAccount` and `additionalQuotas`;
- Reports page takes key names from `/api/api-keys/` instead of options;
- request-log default timeframe 24h;
- gzip for key-report responses;
- dashboard poll 15 s → 30 s;
- new top-N keys/groups endpoints;
- an error-code breakdown and latency charts for key owners (already computable from `error_hour` and the cube).

**Phase 3** (long ranges): two options.
- (a) Additive: triggers on `legacy_hourly_usage`, `legacy_conversation_hourly` and `legacy_hourly_errors` journal deltas into "legacy slices", so 90d/730d stop scanning the legacy tables (they grow with retention).
- (b) Rollups become the long-term store: retention stops folding into `legacy_*`; tier and kind dimensions are added so reprice can reprice rollups; the reader floor is raised to 42 (rollback needs a backup restore). Benefit: medians and UA for pruned days.

**Owner decisions:**
- **D1**: accept approximate medians (≤ 1.6 %). The alternative keeps medians raw in PR 1 and costs about 1/3 of today's report time.
- **D2**: accept ±0.0001 cost on rounding ties.
- **D3**: fractional zones exact via raw split hours, or the 15-minute total table, or hour-start attribution.
- **D4**: group spend uses current `api_keys.group_id` / `account_group_accounts` membership (retroactive), or attribution at event time (needs a new column written at settlement, which old workers would not write).
- **D5**: accept first/last staleness of ≤ 2 s after deletions.
- **D6**: should a verifier mismatch trigger an automatic rebuild, or only alert.
- **D7**: phase 3 (a) or (b).

## 8. Open questions

- **Q1**: real cardinalities on the production copy: (account, key, model, UA) combinations per hour, conversation-hours per day, error rows. These size the indexes and confirm §2.3. Measure with read-only GROUP BY counts on the snapshot.
- **Q2**: does `VACUUM INTO` in modernc v1.59 preserve rowids? Detection covers either answer; the result decides whether a restore always forces a rebuild.
- **Q3**: histogram precision `p=5` (1.6 %) or `p=6` (0.8 %, about 2× blob size).
- **Q4**: should backfill duty drop to 25 % while `peerWorkers>0`?
- **Q5**: thresholds (20 k tail / 20 k journal) and the fold interval (2 s).
- **Q6**: should `WeeklyPaceTopKeys` move to the cube in PR 1?
- **Q7**: privacy of discard images held in the journal during a long rollback.

### Rejected alternatives

| Alternative | Why rejected |
|---|---|
| Update rollups in settlement | misses old-worker inserts |
| Full trigger-maintained rollups | heavy work inside settlement; histogram math in SQL needs functions old connections lack; a rollup bug would fail accounting writes |
| Cursor on `requested_at` | late events |
| AUTOINCREMENT or sequence column | needs a table rewrite or old-writer cooperation |
| HyperLogLog / t-digest | approximate and cannot be subtracted |
| Rollups as the long-term store in PR 1 | breaks old code's long-range reports after a rollback, and reprice cannot reach pruned events |

## Owner decisions (2026-10-28)

- D1: approximate daily medians from histograms with `p=5` (≤ 1/64 relative + rounding) are accepted; the raw fallback uses the same histograms.
- D2: integer-microdollar cost summation (±0.0001 on rounding ties versus the float sum) is accepted.
- D3: fractional-offset zones stay exact by reading the hour containing each local midnight raw.
- D4: group spend uses current membership (`api_keys.group_id`, `account_group_accounts`).
- D5: first/last staleness of up to one fold interval after deletions is accepted.
- D6: a verifier mismatch logs `usage_rollup_mismatch`, counts it and rebuilds; queries use the raw path meanwhile.
- D7: long-range legacy rollups (phase 3) are decided later.
- No `CODEX_LB_USAGE_ROLLUPS` switch: the automatic raw fallback and the verifier make it unnecessary (simplicity gate: no new setting without a reason a default cannot serve).

## Implementation decisions (PR 1, part A)

- Codec: `histQuantile(h, q)` gives any quantile (p50, p95, …) by linear interpolation between the representative values around rank `q·(n−1)`; for `q=0.5` that is the median rank rule, and `median` is implemented through it. Bound: within `q/64` of the same interpolation over exact values (values < 32 exact), plus 0.005 rounding for daily medians and 0.01 more for the stored TPS floor. Property-tested in `rollup_hist_test.go`.
- Layout 1 has a fourth histogram, `total_hist` (total request latency, sampled when `total_latency_ms>0`), on `usage_rollup_total_hour` and `usage_rollup_cube_hour`, folded and journaled like the other three. No current response shows it; the key page's total-latency p50/p95 will, without a layout change.
- Migration 42 adds `idx_usage_rollup_error_key ON usage_rollup_error_hour(api_key_dim,bucket_epoch)` for per-key error breakdowns.
- The delete trigger has no `WHEN` clause: its journal insert and `folded_rows` decrement are conditioned on `OLD.rowid<=folded_rowid`, and the cursor is lowered on every delete that leaves `folded_rowid > max(rowid)`. With the `WHEN` of §2.1, deleting an unfolded maximum row after the folded maximum had already been deleted left the cursor above the remaining maximum, and the next insert reused a folded rowid (covered by `TestUsageRollupDeleteTriggerKeepsInsertsAboveTheCursor`).
