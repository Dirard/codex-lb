package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"codex-lb/internal/domain"
)

// The request-log pending entries as they were read before
// idx_usage_reservations_pending: the reference the optimized reads must
// match field for field. Verbatim except for the legacy names.

const legacyPendingRequestLogSource = `WITH pending_log_entries (` + requestLogColumns + `) AS (
 SELECT r.id,NULL,r.created_at,CASE WHEN EXISTS (SELECT 1 FROM account_deletions d
 WHERE d.account_id=r.account_id AND d.generation=r.account_generation) THEN NULL
 ELSE nullif(r.account_id,'') END,nullif(r.api_key_id,'` + domain.WarmupKeyID + `'),'',
 CASE WHEN r.api_key_id='` + domain.WarmupKeyID + `' THEN 'warmup' ELSE 'unknown' END,r.model,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,
 'reconciliation_required','usage_reconciliation_required',NULL,NULL,NULL,0,NULL,NULL,
 NULL,NULL,NULL,NULL
 FROM usage_reservations r WHERE r.status='reserved' AND r.needs_reconciliation=1
 AND NOT EXISTS (SELECT 1 FROM usage_events recorded WHERE recorded.reservation_id=r.id)
) `

func legacyRequestLogSources(filter domain.RequestLogFilter) (events, pending string, args []any) {
	where, args := requestLogWhere(filter)
	joins := ""
	if filter.Search != "" {
		joins = ` LEFT JOIN accounts a ON a.id=e.account_id LEFT JOIN api_keys k ON k.id=e.api_key_id`
	}
	if where != "1=1" {
		joins += " WHERE " + where
	}
	return " FROM usage_events e" + joins, " FROM pending_log_entries e" + joins,
		append(append([]any{}, args...), args...)
}

func legacyRequestLogPageSource(events, pending string) string {
	// Merge the indexed identifiers first. Joining the full UNION before LIMIT
	// makes SQLite scan and sort the entire history for every page.
	return legacyPendingRequestLogSource + `, log_page AS (
 SELECT e.request_id,e.requested_at,0 AS pending` + events + `
 UNION ALL SELECT e.request_id,e.requested_at,1` + pending + `
 ORDER BY requested_at DESC,request_id DESC LIMIT ? OFFSET ?
), log_entries AS (
 SELECT ` + requestLogColumns + ` FROM usage_events
 WHERE request_id IN (SELECT request_id FROM log_page WHERE pending=0)
 UNION ALL SELECT * FROM pending_log_entries
 WHERE request_id IN (SELECT request_id FROM log_page WHERE pending=1)
) `
}

