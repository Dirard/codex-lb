package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

// rollupTraffic accumulates the current window of the dashboard overview
// and its trend buckets [first+i*size, first+(i+1)*size) seconds.
type rollupTraffic struct {
	first, size          int64
	window               rollupMeasures
	buckets              []rollupMeasures
	conversationRequests int64
	conversations        map[string]int64
	bucketConversations  []map[string]int64
	errors               map[string]int64
}

func newRollupTraffic(first, size int64, count int) *rollupTraffic {
	return &rollupTraffic{first: first, size: size, buckets: make([]rollupMeasures, count),
		conversations: map[string]int64{}, bucketConversations: make([]map[string]int64, count), errors: map[string]int64{}}
}

// bucket maps an instant in milliseconds to its trend bucket.
func (t *rollupTraffic) bucket(at int64) (int, bool) {
	if at < t.first*1000 {
		return 0, false
	}
	i := (at - t.first*1000) / (t.size * 1000)
	return int(i), i < int64(len(t.buckets))
}

func (t *rollupTraffic) addMeasures(at int64, m rollupMeasures) {
	t.window = t.window.plus(m)
	if i, ok := t.bucket(at); ok {
		t.buckets[i] = t.buckets[i].plus(m)
	}
}

func (t *rollupTraffic) addConversation(at int64, conversation string, n int64) {
	t.conversations[conversation] += n
	if i, ok := t.bucket(at); ok {
		addDayCount(t.bucketConversations, i, conversation, n)
	}
}

func (t *rollupTraffic) addImage(img *rollupImage, sign int64) {
	var m rollupMeasures
	m.add(img, sign)
	t.addMeasures(img.at, m)
	if sign == 0 {
		return
	}
	if img.conversation != "" {
		t.conversationRequests += sign
		t.addConversation(img.at, img.conversation, sign)
	}
	if errored, _ := rollupStatusClass(img.status); errored == 1 && img.errorCode != "" && img.errorCode != "client_disconnected" {
		t.errors[img.errorCode] += sign
	}
}

// dashboardTraffic is the rollup reader of QueryDashboardTraffic (design
// §4.2): total hours for sums and trend buckets, error hours for the top
// error, one pass over the window's conversation-hours for the distinct
// conversations of the window and of every bucket, folded legacy hours as the
// raw path reads them, and images for the partial hours, tail and journal.
func (r *rollupRead) dashboardTraffic(ctx context.Context, start, end time.Time, bucketSeconds, bucketCount int) (domain.DashboardTraffic, error) {
	result := domain.DashboardTraffic{Buckets: []domain.DashboardTrafficBucket{}}
	filter := domain.ReportFilter{Start: start, End: end, Location: time.UTC}
	previousStart := start.Add(-end.Sub(start))
	size := int64(bucketSeconds)
	first := (start.Unix() + size - 1) / size * size
	splits := make([]int64, 0, bucketCount+1)
	for i := range int64(bucketCount + 1) {
		splits = append(splits, (first+i*size)*1000)
	}
	plan := planRollupWindow(millis(start), millis(end), splits)
	traffic := newRollupTraffic(first, size, bucketCount)
	removals := false
	where, args := reportEventWhere(reportFilterDims{})
	if err := r.images(ctx, plan, where, args, func(img *rollupImage, sign int64) {
		traffic.addImage(img, sign)
		removals = removals || sign < 0 && img.conversation != ""
	}); err != nil {
		return result, err
	}
	if err := r.trafficHours(ctx, plan, traffic); err != nil {
		return result, err
	}
	if err := r.conversationHours(ctx, plan, reportFilterDims{}, removals, traffic.addConversation); err != nil {
		return result, err
	}
	legacy, legacyCost, err := legacyWindow(ctx, r.tx, filter, start, end)
	if err != nil {
		return result, err
	}
	current := legacy.plus(traffic.window)
	result.Current = domain.UsageTotals{RequestCount: current.requests, FailedCount: current.errors + current.cancelled,
		Usage: domain.UsageAmount{InputTokens: current.input, OutputTokens: current.output,
			CachedInputTokens: current.cached, ReasoningTokens: current.reasoning}}
	result.CurrentCostUSD = reportCostUSD(legacyCost, current.cost)
	if result.Current.Usage.CostMicrodollars, err = legacyCostMicro(sql.NullFloat64{Float64: result.CurrentCostUSD, Valid: true}); err != nil {
		return result, err
	}
	previous, err := r.windowAggregate(ctx, filter, previousStart, start)
	if err != nil {
		return result, err
	}
	result.Previous = domain.UsageTotals{RequestCount: previous.requests,
		Usage: domain.UsageAmount{InputTokens: previous.input, OutputTokens: previous.output}}
	result.PreviousCostUSD = previous.costUSD
	result.CurrentErrors, result.CurrentCancelled = current.errors, current.cancelled
	legacyConversations, err := r.legacyTrafficConversations(ctx, start, end, traffic)
	if err != nil {
		return result, err
	}
	result.CurrentConversations = countPresent(legacyConversations[len(legacyConversations)-1], traffic.conversations)
	var foldedRequests int64
	if err := r.tx.QueryRowContext(ctx, `SELECT coalesce(sum(request_count),0) FROM legacy_conversation_hourly
 WHERE bucket_epoch>=? AND bucket_epoch<?`, start.Unix(), end.Unix()).Scan(&foldedRequests); err != nil {
		return result, err
	}
	result.ConversationRequests = foldedRequests + traffic.conversationRequests
	if result.TopError, err = r.trafficTopError(ctx, start, end, traffic); err != nil {
		return result, err
	}
	if result.CanCompare, err = r.reportHasActivityBefore(ctx, filter, previousStart); err != nil {
		return result, err
	}
	facts, factArgs := legacyReportFacts(filter, time.Unix(first, 0), end)
	sums, err := dashboardFactBuckets(ctx, r.tx, facts, factArgs, bucketSeconds)
	if err != nil {
		return result, err
	}
	for index := range bucketCount {
		epoch := first + int64(index)*size
		sum := sums[epoch]
		m := sum.m.plus(traffic.buckets[index])
		bucket := domain.DashboardTrafficBucket{At: time.Unix(epoch, 0).UTC(), Requests: sum.bucket.Requests + m.requests,
			Input: sum.bucket.Input + m.input, Output: sum.bucket.Output + m.output, Cached: sum.bucket.Cached + m.cached,
			Errors: sum.bucket.Errors + m.errors, Cancelled: sum.bucket.Cancelled + m.cancelled,
			CostUSD: reportCostUSD(sum.legacyCost, m.cost)}
		if bucket.At.Before(end) {
			bucket.Conversations = countPresent(legacyConversations[index], traffic.bucketConversations[index])
		}
		result.Buckets = append(result.Buckets, bucket)
	}
	return result, nil
}

