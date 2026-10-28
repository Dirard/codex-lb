package sqlite

// Reference implementation of the admin report before the single-pass rewrite
// (one statement per measure and day). Tests compare the new QueryReports with
// it; it is not used by production code.

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) referenceQueryReports(ctx context.Context, filter domain.ReportFilter) (domain.ReportsResponse, error) {
	var response domain.ReportsResponse
	response.Daily = []domain.DailyReportRow{}
	response.ByModel = []domain.ModelCostEntry{}
	response.ByUserAgent = []domain.UserAgentCostEntry{}
	response.ByAccount = []domain.AccountCostEntry{}
	if err := checkedReportWindow(filter.Start, filter.End); err != nil {
		return response, err
	}
	if filter.Location == nil {
		filter.Location = time.UTC
	}
	current, err := s.aggregateReportWindow(ctx, filter, filter.Start, filter.End)
	if err != nil {
		return response, err
	}
	previous, err := s.aggregateReportWindow(ctx, filter, filter.PreviousStart, filter.Start)
	if err != nil {
		return response, err
	}
	conversations, err := s.countReportConversations(ctx, filter, filter.Start, filter.End)
	if err != nil {
		return response, err
	}
	response.Summary = domain.ReportSummary{TotalCostUSD: roundReport(current.costUSD, 4),
		TotalInputTokens: current.input, TotalOutputTokens: current.output,
		TotalReasoningTokens: current.reasoning, ReasoningUsageKnownRequests: current.known,
		TotalCachedTokens: current.cached, TotalRequests: current.requests,
		TotalCancelled: current.cancelled, TotalErrors: current.errors,
		TotalConversations: conversations, ActiveAccounts: current.accounts}
	response.Comparison.Previous.TotalCostUSD = roundReport(previous.costUSD, 4)
	response.Comparison.Previous.TotalTokens = previous.input + previous.output
	response.Comparison.Previous.TotalRequests = previous.requests
	response.Comparison.CanCompare, err = s.reportHasActivityBefore(ctx, filter, filter.PreviousStart)
	if err != nil {
		return response, err
	}
	startDay := filter.Start.In(filter.Location)
	endDay := filter.End.In(filter.Location)
	for day := startDay; day.Before(endDay); day = day.AddDate(0, 0, 1) {
		end := day.AddDate(0, 0, 1)
		if end.After(endDay) {
			end = endDay
		}
		row, err := s.referenceDailyReport(ctx, filter, day.UTC(), end.UTC(), day.Format("2006-01-02"))
		if err != nil {
			return response, err
		}
		response.Daily = append(response.Daily, row)
	}
	response.ByModel, err = s.referenceReportByModel(ctx, filter)
	if err != nil {
		return response, err
	}
	response.ByAccount, err = s.referenceReportByAccount(ctx, filter)
	if err != nil {
		return response, err
	}
	response.ByUserAgent, err = s.referenceReportByUserAgent(ctx, filter)
	return response, err
}

func (s *Store) referenceDailyReport(ctx context.Context, filter domain.ReportFilter, start, end time.Time, date string) (domain.DailyReportRow, error) {
	var row domain.DailyReportRow
	row.Date = date
	values, err := s.aggregateReportWindow(ctx, filter, start, end)
	if err != nil {
		return row, err
	}
	row.Requests, row.InputTokens, row.OutputTokens = values.requests, values.input, values.output
	row.CachedInputTokens, row.ActiveAccounts = values.cached, values.accounts
	row.CostUSD = roundReport(values.costUSD, 4)
	row.ErrorCount, row.CancelledCount = values.errors, values.cancelled
	if values.known > 0 || values.reasoning > 0 {
		reasoning := values.reasoning
		row.ReasoningTokens = &reasoning
	}
	row.Conversations, err = s.countReportConversations(ctx, filter, start, end)
	if err != nil {
		return row, err
	}
	row.MedianTTFTMS, err = s.referenceReportMedian(ctx, filter, start, end, "e.latency_first_token_ms",
		"e.latency_first_token_ms>0")
	if err != nil {
		return row, err
	}
	row.MedianQueueMS, err = s.referenceReportMedian(ctx, filter, start, end, "e.queue_latency_ms",
		"e.queue_latency_ms>=0")
	if err != nil {
		return row, err
	}
	row.MedianTPS, err = s.referenceReportMedian(ctx, filter, start, end,
		"e.output_tokens*1000.0/(e.total_latency_ms-e.latency_first_token_ms)",
		"e.output_tokens>0 AND e.latency_first_token_ms>0 AND e.total_latency_ms>e.latency_first_token_ms")
	return row, err
}

func (s *Store) referenceReportMedian(ctx context.Context, filter domain.ReportFilter, start, end time.Time, expr, guard string) (float64, error) {
	where, args := rawReportWhere(filter, start, end)
	query := `SELECT coalesce(avg(value),0) FROM (
 SELECT ` + expr + ` AS value,row_number() OVER (ORDER BY ` + expr + `) AS rank,
 count(*) OVER () AS total FROM usage_events e WHERE ` + where + ` AND ` + guard + `
) WHERE rank IN ((total+1)/2,(total+2)/2)`
	var median float64
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&median)
	return roundReport(median, 2), err
}

func (s *Store) referenceReportByModel(ctx context.Context, filter domain.ReportFilter) ([]domain.ModelCostEntry, error) {
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

func (s *Store) referenceReportByAccount(ctx context.Context, filter domain.ReportFilter) ([]domain.AccountCostEntry, error) {
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

func (s *Store) referenceReportByUserAgent(ctx context.Context, filter domain.ReportFilter) ([]domain.UserAgentCostEntry, error) {
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