func (s *Store) legacyListRequestLogs(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogsResponse, error) {
	response := domain.RequestLogsResponse{Requests: []domain.RequestLogEntry{}}
	if filter.Limit < 1 || filter.Limit > 1000 || filter.Offset < 0 {
		return response, ErrInvalid
	}
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return response, err
	}
	defer tx.Rollback()
	events, pending, args := legacyRequestLogSources(filter)
	joins := ` FROM log_entries e LEFT JOIN accounts a ON a.id=e.account_id
 LEFT JOIN api_keys k ON k.id=e.api_key_id
 LEFT JOIN legacy_model_sources ms ON ms.id=e.model_source_id`
	// The total describes the whole filtered result, not the rows after a cursor.
	unbounded := filter
	unbounded.Before = nil
	countEvents, countPending, countArgs := legacyRequestLogSources(unbounded)
	// The page stays a raw keyset or offset read; the total comes from the
	// rollups when they can serve the filter.
	reader, err := openRollupRead(ctx, tx)
	if err != nil {
		return response, err
	}
	err = errRollupDeclined
	if reader != nil {
		response.Total, err = reader.legacyRequestLogTotal(ctx, unbounded)
	}
	if errors.Is(err, errRollupDeclined) {
		response.Total, err = legacyRequestLogTotalRaw(ctx, tx, unbounded)
	}
	if err != nil {
		return response, err
	}
	query := `SELECT e.requested_at,e.account_id,nullif(coalesce(e.plan_type,a.plan_type),''),
 k.name,e.api_key_id,coalesce(e.legacy_request_id,e.request_id),e.request_kind,e.model,
 e.source,e.model_source_id,ms.kind,e.transport,e.useragent,e.useragent_group,
 e.client_ip,e.conversation_id,nullif(e.service_tier,''),e.status,nullif(e.error_code,''),
 e.input_tokens,e.output_tokens,e.reasoning_tokens,e.reasoning_tokens_known,
 e.cached_input_tokens,e.reasoning_effort,e.cost_microdollars,e.total_latency_ms,
 e.latency_first_token_ms,e.queue_latency_ms,e.request_id` + joins + ` ORDER BY e.requested_at DESC,e.request_id DESC`
	// One extra row tells whether a keyset continuation exists.
	pageArgs := append(append([]any{}, args...), filter.Limit+1, filter.Offset)
	rows, err := tx.QueryContext(ctx, legacyRequestLogPageSource(events, pending)+query, pageArgs...)
	if err != nil {
		return response, err
	}
	more := false
	var last domain.RequestLogCursor
	for rows.Next() {
		var item domain.RequestLogEntry
		var requested int64
		var internalID string
		var account, plan, keyName, keyID, source, sourceID, sourceKind, transport, useragent, group, ip,
			conversation, tier, errorCode, effort sql.NullString
		var input, output, reasoning, cached, cost, total, queue sql.NullInt64
		var first sql.NullInt64
		var reasoningKnown bool
		if err := rows.Scan(&requested, &account, &plan, &keyName, &keyID, &item.RequestID,
			&item.RequestKind, &item.Model, &source, &sourceID, &sourceKind, &transport, &useragent, &group,
			&ip, &conversation, &tier, &item.Status, &errorCode, &input, &output, &reasoning,
			&reasoningKnown, &cached, &effort, &cost, &total, &first, &queue, &internalID); err != nil {
			rows.Close()
			return response, err
		}
		item.RequestedAt = fromMillis(requested)
		item.AccountID = optionalString(account)
		item.PlanType = optionalString(plan)
		item.APIKeyName = optionalString(keyName)
		item.APIKeyID = optionalString(keyID)
		item.Source = optionalString(source)
		item.ModelSourceID = optionalString(sourceID)
		item.ModelSourceKind = optionalString(sourceKind)
		item.Transport = optionalString(transport)
		item.UserAgent = optionalString(useragent)
		item.UserAgentGroup = optionalString(group)
		item.ClientIP = optionalString(ip)
		item.ConversationID = optionalString(conversation)
		item.ServiceTier = optionalString(tier)
		item.ErrorCode = optionalString(errorCode)
		item.ReasoningEffort = optionalString(effort)
		item.InputTokens = logNullableInt(input)
		item.OutputTokens = logNullableInt(output)
		item.CachedInputTokens = logNullableInt(cached)
		if reasoningKnown || reasoning.Valid && reasoning.Int64 > 0 {
			item.ReasoningTokens = logNullableInt(reasoning)
		}
		if input.Valid && output.Valid {
			tokens := input.Int64 + output.Int64
			item.Tokens = &tokens
		}
		if cost.Valid {
			costUSD := float64(cost.Int64) / 1e6
			item.CostUSD = &costUSD
		}
		item.LatencyMS = logNullableInt(total)
		item.LatencyQueueMS = logNullableInt(queue)
		if item.Status == "reconciliation_required" {
			message := "Actual usage is unknown; the reserved budget is held pending reconciliation."
			item.ErrorMessage = &message
		}
		if first.Valid && first.Int64 > 0 {
			item.LatencyFirstTokenMS = &first.Int64
		}
		if len(response.Requests) == filter.Limit {
			more = true
			break
		}
		response.Requests = append(response.Requests, item)
		last = domain.RequestLogCursor{RequestedAt: fromMillis(requested), RequestID: internalID}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return response, err
	}
	response.HasMore = int64(filter.Offset+len(response.Requests)) < response.Total
	if filter.Before != nil {
		response.HasMore = more
	}
	if more {
		cursor := last.String()
		response.NextCursor = &cursor
	}
	if filter.ConversationID != "" {
		var costMicro int64
		costQuery := legacyPendingRequestLogSource + `SELECT coalesce(sum(cost_microdollars),0) FROM (
 SELECT e.cost_microdollars` + countEvents + ` UNION ALL SELECT e.cost_microdollars` + countPending + `)`
		if err := tx.QueryRowContext(ctx, costQuery, countArgs...).Scan(&costMicro); err != nil {
			return response, err
		}
		response.Conversation = &struct {
			RequestCount      int64   `json:"requestCount"`
			AggregatedCostUSD float64 `json:"aggregatedCostUsd"`
		}{response.Total, float64(costMicro) / 1e6}
	}
	return response, nil
}