// trafficHours adds the total hours of the plan to the window and buckets.
func (r *rollupRead) trafficHours(ctx context.Context, plan rollupPlan, traffic *rollupTraffic) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,`+rollupMeasureColumns+`,conversation_requests
 FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, conversations int64
		var m rollupMeasures
		if err := rows.Scan(append(append([]any{&hour}, m.fields()...), &conversations)...); err != nil {
			return err
		}
		if plan.rollupHour(hour) {
			traffic.addMeasures(hour*1000, m)
			traffic.conversationRequests += conversations
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	rows, err = r.tx.QueryContext(ctx, `SELECT bucket_epoch,error_code,errors FROM usage_rollup_error_hour
 WHERE bucket_epoch>=? AND bucket_epoch<? AND error_code<>'client_disconnected'`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, n int64
		var code string
		if err := rows.Scan(&hour, &code, &n); err != nil {
			return err
		}
		if plan.rollupHour(hour) {
			traffic.errors[code] += n
		}
	}
	return rows.Err()
}

// conversationHours feeds add with the in_reports conversation-hours of the
// plan's rollup hours matching dims. Without pending removals a row's
// presence is enough, which the covering unique index serves without table
// reads; exact counts read the rows.
func (r *rollupRead) conversationHours(ctx context.Context, plan rollupPlan, dims reportFilterDims, exact bool, add func(at int64, conversation string, n int64)) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	count := "1"
	if exact {
		count = "c.requests"
	}
	where, args := dims.rollupDimWhere("c")
	rows, err := r.tx.QueryContext(ctx, `SELECT c.bucket_epoch,c.conversation_id,`+count+`
 FROM usage_rollup_conversation_hour c WHERE c.bucket_epoch>=? AND c.bucket_epoch<? AND c.in_reports=1 AND `+where,
		append([]any{from, to}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, n int64
		var conversation string
		if err := rows.Scan(&hour, &conversation, &n); err != nil {
			return err
		}
		if plan.rollupHour(hour) {
			add(hour*1000, conversation, n)
		}
	}
	return rows.Err()
}

// legacyTrafficConversations reads the folded conversation-hours of the
// window once: the sets of every trend bucket, then the window's set last,
// with the bounds of countReportConversations.
func (r *rollupRead) legacyTrafficConversations(ctx context.Context, start, end time.Time, traffic *rollupTraffic) ([]map[string]struct{}, error) {
	sets := make([]map[string]struct{}, len(traffic.buckets)+1)
	for i := range sets {
		sets[i] = map[string]struct{}{}
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT c.bucket_epoch,c.conversation_id FROM legacy_conversation_hourly c
 WHERE c.bucket_epoch>=? AND c.bucket_epoch<?`, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int64
		var conversation string
		if err := rows.Scan(&bucket, &conversation); err != nil {
			return nil, err
		}
		sets[len(traffic.buckets)][conversation] = struct{}{}
		if i, ok := traffic.bucket(bucket * 1000); ok {
			at := time.Unix(traffic.first+int64(i)*traffic.size, 0)
			until := at.Add(time.Duration(traffic.size) * time.Second)
			if until.After(end) {
				until = end
			}
			if bucket < until.Unix() {
				sets[i][conversation] = struct{}{}
			}
		}
	}
	return sets, rows.Err()
}

// countPresent counts the union of folded conversations and conversations
// with events.
func countPresent(folded map[string]struct{}, events map[string]int64) int64 {
	n := int64(len(folded))
	for conversation, count := range events {
		if _, ok := folded[conversation]; !ok && count > 0 {
			n++
		}
	}
	return n
}

// trafficTopError is dashboardTopError over folded error hours, error hours
// and images.
func (r *rollupRead) trafficTopError(ctx context.Context, start, end time.Time, traffic *rollupTraffic) (*string, error) {
	present := map[string]bool{}
	totals := map[string]int64{}
	rows, err := r.tx.QueryContext(ctx, `SELECT error_code,sum(error_count) FROM legacy_hourly_errors
 WHERE bucket_epoch>=? AND bucket_epoch<? AND error_code!='client_disconnected' GROUP BY error_code`, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var n int64
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		present[code], totals[code] = true, n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for code, n := range traffic.errors {
		totals[code] += n
		present[code] = present[code] || n > 0
	}
	var top *string
	for code, ok := range present {
		if !ok {
			continue
		}
		if top == nil || totals[code] > totals[*top] || totals[code] == totals[*top] && code < *top {
			code := code
			top = &code
		}
	}
	return top, nil
}
