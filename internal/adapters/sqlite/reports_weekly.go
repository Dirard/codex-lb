package sqlite

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// PositiveQuotaDeltas aggregates seven-day demand, including the last sample
// before the boundary without treating a reset/drop as new consumption.
func (s *Store) PositiveQuotaDeltas(ctx context.Context, since, until time.Time) (map[string]map[string]float64, error) {
	if !since.Before(until) || until.Sub(since) > 7*24*time.Hour {
		return nil, ErrInvalid
	}
	rows, err := s.readDB.QueryContext(ctx, `WITH baseline AS (
 SELECT (SELECT h.id FROM account_quota_history h WHERE h.account_id=q.account_id
 AND h.window=q.window AND h.observed_at<? ORDER BY h.observed_at DESC,h.id DESC LIMIT 1) AS id
 FROM account_quotas q
 ), samples AS (
 SELECT id,account_id,window,observed_at,used_percent FROM account_quota_history
 WHERE observed_at>=? AND observed_at<=?
 UNION ALL
 SELECT h.id,h.account_id,h.window,h.observed_at,h.used_percent
 FROM baseline b JOIN account_quota_history h ON h.id=b.id
 ), deltas AS (
 SELECT account_id,window,observed_at,used_percent-lag(used_percent) OVER
 (PARTITION BY account_id,window ORDER BY observed_at,id) AS delta FROM samples
 ) SELECT account_id,window,sum(CASE WHEN observed_at>=? AND delta>0 THEN delta ELSE 0 END)
 FROM deltas GROUP BY account_id,window`, millis(since), millis(since), millis(until), millis(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]map[string]float64)
	for rows.Next() {
		var account, window string
		var demand float64
		if err := rows.Scan(&account, &window, &demand); err != nil {
			return nil, err
		}
		if result[account] == nil {
			result[account] = make(map[string]float64)
		}
		result[account][window] = demand
	}
	return result, rows.Err()
}

// WeeklyPaceTopKeys returns the union of the three highest-request and highest-
// token consumers. Shared report facts preserve imported totals without doubles.
func (s *Store) WeeklyPaceTopKeys(ctx context.Context, since, until time.Time) ([]domain.WeeklyCreditAPIKeyAttribution, error) {
	if !since.Before(until) || until.Sub(since) > 2*time.Hour {
		return nil, ErrInvalid
	}
	facts, args := reportFacts(domain.ReportFilter{}, since, until)
	rows, err := s.readDB.QueryContext(ctx, facts+`, models AS (
 SELECT api_key_id,model,sum(request_count) AS requests,
 sum(input_tokens+output_tokens) AS tokens,sum(cached_input_tokens) AS cached
 FROM facts GROUP BY api_key_id,model
 ), ranked_models AS (
 SELECT *,row_number() OVER (PARTITION BY api_key_id ORDER BY requests DESC,tokens DESC,model) AS rank FROM models
 ), totals AS (
 SELECT api_key_id,sum(requests) AS requests,sum(tokens) AS tokens,sum(cached) AS cached,
 max(CASE WHEN rank=1 THEN model END) AS model FROM ranked_models GROUP BY api_key_id
 ), ranked_keys AS (
 SELECT *,row_number() OVER (ORDER BY requests DESC,tokens DESC,api_key_id) AS request_rank,
 row_number() OVER (ORDER BY tokens DESC,requests DESC,api_key_id) AS token_rank FROM totals
 ) SELECT r.api_key_id,coalesce(nullif(k.name,''),'(unnamed)'),r.requests,r.tokens,r.cached,r.model
 FROM ranked_keys r LEFT JOIN api_keys k ON k.id=CASE WHEN substr(r.api_key_id,1,1)=char(31)
 THEN substr(r.api_key_id,2) ELSE r.api_key_id END
 WHERE r.request_rank<=3 OR r.token_rank<=3 ORDER BY r.requests DESC,r.tokens DESC,r.api_key_id LIMIT 6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.WeeklyCreditAPIKeyAttribution{}
	for rows.Next() {
		var entry domain.WeeklyCreditAPIKeyAttribution
		var keyID string
		if err := rows.Scan(&keyID, &entry.Name, &entry.Requests, &entry.BillableTokens, &entry.CachedTokens, &entry.DominantModel); err != nil {
			return nil, err
		}
		if keyID != "\x1f" {
			id := fromLegacyDimension(keyID)
			entry.APIKeyID = &id
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
