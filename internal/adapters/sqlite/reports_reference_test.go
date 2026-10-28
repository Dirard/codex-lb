package sqlite

// Reference implementation of the admin report before the single-pass rewrite
// (one statement per measure and day), with the float cost sums and exact
// medians of that time. Tests compare QueryReports with it: costs within
// 1e-4 and medians within the histogram bound; it is not used by production
// code.

import (
	"context"
	"database/sql"
	"math"
	"reflect"
	"slices"
	"sort"
	"testing"
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
	current, err := s.referenceAggregateReportWindow(ctx, filter, filter.Start, filter.End)
	if err != nil {
		return response, err
	}
	previous, err := s.referenceAggregateReportWindow(ctx, filter, filter.PreviousStart, filter.Start)
	if err != nil {
		return response, err
	}
	conversations, err := countReportConversations(ctx, s.analyticsDB, filter, filter.Start, filter.End)
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
	response.Comparison.CanCompare, err = reportHasActivityBefore(ctx, s.analyticsDB, filter, filter.PreviousStart)
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
	values, err := s.referenceAggregateReportWindow(ctx, filter, start, end)
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
	row.Conversations, err = countReportConversations(ctx, s.analyticsDB, filter, start, end)
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
	facts, args := referenceReportFacts(filter, filter.Start, filter.End)
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
	facts, args := referenceReportFacts(filter, filter.Start, filter.End)
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

// referenceReportFacts is reportFacts with the former REAL cost per raw event.
func referenceReportFacts(filter domain.ReportFilter, start, end time.Time) (string, []any) {
	hWhere, hArgs := hourlyReportWhere(filter, start, end)
	rWhere, rArgs := rawReportWhere(filter, start, end)
	query := `WITH facts AS (
 SELECT h.bucket_epoch*1000 AS at_ms,h.account_id,h.api_key_id,h.model,
 h.request_count,h.input_tokens,h.output_or_reasoning_tokens AS output_tokens,
 h.reasoning_tokens,h.reasoning_known_count,
 h.cached_input_tokens_clamped AS cached_input_tokens,h.cost_usd,
 h.error_count,h.cancelled_count FROM legacy_hourly_usage h WHERE ` + hWhere + `
 UNION ALL
 SELECT e.requested_at,
 CASE WHEN e.account_id IS NULL THEN char(31) WHEN substr(e.account_id,1,1)=char(31)
 THEN char(31)||e.account_id ELSE e.account_id END,
 CASE WHEN e.api_key_id IS NULL THEN char(31) WHEN substr(e.api_key_id,1,1)=char(31)
 THEN char(31)||e.api_key_id ELSE e.api_key_id END,
 e.model,1,e.input_tokens,e.output_tokens,e.reasoning_tokens,e.reasoning_tokens_known,
 max(0,min(e.cached_input_tokens,e.input_tokens)),e.cost_microdollars/1000000.0,
 CASE WHEN e.status NOT IN ('success','completed','cancelled') THEN 1 ELSE 0 END,
 CASE WHEN e.status='cancelled' THEN 1 ELSE 0 END
 FROM usage_events e WHERE ` + rWhere + `)
`
	return query, append(hArgs, rArgs...)
}

// referenceAggregateReportWindow sums a window with the former float costs.
func (s *Store) referenceAggregateReportWindow(ctx context.Context, filter domain.ReportFilter, start, end time.Time) (reportAggregate, error) {
	var result reportAggregate
	facts, args := referenceReportFacts(filter, start, end)
	query := facts + `SELECT coalesce(sum(request_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(reasoning_tokens),0),
 coalesce(sum(reasoning_known_count),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(cost_usd),0),coalesce(sum(error_count),0),
 coalesce(sum(cancelled_count),0),
 count(DISTINCT CASE WHEN account_id!=char(31) THEN account_id END) FROM facts`
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&result.requests, &result.input, &result.output,
		&result.reasoning, &result.known, &result.cached, &result.costUSD, &result.errors,
		&result.cancelled, &result.accounts)
	return result, err
}

// reportCostTolerance bounds integer-microdollar sums against the former
// float sums: one unit of the fourth decimal on a rounding tie.
const reportCostTolerance = 1e-4 + 1e-9

// withinHistogramMedian reports whether a histogram median is within the
// design bound of an exact median, both rounded to two places: 1/64 relative,
// plus the floor of stored tokens per second (extra) and both roundings.
func withinHistogramMedian(got, want, extra float64) bool {
	return math.Abs(got-want) <= (want+0.005)/64+extra+0.01+1e-9
}

// assertReportNearReference compares a report with the reference: costs and
// cost shares within their tolerance, medians within the histogram bound and
// everything else exactly.
func assertReportNearReference(t *testing.T, label string, got, want domain.ReportsResponse) {
	t.Helper()
	cost := func(field string, got *float64, want float64) {
		t.Helper()
		if math.Abs(*got-want) > reportCostTolerance {
			t.Fatalf("%s: %s %v, reference %v", label, field, *got, want)
		}
		*got = want
	}
	share := func(field string, got *float64, want float64) {
		t.Helper()
		if math.Abs(*got-want) > 0.1+1e-9 {
			t.Fatalf("%s: %s share %v, reference %v", label, field, *got, want)
		}
		*got = want
	}
	median := func(field string, got *float64, want, extra float64) {
		t.Helper()
		if !withinHistogramMedian(*got, want, extra) {
			t.Fatalf("%s: %s median %v, reference %v", label, field, *got, want)
		}
		*got = want
	}
	got.Daily = slices.Clone(got.Daily)
	got.ByModel = slices.Clone(got.ByModel)
	got.ByAccount = slices.Clone(got.ByAccount)
	got.ByUserAgent = slices.Clone(got.ByUserAgent)
	cost("summary cost", &got.Summary.TotalCostUSD, want.Summary.TotalCostUSD)
	cost("previous cost", &got.Comparison.Previous.TotalCostUSD, want.Comparison.Previous.TotalCostUSD)
	if len(got.Daily) == len(want.Daily) {
		for i := range got.Daily {
			day := want.Daily[i].Date
			cost(day+" cost", &got.Daily[i].CostUSD, want.Daily[i].CostUSD)
			median(day+" ttft", &got.Daily[i].MedianTTFTMS, want.Daily[i].MedianTTFTMS, 0)
			median(day+" queue", &got.Daily[i].MedianQueueMS, want.Daily[i].MedianQueueMS, 0)
			median(day+" tps", &got.Daily[i].MedianTPS, want.Daily[i].MedianTPS, 0.01)
		}
	}
	if len(got.ByModel) == len(want.ByModel) {
		for i := range got.ByModel {
			cost(want.ByModel[i].Model+" cost", &got.ByModel[i].CostUSD, want.ByModel[i].CostUSD)
			share(want.ByModel[i].Model, &got.ByModel[i].Percentage, want.ByModel[i].Percentage)
		}
	}
	if len(got.ByAccount) == len(want.ByAccount) {
		for i := range got.ByAccount {
			cost("account cost", &got.ByAccount[i].CostUSD, want.ByAccount[i].CostUSD)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s differs:\n got %+v\nwant %+v", label, got, want)
	}
}
