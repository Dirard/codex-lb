package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"codex-lb/internal/domain"
)

func conversationWhere(filter domain.ConversationFilter) (string, []any) {
	where := rollupConversationSQL("e") + ` AND e.legacy_deleted=0`
	args := []any{}
	if !filter.Since.IsZero() {
		where += " AND e.requested_at>=?"
		args = append(args, millis(filter.Since))
	}
	if filter.Search != "" {
		where += ` AND e.conversation_id LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(filter.Search)+"%")
	}
	return where, args
}

// ListConversations lists conversations by latest request from rollups
// (reports_conversations_rollup.go) or, as fallback and oracle, from raw
// events.
func (s *Store) ListConversations(ctx context.Context, filter domain.ConversationFilter) (domain.ConversationsResponse, error) {
	if filter.Limit < 1 || filter.Limit > 1000 || filter.Offset < 0 {
		return domain.ConversationsResponse{Conversations: []domain.ConversationEntry{}}, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.ConversationsResponse, error) { return r.listConversations(ctx, filter) },
		func(tx *sql.Tx) (domain.ConversationsResponse, error) { return listConversationsRaw(ctx, tx, filter) })
}

func listConversationsRaw(ctx context.Context, q analyticsQuerier, filter domain.ConversationFilter) (domain.ConversationsResponse, error) {
	result := domain.ConversationsResponse{Conversations: []domain.ConversationEntry{}}
	where, args := conversationWhere(filter)
	if err := q.QueryRowContext(ctx, `SELECT count(DISTINCT trim(e.conversation_id))
 FROM usage_events e WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `SELECT trim(e.conversation_id),min(e.requested_at),max(e.requested_at),
 count(*),count(DISTINCT e.account_id),count(DISTINCT e.model),
 coalesce(sum(e.input_tokens+e.output_tokens),0),coalesce(sum(e.cached_input_tokens),0),
 coalesce(sum(e.cost_microdollars),0)
 FROM usage_events e WHERE `+where+`
 GROUP BY trim(e.conversation_id) ORDER BY max(e.requested_at) DESC,1 LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), filter.Limit, filter.Offset)...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var entry domain.ConversationEntry
		var first, last, cached, cost, accounts, models int64
		if err := rows.Scan(&entry.ConversationID, &first, &last, &entry.RequestCount,
			&accounts, &models, &entry.TotalTokens, &cached, &cost); err != nil {
			rows.Close()
			return result, err
		}
		entry.FirstRequest, entry.LastRequest = fromMillis(first), fromMillis(last)
		entry.CachedInputTokens = &cached
		entry.TotalCostUSD = float64(cost) / 1e6
		entry.RemainingAccountCount = max(0, accounts-1)
		entry.RemainingModelCount = max(0, models-1)
		result.Conversations = append(result.Conversations, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for index := range result.Conversations {
		entry := &result.Conversations[index]
		var account, key, name, model sql.NullString
		err := q.QueryRowContext(ctx, `SELECT e.account_id,e.api_key_id,k.name,e.model
 FROM usage_events e LEFT JOIN api_keys k ON k.id=e.api_key_id
 WHERE trim(e.conversation_id)=? AND e.request_kind NOT IN ('warmup','limit_warmup')
 AND e.legacy_deleted=0 ORDER BY e.requested_at DESC,e.request_id DESC LIMIT 1`,
			entry.ConversationID).Scan(&account, &key, &name, &model)
		if err != nil {
			return result, err
		}
		entry.RepresentativeAccount = optionalString(account)
		if name.Valid {
			entry.APIKeyID = optionalString(key)
			entry.APIKeyName = optionalString(name)
		}
		entry.RepresentativeModel = optionalString(model)
	}
	result.HasMore = int64(filter.Offset+len(result.Conversations)) < result.Total
	return result, nil
}

// ConversationDetails summarizes one conversation from rollups
// (reports_conversations_rollup.go) or, as fallback and oracle, from raw
// events.
func (s *Store) ConversationDetails(ctx context.Context, id string) (domain.ConversationDetails, error) {
	if strings.TrimSpace(id) == "" {
		return domain.ConversationDetails{}, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.ConversationDetails, error) { return r.conversationDetails(ctx, id) },
		func(tx *sql.Tx) (domain.ConversationDetails, error) { return conversationDetailsRaw(ctx, tx, id) })
}

func conversationDetailsRaw(ctx context.Context, q analyticsQuerier, id string) (domain.ConversationDetails, error) {
	var result domain.ConversationDetails
	var first, last sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT min(e.requested_at),max(e.requested_at),
 count(DISTINCT e.account_id),coalesce(sum(e.total_latency_ms),0)
 FROM usage_events e WHERE trim(e.conversation_id)=?
 AND e.request_kind NOT IN ('warmup','limit_warmup') AND e.legacy_deleted=0`, id).
		Scan(&first, &last, &result.AccountCount, &result.TotalElapsedTime)
	if err != nil {
		return result, err
	}
	if !first.Valid || !last.Valid {
		return result, ErrNotFound
	}
	result.ConversationID = id
	result.Start, result.Latest = fromMillis(first.Int64), fromMillis(last.Int64)
	var group string
	err = q.QueryRowContext(ctx, `SELECT e.useragent_group FROM usage_events e
 WHERE trim(e.conversation_id)=? AND e.useragent_group IS NOT NULL
 AND e.request_kind NOT IN ('warmup','limit_warmup') AND e.legacy_deleted=0
 GROUP BY e.useragent_group ORDER BY count(*) DESC,e.useragent_group LIMIT 1`, id).Scan(&group)
	if err == nil {
		result.DominantUserAgentGroup = &group
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `SELECT e.model,e.reasoning_effort,count(*),
 coalesce(sum(e.total_latency_ms),0),coalesce(sum(e.input_tokens),0),
 coalesce(sum(e.cached_input_tokens),0),coalesce(sum(e.output_tokens),0),
 coalesce(sum(e.cost_microdollars),0)
 FROM usage_events e WHERE trim(e.conversation_id)=?
 AND e.request_kind NOT IN ('warmup','limit_warmup') AND e.legacy_deleted=0
 GROUP BY e.model,e.reasoning_effort ORDER BY sum(e.cost_microdollars) DESC,e.model,e.reasoning_effort LIMIT 2000`, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.ModelStats = []domain.ConversationModelStat{}
	for rows.Next() {
		var item domain.ConversationModelStat
		var effort sql.NullString
		var cached, cost int64
		if err := rows.Scan(&item.ModelEffort.Model, &effort, &item.Requests,
			&item.TotalElapsedTime, &item.TotalInputTokens, &cached, &item.TotalOutputTokens, &cost); err != nil {
			return result, err
		}
		item.ModelEffort.ReasoningEffort = optionalString(effort)
		item.CachedInputTokens = &cached
		item.TotalCostUSD = float64(cost) / 1e6
		result.ModelStats = append(result.ModelStats, item)
	}
	return result, rows.Err()
}
