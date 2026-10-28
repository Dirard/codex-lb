package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

// KeyTrendBuckets returns the hourly tokens and cost of one key from rollups
// (reports_key_rollup.go) or, as fallback and oracle, from raw events.
func (s *Store) KeyTrendBuckets(ctx context.Context, keyID string, since, until time.Time) ([]domain.APIKeyTrendBucket, error) {
	if keyID == "" || !since.Before(until) || until.Sub(since) > 8*24*time.Hour {
		return nil, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) ([]domain.APIKeyTrendBucket, error) {
		return r.keyTrendBuckets(ctx, keyID, since, until)
	}, func(tx *sql.Tx) ([]domain.APIKeyTrendBucket, error) {
		return keyTrendBucketsRaw(ctx, tx, keyID, since, until)
	})
}

func keyTrendBucketsRaw(ctx context.Context, q analyticsQuerier, keyID string, since, until time.Time) ([]domain.APIKeyTrendBucket, error) {
	facts, args := reportFacts(domain.ReportFilter{APIKeyIDs: []string{keyID}}, since, until)
	hours, err := keyTrendFacts(ctx, q, facts, args)
	if err != nil {
		return nil, err
	}
	result := []domain.APIKeyTrendBucket{}
	for _, hour := range hours {
		result = append(result, domain.APIKeyTrendBucket{At: fromMillis(hour.at), Tokens: hour.tokens,
			CostUSD: reportCostUSD(hour.legacyCost, hour.micro)})
	}
	return result, nil
}

// keyTrendHour is one hour of key facts: folded REAL costs summed by SQLite
// and raw microdollars apart.
type keyTrendHour struct {
	at, tokens, micro int64
	legacyCost        float64
}

// keyTrendFacts sums facts by hour in hour order.
func keyTrendFacts(ctx context.Context, q analyticsQuerier, facts string, args []any) ([]keyTrendHour, error) {
	rows, err := q.QueryContext(ctx, facts+`SELECT (at_ms/3600000)*3600000,
 coalesce(sum(input_tokens+output_tokens),0),coalesce(sum(cost_usd),0),coalesce(sum(cost_micro),0)
 FROM facts GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hours []keyTrendHour
	for rows.Next() {
		var hour keyTrendHour
		if err := rows.Scan(&hour.at, &hour.tokens, &hour.legacyCost, &hour.micro); err != nil {
			return nil, err
		}
		hours = append(hours, hour)
	}
	return hours, rows.Err()
}

// KeyUsage7Day returns one key's usage and cost by account from rollups
// (reports_key_rollup.go) or, as fallback and oracle, from raw events.
func (s *Store) KeyUsage7Day(ctx context.Context, keyID string, since, until time.Time) (domain.APIKeyUsage7DayResponse, error) {
	if keyID == "" || !since.Before(until) || until.Sub(since) > 8*24*time.Hour {
		return domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.APIKeyUsage7DayResponse, error) {
		return r.keyUsage7Day(ctx, keyID, since, until)
	}, func(tx *sql.Tx) (domain.APIKeyUsage7DayResponse, error) {
		return keyUsage7DayRaw(ctx, tx, keyID, since, until)
	})
}

func keyUsage7DayRaw(ctx context.Context, q analyticsQuerier, keyID string, since, until time.Time) (domain.APIKeyUsage7DayResponse, error) {
	result := domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}
	filter := domain.ReportFilter{APIKeyIDs: []string{keyID}}
	usage, err := aggregateReportWindow(ctx, q, filter, since, until)
	if err != nil {
		return result, err
	}
	rWhere, rArgs := rawReportWhere(filter, since, until)
	groups, err := keyAccountGroups(ctx, q, filter, since, until, ` UNION ALL
 SELECT CASE WHEN e.account_id IS NULL THEN char(31)
 WHEN substr(e.account_id,1,1)=char(31) THEN char(31)||e.account_id ELSE e.account_id END,
 e.legacy_deleted,NULL,e.cost_microdollars FROM usage_events e WHERE `+rWhere, rArgs)
	if err != nil {
		return result, err
	}
	return keyUsageResponse(keyID, usage, groups), nil
}

// keyAccountGroup is one (account, deleted) group of a key's costs.
type keyAccountGroup struct {
	account    string
	email      sql.NullString
	deleted    bool
	legacyCost float64
	micro      int64
}

// keyAccountGroups sums a key's folded hours, plus the facts of rawArm, by
// account and deleted flag in the order of the GROUP BY.
func keyAccountGroups(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, since, until time.Time, rawArm string, rawArgs []any) ([]keyAccountGroup, error) {
	hWhere, hArgs := hourlyReportWhere(filter, since, until)
	query := `WITH account_facts AS (
 SELECT h.account_id,h.is_deleted,h.cost_usd,0 AS cost_micro FROM legacy_hourly_usage h WHERE ` + hWhere + rawArm + `
 ) SELECT f.account_id,a.email,f.is_deleted,coalesce(sum(f.cost_usd),0),coalesce(sum(f.cost_micro),0)
 FROM account_facts f LEFT JOIN accounts a ON a.id=CASE
 WHEN f.account_id=char(31) THEN NULL
 WHEN substr(f.account_id,1,1)=char(31) THEN substr(f.account_id,2)
 ELSE f.account_id END
 GROUP BY f.account_id,a.email,f.is_deleted`
	rows, err := q.QueryContext(ctx, query, append(hArgs, rawArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []keyAccountGroup
	for rows.Next() {
		var group keyAccountGroup
		if err := rows.Scan(&group.account, &group.email, &group.deleted, &group.legacyCost, &group.micro); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// keyUsageResponse assembles the usage of a key from its window totals and
// its account groups in GROUP BY order.
func keyUsageResponse(keyID string, usage reportAggregate, groups []keyAccountGroup) domain.APIKeyUsage7DayResponse {
	result := domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}
	result.TotalRequests = usage.requests
	result.TotalTokens = usage.input + usage.output
	result.CachedInputTokens = max(0, min(usage.cached, usage.input))
	result.TotalCostUSD = roundReport(usage.costUSD, 6)
	deletedCost := 0.0
	for _, group := range groups {
		cost := roundReport(reportCostUSD(group.legacyCost, group.micro), 6)
		if cost <= 0 {
			continue
		}
		if group.deleted {
			deletedCost += cost
			continue
		}
		entry := domain.APIKeyAccountCost{CostUSD: cost}
		if group.account != "\x1f" {
			id := fromLegacyDimension(group.account)
			entry.AccountID = &id
		}
		if group.email.Valid {
			email := group.email.String
			entry.Email = &email
		}
		result.AccountCosts = append(result.AccountCosts, entry)
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
	return result
}

func accountCostID(item domain.APIKeyAccountCost) string {
	if item.AccountID == nil {
		return ""
	}
	return *item.AccountID
}