func legacyRequestLogTotalRaw(ctx context.Context, tx *sql.Tx, filter domain.RequestLogFilter) (int64, error) {
	countEvents, countPending, countArgs := legacyRequestLogSources(filter)
	var total int64
	err := tx.QueryRowContext(ctx, legacyPendingRequestLogSource+`SELECT (SELECT count(*)`+countEvents+`)+(SELECT count(*)`+
		countPending+`)`, countArgs...).Scan(&total)
	return total, err
}

func legacyRequestLogOptionsRaw(ctx context.Context, tx *sql.Tx, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	options := emptyRequestLogOptions()
	filter.Statuses = nil // Status facet must not self-filter.
	filter.Before = nil   // Facets describe the whole result, not one page.
	events, pending, args := legacyRequestLogSources(filter)
	// UNION deduplicates only the facet columns; no full log rows or unrelated
	// account/key lookups are needed to populate the filter controls.
	accountQuery := legacyPendingRequestLogSource + `SELECT account_id FROM (
 SELECT DISTINCT e.account_id` + events + ` UNION SELECT DISTINCT e.account_id` + pending + `)
 WHERE account_id IS NOT NULL ORDER BY account_id LIMIT 2000`
	rows, err := tx.QueryContext(ctx, accountQuery, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return options, err
		}
		options.AccountIDs = append(options.AccountIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return options, err
	}
	modelQuery := legacyPendingRequestLogSource + `SELECT DISTINCT e.model,e.reasoning_effort` + events + `
 UNION SELECT DISTINCT e.model,e.reasoning_effort` + pending + ` ORDER BY model,reasoning_effort LIMIT 2000`
	rows, err = tx.QueryContext(ctx, modelQuery, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var item domain.RequestLogModelOption
		var effort sql.NullString
		if err := rows.Scan(&item.Model, &effort); err != nil {
			rows.Close()
			return options, err
		}
		item.ReasoningEffort = optionalString(effort)
		options.ModelOptions = append(options.ModelOptions, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return options, err
	}
	keyQuery := legacyPendingRequestLogSource + `SELECT k.id,k.name,k.key_prefix FROM (
 SELECT DISTINCT e.api_key_id` + events + ` UNION SELECT DISTINCT e.api_key_id` + pending + `) e
 JOIN api_keys k ON k.id=e.api_key_id ORDER BY k.name,k.id LIMIT 2000`
	rows, err = tx.QueryContext(ctx, keyQuery, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var item domain.RequestLogAPIKeyOption
		var prefix sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &prefix); err != nil {
			rows.Close()
			return options, err
		}
		item.KeyPrefix = optionalString(prefix)
		options.APIKeys = append(options.APIKeys, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return options, err
	}
	statusQuery := legacyPendingRequestLogSource + `SELECT DISTINCT e.status` + events + `
 UNION SELECT DISTINCT e.status` + pending + ` ORDER BY 1 LIMIT 2000`
	rows, err = tx.QueryContext(ctx, statusQuery, args...)
	if err != nil {
		return options, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return options, err
		}
		options.Statuses = append(options.Statuses, status)
	}
	return options, rows.Err()
}

