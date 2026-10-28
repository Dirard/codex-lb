package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

// QueryDashboardTraffic evaluates the dashboard overview's traffic from
// rollups (reports_dashboard_rollup.go) or, as fallback and oracle, from raw
// events and folded hours.
func (s *Store) QueryDashboardTraffic(ctx context.Context, start, end time.Time, bucketSeconds, bucketCount int) (domain.DashboardTraffic, error) {
	if !start.Before(end) || bucketSeconds < 3600 || bucketSeconds > 86400 || bucketCount < 1 || bucketCount > 30 {
		return domain.DashboardTraffic{Buckets: []domain.DashboardTrafficBucket{}}, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.DashboardTraffic, error) {
		return r.dashboardTraffic(ctx, start, end, bucketSeconds, bucketCount)
	}, func(tx *sql.Tx) (domain.DashboardTraffic, error) {
		return queryDashboardTrafficRaw(ctx, tx, start, end, bucketSeconds, bucketCount)
	})
}

func queryDashboardTrafficRaw(ctx context.Context, q analyticsQuerier, start, end time.Time, bucketSeconds, bucketCount int) (domain.DashboardTraffic, error) {
	var result domain.DashboardTraffic
	result.Buckets = []domain.DashboardTrafficBucket{}
	filter := domain.ReportFilter{Start: start, End: end, Location: time.UTC}
	current, err := aggregateReportWindow(ctx, q, filter, start, end)
	if err != nil {
		return result, err
	}
	previousStart := start.Add(-end.Sub(start))
	previous, err := aggregateReportWindow(ctx, q, filter, previousStart, start)
	if err != nil {
		return result, err
	}
	result.Current = domain.UsageTotals{RequestCount: current.requests, FailedCount: current.errors + current.cancelled,
		Usage: domain.UsageAmount{InputTokens: current.input, OutputTokens: current.output,
			CachedInputTokens: current.cached, ReasoningTokens: current.reasoning}}
	result.Previous = domain.UsageTotals{RequestCount: previous.requests,
		Usage: domain.UsageAmount{InputTokens: previous.input, OutputTokens: previous.output}}
	result.CurrentCostUSD = current.costUSD
	currentCost := sql.NullFloat64{Float64: current.costUSD, Valid: true}
	result.Current.Usage.CostMicrodollars, err = legacyCostMicro(currentCost)
	if err != nil {
		return result, err
	}
	result.PreviousCostUSD = previous.costUSD
	result.CurrentErrors, result.CurrentCancelled = current.errors, current.cancelled
	result.CurrentConversations, err = countReportConversations(ctx, q, filter, start, end)
	if err != nil {
		return result, err
	}
	result.ConversationRequests, err = dashboardConversationRequests(ctx, q, filter, start, end)
	if err != nil {
		return result, err
	}
	result.TopError, err = dashboardTopError(ctx, q, filter, start, end)
	if err != nil {
		return result, err
	}
	result.CanCompare, err = reportHasActivityBefore(ctx, q, filter, previousStart)
	if err != nil {
		return result, err
	}
	first := (start.Unix() + int64(bucketSeconds) - 1) / int64(bucketSeconds) * int64(bucketSeconds)
	facts, args := reportFacts(filter, time.Unix(first, 0), end)
	sums, err := dashboardFactBuckets(ctx, q, facts, args, bucketSeconds)
	if err != nil {
		return result, err
	}
	buckets := map[int64]domain.DashboardTrafficBucket{}
	for epoch, sum := range sums {
		bucket := sum.bucket
		bucket.CostUSD = reportCostUSD(sum.legacyCost, sum.m.cost)
		buckets[epoch] = bucket
	}
	for index := 0; index < bucketCount; index++ {
		epoch := first + int64(index*bucketSeconds)
		bucket := buckets[epoch]
		bucket.At = time.Unix(epoch, 0).UTC()
		until := bucket.At.Add(time.Duration(bucketSeconds) * time.Second)
		if until.After(end) {
			until = end
		}
		if bucket.At.Before(end) {
			bucket.Conversations, err = countReportConversations(ctx, q, filter, bucket.At, until)
			if err != nil {
				return result, err
			}
		}
		result.Buckets = append(result.Buckets, bucket)
	}
	return result, nil
}

// dashboardFactSum is one trend bucket of facts: folded REAL costs summed by
// SQLite and raw microdollars apart.
type dashboardFactSum struct {
	bucket     domain.DashboardTrafficBucket
	legacyCost float64
	m          rollupMeasures
}

// dashboardFactBuckets sums facts by trend bucket.
func dashboardFactBuckets(ctx context.Context, q analyticsQuerier, facts string, args []any, bucketSeconds int) (map[int64]dashboardFactSum, error) {
	rows, err := q.QueryContext(ctx, facts+`SELECT (at_ms/1000/?)*?,coalesce(sum(request_count),0),
 coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),
 coalesce(sum(cached_input_tokens),0),coalesce(sum(cost_usd),0),coalesce(sum(cost_micro),0),
 coalesce(sum(error_count),0),coalesce(sum(cancelled_count),0)
 FROM facts GROUP BY 1 ORDER BY 1`, append(args, bucketSeconds, bucketSeconds)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sums := map[int64]dashboardFactSum{}
	for rows.Next() {
		var epoch int64
		var sum dashboardFactSum
		b := &sum.bucket
		if err := rows.Scan(&epoch, &b.Requests, &b.Input, &b.Output, &b.Cached, &sum.legacyCost, &sum.m.cost,
			&b.Errors, &b.Cancelled); err != nil {
			return nil, err
		}
		b.At = time.Unix(epoch, 0).UTC()
		sums[epoch] = sum
	}
	return sums, rows.Err()
}

func dashboardConversationRequests(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, start, end time.Time) (int64, error) {
	var folded, raw int64
	err := q.QueryRowContext(ctx, `SELECT coalesce(sum(request_count),0) FROM legacy_conversation_hourly
 WHERE bucket_epoch>=? AND bucket_epoch<?`, start.Unix(), end.Unix()).Scan(&folded)
	if err != nil {
		return 0, err
	}
	where, args := rawReportWhere(filter, start, end)
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM usage_events e WHERE `+where+`
 AND e.conversation_id IS NOT NULL AND trim(e.conversation_id)!=''`, args...).Scan(&raw)
	return folded + raw, err
}

func dashboardTopError(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, start, end time.Time) (*string, error) {
	rawWhere, rawArgs := rawReportWhere(filter, start, end)
	query := `SELECT error_code FROM (
 SELECT error_code,error_count AS amount FROM legacy_hourly_errors
 WHERE bucket_epoch>=? AND bucket_epoch<? AND error_code!='client_disconnected'
 UNION ALL
 SELECT e.error_code,1 FROM usage_events e WHERE ` + rawWhere + `
 AND e.status NOT IN ('success','completed','cancelled')
 AND e.error_code IS NOT NULL AND e.error_code!='' AND e.error_code!='client_disconnected'
) GROUP BY error_code ORDER BY sum(amount) DESC,error_code LIMIT 1`
	args := append([]any{start.Unix(), end.Unix()}, rawArgs...)
	var code string
	err := q.QueryRowContext(ctx, query, args...).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &code, nil
}
