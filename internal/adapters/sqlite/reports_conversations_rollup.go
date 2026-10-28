package sqlite

import (
	"context"
	"math"
	"sort"
	"strings"

	"codex-lb/internal/domain"
)

// rollupConversationSum accumulates one conversation of the list: counts by
// account and model for the distinct counts, and the dimensions of its latest
// event, which the raw path reads as the representative row.
type rollupConversationSum struct {
	requests, tokens, cached, cost int64
	first, last                    int64
	lastRequest                    string
	account, key, model            string
	accounts, models               map[string]int64
}

func newRollupConversationSum() *rollupConversationSum {
	return &rollupConversationSum{first: math.MaxInt64, last: math.MinInt64, accounts: map[string]int64{}, models: map[string]int64{}}
}

func (c *rollupConversationSum) add(requests, tokens, cached, cost int64, account, model string) {
	c.requests += requests
	c.tokens += tokens
	c.cached += cached
	c.cost += cost
	c.accounts[account] += requests
	c.models[model] += requests
}

// observe extends first and last; the latest event orders by time, then
// request id, like the raw representative query.
func (c *rollupConversationSum) observe(first, last int64, request, account, key, model string) {
	c.first = min(c.first, first)
	if last > c.last || last == c.last && request > c.lastRequest {
		c.last, c.lastRequest, c.account, c.key, c.model = last, request, account, key, model
	}
}

// countPositive counts the keys with a positive count, NULL dimensions
// (\x1f) excluded when nulls is false.
func countPositive(counts map[string]int64, nulls bool) int64 {
	n := int64(0)
	for key, count := range counts {
		if count > 0 && (nulls || key != "\x1f") {
			n++
		}
	}
	return n
}

// listConversations is the rollup reader of ListConversations (design §4.2):
// conversation-hours from the first whole hour after since, images for the
// partial hour, the tail and the journal, aggregated by conversation in Go,
// then one key-name lookup for the page. A pending removal of a conversation
// event is declined: first, last and the representative event of a
// conversation-hour cannot be subtracted until the folder recomputes them.
func (r *rollupRead) listConversations(ctx context.Context, filter domain.ConversationFilter) (domain.ConversationsResponse, error) {
	result := domain.ConversationsResponse{Conversations: []domain.ConversationEntry{}}
	// LIKE on the trimmed id equals LIKE on the stored id only for a term
	// without leading or trailing spaces.
	if strings.Trim(filter.Search, " ") != filter.Search {
		return result, errRollupDeclined
	}
	start := int64(math.MinInt64)
	if !filter.Since.IsZero() {
		start = millis(filter.Since)
	}
	plan := planRollupWindow(start, math.MaxInt64, nil)
	where, args := conversationWhere(domain.ConversationFilter{Search: filter.Search})
	conversations := map[string]*rollupConversationSum{}
	at := func(id string) *rollupConversationSum {
		sum := conversations[id]
		if sum == nil {
			sum = newRollupConversationSum()
			conversations[id] = sum
		}
		return sum
	}
	removal := false
	if err := r.images(ctx, plan, where, args, func(img *rollupImage, sign int64) {
		sum := at(img.conversation)
		switch {
		case sign < 0:
			removal = true
		case sign == 0:
			sum.cost += img.cost
		default:
			account, key := rawReportDimension(img.account), rawReportDimension(img.key)
			sum.add(1, img.input+img.output, img.cached, img.cost, account, img.model)
			sum.observe(img.at, img.at, img.requestID, account, key, img.model)
		}
	}); err != nil {
		return result, err
	}
	if removal {
		return result, errRollupDeclined
	}
	if from, to, ok := plan.hours(); ok {
		query := `SELECT conversation_id,account_dim,api_key_dim,model,requests,input_tokens+output_tokens,
 cached_input_tokens,cost_microdollars,first_at,last_at,last_request_id FROM usage_rollup_conversation_hour
 WHERE bucket_epoch>=? AND bucket_epoch<? AND deleted=0`
		queryArgs := []any{from, to}
		if filter.Search != "" {
			query += ` AND conversation_id LIKE ? ESCAPE '\'`
			queryArgs = append(queryArgs, "%"+escapeLike(filter.Search)+"%")
		}
		rows, err := r.tx.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return result, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, account, key, model, lastRequest string
			var requests, tokens, cached, cost, first, last int64
			if err := rows.Scan(&id, &account, &key, &model, &requests, &tokens, &cached, &cost, &first, &last, &lastRequest); err != nil {
				return result, err
			}
			sum := at(id)
			sum.add(requests, tokens, cached, cost, account, model)
			sum.observe(first, last, lastRequest, account, key, model)
		}
		if err := rows.Err(); err != nil {
			return result, err
		}
	}
	ids := make([]string, 0, len(conversations))
	for id, sum := range conversations {
		if sum.requests > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := conversations[ids[i]], conversations[ids[j]]
		if a.last != b.last {
			return a.last > b.last
		}
		return ids[i] < ids[j]
	})
	result.Total = int64(len(ids))
	page := ids[min(filter.Offset, len(ids)):]
	page = page[:min(filter.Limit, len(page))]
	var keys []string
	for _, id := range page {
		if key := decodeRollupDim(conversations[id].key); key.Valid {
			keys = append(keys, key.String)
		}
	}
	names, err := r.keyNames(ctx, keys)
	if err != nil {
		return result, err
	}
	for _, id := range page {
		sum := conversations[id]
		cached := sum.cached
		entry := domain.ConversationEntry{ConversationID: id, FirstRequest: fromMillis(sum.first),
			LastRequest: fromMillis(sum.last), RequestCount: sum.requests,
			RepresentativeAccount: optionalString(decodeRollupDim(sum.account)),
			RemainingAccountCount: max(0, countPositive(sum.accounts, false)-1),
			RepresentativeModel:   &sum.model, RemainingModelCount: max(0, countPositive(sum.models, true)-1),
			TotalTokens: sum.tokens, CachedInputTokens: &cached, TotalCostUSD: float64(sum.cost) / 1e6}
		if key := decodeRollupDim(sum.key); key.Valid {
			if name, ok := names[key.String]; ok {
				keyID, keyName := key.String, name
				entry.APIKeyID, entry.APIKeyName = &keyID, &keyName
			}
		}
		result.Conversations = append(result.Conversations, entry)
	}
	result.HasMore = int64(filter.Offset+len(result.Conversations)) < result.Total
	return result, nil
}

