package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"codex-lb/internal/domain"
)

const requestLogColumns = `request_id,legacy_request_id,requested_at,account_id,api_key_id,plan_type,
 request_kind,model,source,model_source_id,transport,useragent,useragent_group,
 client_ip,conversation_id,service_tier,status,error_code,input_tokens,output_tokens,
 reasoning_tokens,reasoning_tokens_known,cached_input_tokens,reasoning_effort,
 cost_microdollars,total_latency_ms,latency_first_token_ms,queue_latency_ms`

// The request log merges usage events with pending entries: reservations
// still reserved, flagged for reconciliation and without a usage event
// (pendingLogProjection, read by reports_logs_pending.go).

func requestLogWhere(filter domain.RequestLogFilter) (string, []any) {
	parts := []string{"1=1"}
	args := make([]any, 0)
	if filter.Since != nil {
		parts = append(parts, "e.requested_at>=?")
		args = append(args, millis(*filter.Since))
	}
	if filter.Until != nil {
		parts = append(parts, "e.requested_at<?")
		args = append(args, millis(*filter.Until))
	}
	if filter.ConversationID != "" {
		parts = append(parts, "e.conversation_id=?")
		args = append(args, filter.ConversationID)
	}
	if filter.Before != nil {
		// A row-value comparison is an index range on (requested_at, request_id).
		parts = append(parts, "(e.requested_at,e.request_id)<(?,?)")
		args = append(args, millis(filter.Before.RequestedAt), filter.Before.RequestID)
	}
	if len(filter.AccountIDs) > 0 {
		parts, args = appendInCondition(parts, args, "e.account_id", filter.AccountIDs)
	}
	if len(filter.APIKeyIDs) > 0 {
		parts, args = appendInCondition(parts, args, "e.api_key_id", filter.APIKeyIDs)
	}
	if len(filter.Statuses) > 0 {
		parts, args = appendInCondition(parts, args, "e.status", filter.Statuses)
	}
	if len(filter.Models) > 0 {
		parts, args = appendInCondition(parts, args, "e.model", filter.Models)
	}
	if len(filter.ReasoningEfforts) > 0 {
		parts, args = appendInCondition(parts, args, "e.reasoning_effort", filter.ReasoningEfforts)
	}
	if len(filter.ModelOptions) > 0 {
		options := make([]string, 0, len(filter.ModelOptions))
		for _, option := range filter.ModelOptions {
			model, effort, hasEffort := strings.Cut(option, ":::")
			if model == "" {
				continue
			}
			if hasEffort && effort != "" {
				options = append(options, "(e.model=? AND e.reasoning_effort=?)")
				args = append(args, model, effort)
			} else {
				options = append(options, "(e.model=? AND e.reasoning_effort IS NULL)")
				args = append(args, model)
			}
		}
		if len(options) > 0 {
			parts = append(parts, "("+strings.Join(options, " OR ")+")")
		}
	}
	if filter.Search != "" {
		term := "%" + escapeLike(filter.Search) + "%"
		parts = append(parts, `(e.model LIKE ? ESCAPE '\' OR coalesce(e.legacy_request_id,e.request_id) LIKE ? ESCAPE '\'
 OR e.error_code LIKE ? ESCAPE '\' OR e.useragent LIKE ? ESCAPE '\'
 OR e.conversation_id LIKE ? ESCAPE '\' OR a.alias LIKE ? ESCAPE '\' OR k.name LIKE ? ESCAPE '\')`)
		for range 7 {
			args = append(args, term)
		}
	}
	return strings.Join(parts, " AND "), args
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

// requestLogEventSource is the FROM clause of the filtered usage events.
func requestLogEventSource(filter domain.RequestLogFilter) (string, []any) {
	where, args := requestLogWhere(filter)
	joins := ""
	if filter.Search != "" {
		joins = ` LEFT JOIN accounts a ON a.id=e.account_id LEFT JOIN api_keys k ON k.id=e.api_key_id`
	}
	if where != "1=1" {
		joins += " WHERE " + where
	}
	return " FROM usage_events e" + joins, args
}

// requestLogPageSource defines log_page, the identifiers of one page, and
// log_entries, its rows. pending is pendingLogReader.pageCTE, empty when the
// filter selects no pending entry.
func requestLogPageSource(events, pending string) string {
	// Merge the indexed identifiers first. Joining the full UNION before LIMIT
	// makes SQLite scan and sort the entire history for every page.
	page := `WITH ` + pending + `log_page AS (
 SELECT e.request_id,e.requested_at,0 AS pending` + events
	entries := `), log_entries AS (
 SELECT ` + requestLogColumns + ` FROM usage_events
 WHERE request_id IN (SELECT request_id FROM log_page WHERE pending=0)`
	if pending != "" {
		page += `
 UNION ALL SELECT request_id,requested_at,1 FROM pending_page`
		entries += `
 UNION ALL SELECT ` + pendingLogProjection + ` FROM usage_reservations r
 WHERE r.id IN (SELECT request_id FROM log_page WHERE pending=1) AND r.status='reserved' AND r.needs_reconciliation=1`
	}
	return page + `
 ORDER BY requested_at DESC,request_id DESC LIMIT ? OFFSET ?
` + entries + `
) `
}

func (s *Store) ListRequestLogs(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogsResponse, error) {
	response := domain.RequestLogsResponse{Requests: []domain.RequestLogEntry{}}
	if filter.Limit < 1 || filter.Limit > 1000 || filter.Offset < 0 {
		return response, ErrInvalid
	}
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return response, err
	}
	defer tx.Rollback()
	events, args := requestLogEventSource(filter)
	joins := ` FROM log_entries e LEFT JOIN accounts a ON a.id=e.account_id
 LEFT JOIN api_keys k ON k.id=e.api_key_id
 LEFT JOIN legacy_model_sources ms ON ms.id=e.model_source_id`
	// The total describes the whole filtered result, not the rows after a cursor.
	unbounded := filter
	unbounded.Before = nil
	// The page stays a raw keyset or offset read; the total comes from the
	// rollups when they can serve the filter.
	reader, err := openRollupRead(ctx, tx)
	if err != nil {
		return response, err
	}
	pendingLogs := newPendingLogReader(tx)
	err = errRollupDeclined
	if reader != nil {
		response.Total, err = reader.requestLogTotal(ctx, pendingLogs, unbounded)
	}
	if errors.Is(err, errRollupDeclined) {
		response.Total, err = requestLogTotalRaw(ctx, tx, pendingLogs, unbounded)
	}
	if err != nil {
		return response, err
	}
	pending, pageArgs, err := pendingLogs.pageCTE(ctx, filter, filter.Offset+filter.Limit+1)
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
	pageArgs = append(append(pageArgs, args...), filter.Limit+1, filter.Offset)
	rows, err := tx.QueryContext(ctx, requestLogPageSource(events, pending)+query, pageArgs...)
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
		// Pending entries have neither a conversation nor a cost.
		var costMicro int64
		countEvents, countArgs := requestLogEventSource(unbounded)
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(e.cost_microdollars),0)`+countEvents, countArgs...).Scan(&costMicro); err != nil {
			return response, err
		}
		response.Conversation = &struct {
			RequestCount      int64   `json:"requestCount"`
			AggregatedCostUSD float64 `json:"aggregatedCostUsd"`
		}{response.Total, float64(costMicro) / 1e6}
	}
	return response, nil
}

func logNullableInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// requestLogTotalRaw counts the filtered events and pending entries.
func requestLogTotalRaw(ctx context.Context, tx *sql.Tx, pending *pendingLogReader, filter domain.RequestLogFilter) (int64, error) {
	events, args := requestLogEventSource(filter)
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+events, args...).Scan(&total); err != nil {
		return 0, err
	}
	entries, err := pending.count(ctx, filter)
	return total + entries, err
}

func emptyRequestLogOptions() domain.RequestLogOptions {
	return domain.RequestLogOptions{AccountIDs: []string{}, ModelOptions: []domain.RequestLogModelOption{},
		APIKeys: []domain.RequestLogAPIKeyOption{}, Statuses: []string{}}
}

// RequestLogOptions returns the filter facets of the request log from
// rollups (reports_logs_rollup.go) or, as fallback and oracle, from raw
// events. Statuses never filter the facets and a cursor never limits them.
func (s *Store) RequestLogOptions(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	filter.Statuses = nil
	filter.Before = nil
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.RequestLogOptions, error) { return r.requestLogOptions(ctx, filter) },
		func(tx *sql.Tx) (domain.RequestLogOptions, error) { return requestLogOptionsRaw(ctx, tx, filter) })
}

func requestLogOptionsRaw(ctx context.Context, tx *sql.Tx, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	options := emptyRequestLogOptions()
	filter.Statuses = nil // Status facet must not self-filter.
	filter.Before = nil   // Facets describe the whole result, not one page.
	events, args := requestLogEventSource(filter)
	// The pending entries' facets are merged into the events' sorted and
	// limited facets, which keeps the order and limit of their union.
	groups, err := pendingLogGroups(ctx, tx, filter)
	if err != nil {
		return options, err
	}
	// DISTINCT deduplicates only the facet columns; no full log rows or
	// unrelated account/key lookups are needed to populate the filter controls.
	rows, err := tx.QueryContext(ctx, `SELECT account_id FROM (SELECT DISTINCT e.account_id`+events+`)
 WHERE account_id IS NOT NULL ORDER BY account_id LIMIT 2000`, args...)
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
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT e.model,e.reasoning_effort`+events+` ORDER BY 1,2 LIMIT 2000`, args...)
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
	rows, err = tx.QueryContext(ctx, `SELECT k.id,k.name,k.key_prefix FROM (SELECT DISTINCT e.api_key_id`+events+`) e
 JOIN api_keys k ON k.id=e.api_key_id ORDER BY k.name,k.id LIMIT 2000`, args...)
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
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT e.status`+events+` ORDER BY 1 LIMIT 2000`, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			rows.Close()
			return options, err
		}
		options.Statuses = append(options.Statuses, status)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(groups) == 0 {
		return options, err
	}
	var accounts []string
	var models []domain.RequestLogModelOption
	keys := map[string]bool{}
	for _, group := range groups {
		if group.account.Valid {
			accounts = append(accounts, group.account.String)
		}
		if group.key.Valid {
			keys[group.key.String] = true
		}
		models = append(models, domain.RequestLogModelOption{Model: group.model})
	}
	pendingKeys, err := requestLogKeyOptions(ctx, tx, keys)
	if err != nil {
		return options, err
	}
	options.AccountIDs = mergeRequestLogFacet(options.AccountIDs, accounts, strings.Compare)
	options.ModelOptions = mergeRequestLogFacet(options.ModelOptions, models, compareRequestLogModelOptions)
	options.APIKeys = mergeRequestLogFacet(options.APIKeys, pendingKeys, compareRequestLogKeyOptions)
	options.Statuses = mergeRequestLogFacet(options.Statuses, []string{pendingLogStatus}, strings.Compare)
	return options, nil
}

// mergeRequestLogFacet adds values to the sorted, distinct and limited values
// of one facet query: the result is the query over the union of both.
func mergeRequestLogFacet[T any](sorted, values []T, compare func(a, b T) int) []T {
	merged := append(slices.Clip(sorted), values...)
	slices.SortFunc(merged, compare)
	merged = slices.CompactFunc(merged, func(a, b T) bool { return compare(a, b) == 0 })
	return merged[:min(len(merged), 2000)]
}

// compareRequestLogModelOptions is the facet order ORDER BY model,
// reasoning_effort: NULL effort first, then BINARY order.
func compareRequestLogModelOptions(a, b domain.RequestLogModelOption) int {
	if c := strings.Compare(a.Model, b.Model); c != 0 {
		return c
	}
	switch {
	case a.ReasoningEffort == nil && b.ReasoningEffort == nil:
		return 0
	case a.ReasoningEffort == nil:
		return -1
	case b.ReasoningEffort == nil:
		return 1
	}
	return strings.Compare(*a.ReasoningEffort, *b.ReasoningEffort)
}

// compareRequestLogKeyOptions is the facet order ORDER BY k.name,k.id.
func compareRequestLogKeyOptions(a, b domain.RequestLogAPIKeyOption) int {
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}
