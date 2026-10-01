package sqlite

import (
	"context"
	"math"
	"time"

	"codex-lb/internal/domain"
)

type reportAggregate struct {
	requests, input, output, reasoning, known, cached, errors, cancelled, accounts int64
	costUSD                                                                        float64
}

func (s *Store) QueryReports(ctx context.Context, filter domain.ReportFilter) (domain.ReportsResponse, error) {
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
		row, err := s.dailyReport(ctx, filter, day.UTC(), end.UTC(), day.Format("2006-01-02"))
		if err != nil {
			return response, err
		}
		response.Daily = append(response.Daily, row)
	}
	response.ByModel, err = s.reportByModel(ctx, filter)
	if err != nil {
		return response, err
	}
	response.ByAccount, err = s.reportByAccount(ctx, filter)
	if err != nil {
		return response, err
	}
	response.ByUserAgent, err = s.reportByUserAgent(ctx, filter)
	return response, err
}

func (s *Store) aggregateReportWindow(ctx context.Context, filter domain.ReportFilter, start, end time.Time) (reportAggregate, error) {
	var result reportAggregate
	facts, args := reportFacts(filter, start, end)
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

func (s *Store) reportHasActivityBefore(ctx context.Context, filter domain.ReportFilter, before time.Time) (bool, error) {
	facts, args := reportFacts(filter, time.Unix(0, 0), before)
	var count int
	err := s.readDB.QueryRowContext(ctx, facts+"SELECT count(*) FROM facts LIMIT 1", args...).Scan(&count)
	return count > 0, err
}

func (s *Store) dailyReport(ctx context.Context, filter domain.ReportFilter, start, end time.Time, date string) (domain.DailyReportRow, error) {
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
	row.MedianTTFTMS, err = s.reportMedian(ctx, filter, start, end, "e.latency_first_token_ms",
		"e.latency_first_token_ms>0")
	if err != nil {
		return row, err
	}
	row.MedianQueueMS, err = s.reportMedian(ctx, filter, start, end, "e.queue_latency_ms",
		"e.queue_latency_ms>=0")
	if err != nil {
		return row, err
	}
	row.MedianTPS, err = s.reportMedian(ctx, filter, start, end,
		"e.output_tokens*1000.0/(e.total_latency_ms-e.latency_first_token_ms)",
		"e.output_tokens>0 AND e.latency_first_token_ms>0 AND e.total_latency_ms>e.latency_first_token_ms")
	return row, err
}

func (s *Store) reportMedian(ctx context.Context, filter domain.ReportFilter, start, end time.Time, expr, guard string) (float64, error) {
	where, args := rawReportWhere(filter, start, end)
	query := `SELECT coalesce(avg(value),0) FROM (
 SELECT ` + expr + ` AS value,row_number() OVER (ORDER BY ` + expr + `) AS rank,
 count(*) OVER () AS total FROM usage_events e WHERE ` + where + ` AND ` + guard + `
) WHERE rank IN ((total+1)/2,(total+2)/2)`
	var median float64
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&median)
	return roundReport(median, 2), err
}

func roundReport(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
