package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) KeyTrendBuckets(ctx context.Context, keyID string, since, until time.Time) ([]domain.APIKeyTrendBucket, error) {
	if keyID == "" || !since.Before(until) || until.Sub(since) > 8*24*time.Hour {
		return nil, ErrInvalid
	}
	filter := domain.ReportFilter{APIKeyIDs: []string{keyID}}
	facts, args := reportFacts(filter, since, until)
	rows, err := s.readDB.QueryContext(ctx, facts+`SELECT (at_ms/3600000)*3600000,
 coalesce(sum(input_tokens+output_tokens),0),coalesce(sum(cost_usd),0)
 FROM facts GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.APIKeyTrendBucket{}
	for rows.Next() {
		var bucket domain.APIKeyTrendBucket
		var at int64
		if err := rows.Scan(&at, &bucket.Tokens, &bucket.CostUSD); err != nil {
			return nil, err
		}
		bucket.At = fromMillis(at)
		result = append(result, bucket)
	}
	return result, rows.Err()
}

func (s *Store) KeyUsage7Day(ctx context.Context, keyID string, since, until time.Time) (domain.APIKeyUsage7DayResponse, error) {
	result := domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}
	if keyID == "" || !since.Before(until) || until.Sub(since) > 8*24*time.Hour {
		return result, ErrInvalid
	}
	filter := domain.ReportFilter{APIKeyIDs: []string{keyID}}
	usage, err := s.aggregateReportWindow(ctx, filter, since, until)
	if err != nil {
		return result, err
	}
	result.TotalRequests = usage.requests
	result.TotalTokens = usage.input + usage.output
	result.CachedInputTokens = max(0, min(usage.cached, usage.input))
	result.TotalCostUSD = roundReport(usage.costUSD, 6)
	hWhere, hArgs := hourlyReportWhere(filter, since, until)
	rWhere, rArgs := rawReportWhere(filter, since, until)
	query := `WITH account_facts AS (
 SELECT h.account_id,h.is_deleted,h.cost_usd FROM legacy_hourly_usage h WHERE ` + hWhere + `
 UNION ALL
 SELECT CASE WHEN e.account_id IS NULL THEN char(31)
 WHEN substr(e.account_id,1,1)=char(31) THEN char(31)||e.account_id ELSE e.account_id END,
 e.legacy_deleted,e.cost_microdollars/1000000.0 FROM usage_events e WHERE ` + rWhere + `
 ) SELECT f.account_id,a.email,f.is_deleted,coalesce(sum(f.cost_usd),0)
 FROM account_facts f LEFT JOIN accounts a ON a.id=CASE
 WHEN f.account_id=char(31) THEN NULL
 WHEN substr(f.account_id,1,1)=char(31) THEN substr(f.account_id,2)
 ELSE f.account_id END
 GROUP BY f.account_id,a.email,f.is_deleted`
	rows, err := s.readDB.QueryContext(ctx, query, append(hArgs, rArgs...)...)
	if err != nil {
		return result, err
	}
	deletedCost := 0.0
	for rows.Next() {
		var accountID string
		var email sql.NullString
		var deleted bool
		var cost float64
		if err := rows.Scan(&accountID, &email, &deleted, &cost); err != nil {
			rows.Close()
			return result, err
		}
		cost = roundReport(cost, 6)
		if cost <= 0 {
			continue
		}
		if deleted {
			deletedCost += cost
			continue
		}
		entry := domain.APIKeyAccountCost{CostUSD: cost}
		if accountID != "\x1f" {
			id := fromLegacyDimension(accountID)
			entry.AccountID = &id
		}
		if email.Valid {
			entry.Email = &email.String
		}
		result.AccountCosts = append(result.AccountCosts, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if deletedCost > 0 {
		result.AccountCosts = append(result.AccountCosts, domain.APIKeyAccountCost{
			CostUSD: roundReport(deletedCost, 6), IsDeleted: true})
	}
	sort.Slice(result.AccountCosts, func(i, j int) bool {
		if result.AccountCosts[i].CostUSD != result.AccountCosts[j].CostUSD {
			return result.AccountCosts[i].CostUSD > result.AccountCosts[j].CostUSD
		}
		return accountCostID(result.AccountCosts[i]) < accountCostID(result.AccountCosts[j])
	})
	return result, nil
}

func accountCostID(item domain.APIKeyAccountCost) string {
	if item.AccountID == nil {
		return ""
	}
	return *item.AccountID
}