// keyNames looks up the names of key ids.
func (r *rollupRead) keyNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT id,name FROM api_keys WHERE id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, anySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// conversationModelKey is a model and effort dimension of a conversation.
type conversationModelKey struct{ model, effort string }

// conversationDetails is the rollup reader of ConversationDetails (design
// §4.2): the conversation's rows through idx_usage_rollup_conversation_id
// plus its tail and journal images. A pending removal is declined like in
// the list.
func (r *rollupRead) conversationDetails(ctx context.Context, id string) (domain.ConversationDetails, error) {
	var result domain.ConversationDetails
	type stat struct{ requests, latency, input, cached, output, cost int64 }
	var requests, latency int64
	first, last := int64(math.MaxInt64), int64(math.MinInt64)
	accounts, agents := map[string]int64{}, map[string]int64{}
	stats := map[conversationModelKey]*stat{}
	add := func(account, model, effort, agent string, s stat, from, to int64) {
		requests += s.requests
		latency += s.latency
		accounts[account] += s.requests
		agents[agent] += s.requests
		key := conversationModelKey{model, effort}
		if stats[key] == nil {
			stats[key] = &stat{}
		}
		sum := stats[key]
		sum.requests += s.requests
		sum.latency += s.latency
		sum.input += s.input
		sum.cached += s.cached
		sum.output += s.output
		sum.cost += s.cost
		if s.requests > 0 {
			first, last = min(first, from), max(last, to)
		}
	}
	removal := false
	if err := r.images(ctx, rollupEverything, rollupConversationSQL("e")+` AND e.legacy_deleted=0 AND trim(e.conversation_id)=?`,
		[]any{id}, func(img *rollupImage, sign int64) {
			switch {
			case sign < 0:
				removal = true
			case sign == 0:
				add(rawReportDimension(img.account), img.model, rawReportDimension(img.effort), rawReportDimension(img.userAgent),
					stat{cost: img.cost}, 0, 0)
			default:
				add(rawReportDimension(img.account), img.model, rawReportDimension(img.effort), rawReportDimension(img.userAgent),
					stat{1, img.total, img.input, img.cached, img.output, img.cost}, img.at, img.at)
			}
		}); err != nil {
		return result, err
	}
	if removal {
		return result, errRollupDeclined
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT account_dim,model,effort_dim,ua_dim,requests,total_latency_ms,input_tokens,
 cached_input_tokens,output_tokens,cost_microdollars,first_at,last_at
 FROM usage_rollup_conversation_hour INDEXED BY idx_usage_rollup_conversation_id WHERE conversation_id=? AND deleted=0`, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var account, model, effort, agent string
		var s stat
		var from, to int64
		if err := rows.Scan(&account, &model, &effort, &agent, &s.requests, &s.latency, &s.input, &s.cached, &s.output,
			&s.cost, &from, &to); err != nil {
			return result, err
		}
		add(account, model, effort, agent, s, from, to)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if requests <= 0 {
		return result, ErrNotFound
	}
	result.ConversationID = id
	result.Start, result.Latest = fromMillis(first), fromMillis(last)
	result.AccountCount = countPositive(accounts, false)
	result.TotalElapsedTime = latency
	var dominant string
	var dominantCount int64
	for dim, n := range agents {
		agent := decodeRollupDim(dim)
		if n <= 0 || !agent.Valid {
			continue
		}
		if n > dominantCount || n == dominantCount && agent.String < dominant {
			dominant, dominantCount = agent.String, n
		}
	}
	if dominantCount > 0 {
		result.DominantUserAgentGroup = &dominant
	}
	keys := make([]conversationModelKey, 0, len(stats))
	for key, s := range stats {
		if s.requests > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := stats[keys[i]], stats[keys[j]]
		if a.cost != b.cost {
			return a.cost > b.cost
		}
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		ei, ej := decodeRollupDim(keys[i].effort), decodeRollupDim(keys[j].effort)
		if !ei.Valid || !ej.Valid {
			return !ei.Valid && ej.Valid
		}
		return ei.String < ej.String
	})
	result.ModelStats = []domain.ConversationModelStat{}
	for _, key := range keys[:min(len(keys), 2000)] {
		s := stats[key]
		var item domain.ConversationModelStat
		item.ModelEffort.Model = key.model
		item.ModelEffort.ReasoningEffort = optionalString(decodeRollupDim(key.effort))
		cached := s.cached
		item.Requests, item.TotalElapsedTime, item.TotalInputTokens = s.requests, s.latency, s.input
		item.CachedInputTokens, item.TotalOutputTokens, item.TotalCostUSD = &cached, s.output, float64(s.cost)/1e6
		result.ModelStats = append(result.ModelStats, item)
	}
	return result, nil
}