func (r *rollupRead) legacyRequestLogOptions(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	options := emptyRequestLogOptions()
	if !requestLogCubeEligible(filter) {
		return options, errRollupDeclined
	}
	filter.Statuses, filter.Before = nil, nil
	facets, err := r.requestLogFacetCounts(ctx, filter)
	if err != nil {
		return options, err
	}
	where, args := requestLogWhere(filter)
	rows, err := r.tx.QueryContext(ctx, legacyPendingRequestLogSource+`SELECT e.account_id,e.api_key_id,e.model,
 e.reasoning_effort,e.status FROM pending_log_entries e WHERE `+where, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var account, key, effort sql.NullString
		var model, status string
		if err := rows.Scan(&account, &key, &model, &effort, &status); err != nil {
			rows.Close()
			return options, err
		}
		facets.add(rawReportDimension(account), rawReportDimension(key), model, rawReportDimension(effort), status, 1)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return options, err
	}
	for account, n := range facets.accounts {
		if id := decodeRollupDim(account); n > 0 && id.Valid {
			options.AccountIDs = append(options.AccountIDs, id.String)
		}
	}
	sort.Strings(options.AccountIDs)
	options.AccountIDs = options.AccountIDs[:min(len(options.AccountIDs), 2000)]
	for option, n := range facets.models {
		if n > 0 {
			options.ModelOptions = append(options.ModelOptions, domain.RequestLogModelOption{Model: option[0],
				ReasoningEffort: optionalString(decodeRollupDim(option[1]))})
		}
	}
	sort.Slice(options.ModelOptions, func(i, j int) bool {
		a, b := options.ModelOptions[i], options.ModelOptions[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.ReasoningEffort == nil || b.ReasoningEffort == nil {
			return a.ReasoningEffort == nil && b.ReasoningEffort != nil
		}
		return *a.ReasoningEffort < *b.ReasoningEffort
	})
	options.ModelOptions = options.ModelOptions[:min(len(options.ModelOptions), 2000)]
	if options.APIKeys, err = r.legacyRequestLogKeyOptions(ctx, facets.keys); err != nil {
		return options, err
	}
	for status, n := range facets.statuses {
		if n > 0 {
			options.Statuses = append(options.Statuses, status)
		}
	}
	sort.Strings(options.Statuses)
	options.Statuses = options.Statuses[:min(len(options.Statuses), 2000)]
	return options, nil
}

func (r *rollupRead) legacyRequestLogKeyOptions(ctx context.Context, keys map[string]int64) ([]domain.RequestLogAPIKeyOption, error) {
	options := []domain.RequestLogAPIKeyOption{}
	present := map[string]bool{}
	for key, n := range keys {
		if id := decodeRollupDim(key); n > 0 && id.Valid {
			present[id.String] = true
		}
	}
	if len(present) == 0 {
		return options, nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT id,name,key_prefix FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.RequestLogAPIKeyOption
		var prefix sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &prefix); err != nil {
			return nil, err
		}
		if present[item.ID] {
			item.KeyPrefix = optionalString(prefix)
			options = append(options, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].ID < options[j].ID
	})
	return options[:min(len(options), 2000)], nil
}

func (r *rollupRead) legacyRequestLogTotal(ctx context.Context, filter domain.RequestLogFilter) (int64, error) {
	if !requestLogCubeEligible(filter) {
		return 0, errRollupDeclined
	}
	where, args := requestLogWhere(filter)
	var total int64
	if err := r.tx.QueryRowContext(ctx, legacyPendingRequestLogSource+`SELECT count(*) FROM pending_log_entries e WHERE `+where,
		args...).Scan(&total); err != nil {
		return 0, err
	}
	if requestLogUnfiltered(filter) {
		var tail int64
		if err := r.tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_events WHERE rowid>?`, r.state.foldedRowid).Scan(&tail); err != nil {
			return 0, err
		}
		return total + r.state.foldedRows + tail, nil
	}
	plan := requestLogPlan(filter)
	eventWhere, eventArgs := requestLogEventWhere(filter)
	if err := r.images(ctx, plan, eventWhere, eventArgs, func(_ *rollupImage, sign int64) { total += sign }); err != nil {
		return 0, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return total, nil
	}
	cubeWhere, cubeArgs := requestLogCubeWhere(filter)
	var events int64
	if err := r.tx.QueryRowContext(ctx, `SELECT coalesce(sum(c.requests),0) FROM usage_rollup_log_hour c
 WHERE c.bucket_epoch>=? AND c.bucket_epoch<? AND `+cubeWhere, append([]any{from, to}, cubeArgs...)...).Scan(&events); err != nil {
		return 0, err
	}
	return total + events, nil
}

// legacyRequestLogOptions is RequestLogOptions over the reference readers.
func (s *Store) legacyRequestLogOptions(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	filter.Statuses = nil
	filter.Before = nil
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.RequestLogOptions, error) { return r.legacyRequestLogOptions(ctx, filter) },
		func(tx *sql.Tx) (domain.RequestLogOptions, error) { return legacyRequestLogOptionsRaw(ctx, tx, filter) })
}
