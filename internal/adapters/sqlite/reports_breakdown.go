package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) reportByModel(ctx context.Context, filter domain.ReportFilter) ([]domain.ModelCostEntry, error) {
	facts, args := reportFacts(filter, filter.Start, filter.End)
	rows, err := s.readDB.QueryContext(ctx, facts+`SELECT model,coalesce(sum(cost_usd),0),
 coalesce(sum(request_count),0),coalesce(sum(sum(cost_usd)) OVER (),0)
 FROM facts GROUP BY model ORDER BY sum(cost_usd) DESC,model LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ModelCostEntry, 0)
	for rows.Next() {
		var entry domain.ModelCostEntry
		var total float64
		if err := rows.Scan(&entry.Model, &entry.CostUSD, &entry.Requests, &total); err != nil {
			return nil, err
		}
		if total > 0 {
			entry.Percentage = roundReport(entry.CostUSD/total*100, 1)
		}
		entry.CostUSD = roundReport(entry.CostUSD, 4)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) reportByAccount(ctx context.Context, filter domain.ReportFilter) ([]domain.AccountCostEntry, error) {
	facts, args := reportFacts(filter, filter.Start, filter.End)
	rows, err := s.readDB.QueryContext(ctx, facts+`SELECT f.account_id,nullif(a.alias,''),
 coalesce(sum(f.cost_usd),0),coalesce(sum(f.request_count),0)
 FROM facts f LEFT JOIN accounts a ON a.id=f.account_id
 GROUP BY f.account_id,a.alias ORDER BY sum(f.cost_usd) DESC,f.account_id LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.AccountCostEntry, 0)
	for rows.Next() {
		var entry domain.AccountCostEntry
		var accountID string
		var alias sql.NullString
		if err := rows.Scan(&accountID, &alias, &entry.CostUSD, &entry.Requests); err != nil {
			return nil, err
		}
		if accountID != "\x1f" {
			id := fromLegacyDimension(accountID)
			entry.AccountID = &id
		}
		if alias.Valid {
			entry.Alias = &alias.String
		}
		entry.CostUSD = roundReport(entry.CostUSD, 4)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) reportByUserAgent(ctx context.Context, filter domain.ReportFilter) ([]domain.UserAgentCostEntry, error) {
	where, args := rawReportWhere(filter, filter.Start, filter.End)
	rows, err := s.readDB.QueryContext(ctx, `SELECT coalesce(nullif(e.useragent_group,''),'Missing User-Agent'),
 coalesce(sum(e.cost_microdollars),0)/1000000.0,count(*)
 FROM usage_events e WHERE `+where+` GROUP BY coalesce(nullif(e.useragent_group,''),'Missing User-Agent')
 ORDER BY sum(e.cost_microdollars) DESC,1 LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	result := make([]domain.UserAgentCostEntry, 0)
	for rows.Next() {
		var entry domain.UserAgentCostEntry
		if err := rows.Scan(&entry.UserAgent, &entry.CostUSD, &entry.Requests); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	hourlyWhere, hourlyArgs := hourlyReportWhere(filter, filter.Start, filter.End)
	var foldedCost float64
	var foldedRequests int64
	if err := s.readDB.QueryRowContext(ctx, `SELECT coalesce(sum(h.cost_usd),0),coalesce(sum(h.request_count),0)
 FROM legacy_hourly_usage h WHERE `+hourlyWhere, hourlyArgs...).Scan(&foldedCost, &foldedRequests); err != nil {
		return nil, err
	}
	if foldedRequests > 0 {
		result = append(result, domain.UserAgentCostEntry{
			UserAgent: "Historical (unattributed)", CostUSD: foldedCost, Requests: foldedRequests})
	}
	total := 0.0
	for _, entry := range result {
		total += entry.CostUSD
	}
	for i := range result {
		if total > 0 {
			result[i].Percentage = roundReport(result[i].CostUSD/total*100, 1)
		}
		result[i].CostUSD = roundReport(result[i].CostUSD, 4)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CostUSD != result[j].CostUSD {
			return result[i].CostUSD > result[j].CostUSD
		}
		return result[i].UserAgent < result[j].UserAgent
	})
	return result, nil
}

func (s *Store) countReportConversations(ctx context.Context, filter domain.ReportFilter, start, end time.Time) (int64, error) {
	rawWhere, rawArgs := rawReportWhere(filter, start, end)
	query := `SELECT count(DISTINCT conversation_id) FROM (`
	args := make([]any, 0)
	if filter.Model == "" && filter.UserAgentGroup == "" && len(filter.APIKeyIDs) == 0 {
		query += `SELECT c.conversation_id FROM legacy_conversation_hourly c
 WHERE c.bucket_epoch>=? AND c.bucket_epoch<?`
		args = append(args, start.Unix(), end.Unix())
		if len(filter.AccountIDs) > 0 {
			ids := make([]string, len(filter.AccountIDs))
			for i, id := range filter.AccountIDs {
				ids[i] = toLegacyDimension(id)
			}
			var parts []string
			parts, args = appendInCondition(parts, args, "c.account_id", ids)
			query += " AND " + parts[0]
		}
		query += ` UNION ALL `
	}
	query += `SELECT trim(e.conversation_id) AS conversation_id FROM usage_events e WHERE ` + rawWhere + `
 AND e.conversation_id IS NOT NULL AND trim(e.conversation_id)!='')`
	args = append(args, rawArgs...)
	var count int64
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
