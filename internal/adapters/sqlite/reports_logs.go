package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"codex-lb/internal/domain"
)

const requestLogColumns = `request_id,legacy_request_id,requested_at,account_id,api_key_id,plan_type,
 request_kind,model,source,model_source_id,transport,useragent,useragent_group,
 client_ip,conversation_id,service_tier,status,error_code,input_tokens,output_tokens,
 reasoning_tokens,reasoning_tokens_known,cached_input_tokens,reasoning_effort,
 cost_microdollars,total_latency_ms,latency_first_token_ms,queue_latency_ms`

// Pending rows are a read projection, not usage events: held budget is never
// presented as actual usage and ordinary live reservations remain invisible.
const pendingRequestLogSource = `WITH pending_log_entries (` + requestLogColumns + `) AS (
 SELECT r.id,NULL,r.created_at,CASE WHEN EXISTS (SELECT 1 FROM account_deletions d
 WHERE d.account_id=r.account_id AND d.generation=r.account_generation) THEN NULL
 ELSE nullif(r.account_id,'') END,nullif(r.api_key_id,'` + domain.WarmupKeyID + `'),'',
 CASE WHEN r.api_key_id='` + domain.WarmupKeyID + `' THEN 'warmup' ELSE 'unknown' END,r.model,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,
 'reconciliation_required','usage_reconciliation_required',NULL,NULL,NULL,0,NULL,NULL,
 NULL,NULL,NULL,NULL
 FROM usage_reservations r WHERE r.status='reserved' AND r.needs_reconciliation=1
 AND NOT EXISTS (SELECT 1 FROM usage_events recorded WHERE recorded.reservation_id=r.id)
) `

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

func requestLogSources(filter domain.RequestLogFilter) (events, pending string, args []any) {
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

func requestLogPageSource(events, pending string) string {
	// Merge the indexed identifiers first. Joining the full UNION before LIMIT
	// makes SQLite scan and sort the entire history for every page.
	return pendingRequestLogSource + `, log_page AS (
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

func (s *Store) ListRequestLogs(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogsResponse, error) {
	response := domain.RequestLogsResponse{Requests: []domain.RequestLogEntry{}}
	if filter.Limit < 1 || filter.Limit > 1000 || filter.Offset < 0 {
		return response, ErrInvalid
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return response, err
	}
	defer tx.Rollback()
	events, pending, args := requestLogSources(filter)
	joins := ` FROM log_entries e LEFT JOIN accounts a ON a.id=e.account_id
 LEFT JOIN api_keys k ON k.id=e.api_key_id
 LEFT JOIN legacy_model_sources ms ON ms.id=e.model_source_id`
	countQuery := pendingRequestLogSource + `SELECT (SELECT count(*)` + events + `)+(SELECT count(*)` + pending + `)`
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&response.Total); err != nil {
		return response, err
	}
	query := `SELECT e.requested_at,e.account_id,nullif(coalesce(e.plan_type,a.plan_type),''),
 k.name,e.api_key_id,coalesce(e.legacy_request_id,e.request_id),e.request_kind,e.model,
 e.source,e.model_source_id,ms.kind,e.transport,e.useragent,e.useragent_group,
 e.client_ip,e.conversation_id,nullif(e.service_tier,''),e.status,nullif(e.error_code,''),
 e.input_tokens,e.output_tokens,e.reasoning_tokens,e.reasoning_tokens_known,
 e.cached_input_tokens,e.reasoning_effort,e.cost_microdollars,e.total_latency_ms,
 e.latency_first_token_ms,e.queue_latency_ms` + joins + ` ORDER BY e.requested_at DESC,e.request_id DESC`
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := tx.QueryContext(ctx, requestLogPageSource(events, pending)+query, pageArgs...)
	if err != nil {
		return response, err
	}
	for rows.Next() {
		var item domain.RequestLogEntry
		var requested int64
		var account, plan, keyName, keyID, source, sourceID, sourceKind, transport, useragent, group, ip,
			conversation, tier, errorCode, effort sql.NullString
		var input, output, reasoning, cached, cost, total, queue sql.NullInt64
		var first sql.NullInt64
		var reasoningKnown bool
		if err := rows.Scan(&requested, &account, &plan, &keyName, &keyID, &item.RequestID,
			&item.RequestKind, &item.Model, &source, &sourceID, &sourceKind, &transport, &useragent, &group,
			&ip, &conversation, &tier, &item.Status, &errorCode, &input, &output, &reasoning,
			&reasoningKnown, &cached, &effort, &cost, &total, &first, &queue); err != nil {
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
		response.Requests = append(response.Requests, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return response, err
	}
	response.HasMore = int64(filter.Offset+len(response.Requests)) < response.Total
	if filter.ConversationID != "" {
		var costMicro int64
		costQuery := pendingRequestLogSource + `SELECT coalesce(sum(cost_microdollars),0) FROM (
 SELECT e.cost_microdollars` + events + ` UNION ALL SELECT e.cost_microdollars` + pending + `)`
		if err := tx.QueryRowContext(ctx, costQuery, args...).Scan(&costMicro); err != nil {
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

func (s *Store) RequestLogOptions(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	options := domain.RequestLogOptions{AccountIDs: []string{}, ModelOptions: []domain.RequestLogModelOption{},
		APIKeys: []domain.RequestLogAPIKeyOption{}, Statuses: []string{}}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return options, err
	}
	defer tx.Rollback()
	filter.Statuses = nil // Status facet must not self-filter.
	events, pending, args := requestLogSources(filter)
	// UNION deduplicates only the facet columns; no full log rows or unrelated
	// account/key lookups are needed to populate the filter controls.
	accountQuery := pendingRequestLogSource + `SELECT account_id FROM (
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
	modelQuery := pendingRequestLogSource + `SELECT DISTINCT e.model,e.reasoning_effort` + events + `
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
	keyQuery := pendingRequestLogSource + `SELECT k.id,k.name,k.key_prefix FROM (
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
	statusQuery := pendingRequestLogSource + `SELECT DISTINCT e.status` + events + `
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
