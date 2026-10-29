package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// groupsSpanMaxDays bounds the span a groups summary reads before the
// overview's days.
const groupsSpanMaxDays = 40

// GroupsSummaryEntities reads the overview's entity snapshot (OverviewEntities)
// and, in the same read snapshot and one more statement, the limit templates
// of every group (dashboard redesign use case 7, "limit templates from
// group_limits").
func (s *Store) GroupsSummaryEntities(ctx context.Context, now time.Time) (domain.GroupsSummaryEntities, error) {
	var result domain.GroupsSummaryEntities
	if now.IsZero() {
		return result, ErrInvalid
	}
	err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
		accounts, err := listAccountHealth(ctx, tx)
		if err != nil {
			return err
		}
		groups, err := listGroupRefs(ctx, tx)
		if err != nil {
			return err
		}
		keys, err := listKeyRows(ctx, tx, now)
		if err != nil {
			return err
		}
		limits, err := listGroupLimitTemplates(ctx, tx)
		if err != nil {
			return err
		}
		result = domain.GroupsSummaryEntities{OverviewEntities: domain.OverviewEntities{Accounts: accounts, Groups: groups, Keys: keys},
			Limits: limits}
		return nil
	})
	return result, err
}

// listGroupLimitTemplates reads every group's limit templates in one statement.
func listGroupLimitTemplates(ctx context.Context, tx *sql.Tx) (map[string][]domain.LimitTemplate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id,limit_type,limit_window,model_filter,max_value FROM group_limits
 ORDER BY group_id,limit_type,limit_window,model_filter`)
	if err != nil {
		return nil, err
	}
	limits := map[string][]domain.LimitTemplate{}
	for rows.Next() {
		var group, model string
		var template domain.LimitTemplate
		if err := rows.Scan(&group, &template.Type, &template.Window, &model, &template.MaxValue); err != nil {
			rows.Close()
			return nil, err
		}
		template.ModelFilter = optionalModel(model)
		limits[group] = append(limits[group], template)
	}
	return limits, closeRows(rows)
}

// GroupsSummaryUsage reads, in one analytics snapshot, the overview's usage
// (OverviewUsage) and the requests and cost of the span [SpanStart, the
// overview's first day) per key and for the pool, which complete the 30-day
// spend of the Groups page (use case 7, "30-day spend from the key day
// slices"). While the rollups are unusable the overview usage follows its raw
// fallback and the span is not read.
func (s *Store) GroupsSummaryUsage(ctx context.Context, q domain.GroupsSummaryUsageQuery) (domain.GroupsSummaryUsage, error) {
	if err := checkedOverviewQuery(q.Overview); err != nil {
		return domain.GroupsSummaryUsage{}, err
	}
	first := q.Overview.DayStarts[0]
	if q.SpanStart.IsZero() || !q.SpanStart.Before(first) || first.Sub(q.SpanStart) > groupsSpanMaxDays*24*time.Hour {
		return domain.GroupsSummaryUsage{}, fmt.Errorf("groups summary span: %w", ErrInvalid)
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.GroupsSummaryUsage, error) {
		usage, err := r.overviewUsage(ctx, q.Overview)
		if err != nil {
			return domain.GroupsSummaryUsage{}, err
		}
		span, err := r.keySpan(ctx, millis(q.SpanStart), millis(first))
		if err != nil {
			return domain.GroupsSummaryUsage{}, err
		}
		return span.result(usage), nil
	}, func(tx *sql.Tx) (domain.GroupsSummaryUsage, error) {
		from := q.Overview.DayStarts[len(q.Overview.DayStarts)-1]
		if q.Overview.TrailingStart.Before(from) {
			from = q.Overview.TrailingStart
		}
		usage, err := overviewUsageFromEvents(ctx, tx, q.Overview, from)
		return domain.GroupsSummaryUsage{Overview: usage}, err
	})
}

// spanSums accumulates one span's requests and cost per key dimension and for
// the pool.
type spanSums struct {
	pool overviewDay
	keys map[string]*overviewDay
}

func (s *spanSums) add(dim string, requests, micro int64, legacy float64) {
	key := s.keys[dim]
	if key == nil {
		key = &overviewDay{}
		s.keys[dim] = key
	}
	for _, day := range []*overviewDay{&s.pool, key} {
		day.requests += requests
		day.micro += micro
		day.legacy += legacy
	}
}

func (s *spanSums) result(usage domain.OverviewUsage) domain.GroupsSummaryUsage {
	out := domain.GroupsSummaryUsage{Overview: usage, SpanKnown: true, SpanKeys: make(map[string]domain.UsageDay, len(s.keys)),
		SpanPool: domain.UsageDay{Requests: s.pool.requests, CostUSD: reportCostUSD(s.pool.legacy, s.pool.micro)}}
	for dim, day := range s.keys {
		id := decodeRollupDim(dim)
		if !id.Valid || *day == (overviewDay{}) {
			// Corrections that cancel out leave no usage, as raw events do.
			continue
		}
		out.SpanKeys[id.String] = domain.UsageDay{Requests: day.requests, CostUSD: reportCostUSD(day.legacy, day.micro)}
	}
	return out
}

// keySpan is the rollup reader of one span [from, to) summed per key: folded
// legacy hours, raw partial hours as per-key sums in SQL, the unfolded tail
// and the journal, and key slice rows (UTC-day rows for whole UTC days, hour
// rows at the edges; an edge longer than half a day reads its UTC day's row
// minus the hour rows outside the span).
func (r *rollupRead) keySpan(ctx context.Context, from, to int64) (*spanSums, error) {
	sums := &spanSums{keys: map[string]*overviewDay{}}
	if err := sums.addLegacy(ctx, r.tx, fromMillis(from), fromMillis(to)); err != nil {
		return nil, err
	}
	plan := planRollupWindow(from, to, nil)
	where, args := reportEventWhere(reportFilterDims{})
	if err := r.spanRawSums(ctx, plan, sums, where, args); err != nil {
		return nil, err
	}
	if err := r.unfoldedImages(ctx, plan, where, args, func(img *rollupImage, sign int64) {
		var m rollupMeasures
		m.add(img, sign)
		sums.add(rawReportDimension(img.key), m.requests, m.cost, 0)
	}); err != nil {
		return nil, err
	}
	return sums, r.spanSlices(ctx, planSpanSlices(plan), sums)
}

// addLegacy adds the folded legacy hours starting inside [start, end).
func (s *spanSums) addLegacy(ctx context.Context, q analyticsQuerier, start, end time.Time) error {
	facts, args := legacyReportFacts(domain.ReportFilter{}, start, end)
	rows, err := q.QueryContext(ctx, facts+`SELECT api_key_id,coalesce(sum(request_count),0),coalesce(sum(cost_usd),0)
 FROM facts GROUP BY api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dim string
		var requests int64
		var cost float64
		if err := rows.Scan(&dim, &requests, &cost); err != nil {
			return err
		}
		s.add(dim, requests, 0, cost)
	}
	return rows.Err()
}

// spanRawSums reads the raw spans of plan as per-key sums in SQL.
func (r *rollupRead) spanRawSums(ctx context.Context, plan rollupPlan, sums *spanSums, where string, args []any) error {
	spans := plan.raws()
	if len(spans) == 0 {
		return nil
	}
	values := strings.TrimSuffix(strings.Repeat("(?,?),", len(spans)), ",")
	spanArgs := make([]any, 0, 2*len(spans)+len(args))
	for _, span := range spans {
		spanArgs = append(spanArgs, span.from, span.to)
	}
	rows, err := r.tx.QueryContext(ctx, `WITH spans(lo,hi) AS (VALUES `+values+`)
 SELECT e.api_key_id,count(*),coalesce(sum(e.cost_microdollars),0)
 FROM spans s CROSS JOIN usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=s.lo AND e.requested_at<s.hi AND `+where+` GROUP BY e.api_key_id`, append(spanArgs, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key sql.NullString
		var requests, micro int64
		if err := rows.Scan(&key, &requests, &micro); err != nil {
			return err
		}
		sums.add(rawReportDimension(key), requests, micro, 0)
	}
	return rows.Err()
}

// spanBuckets maps slice buckets (seconds) of each grain to their signs.
type spanBuckets struct {
	hours, days map[int64]int64
}

// planSpanSlices is the step "30-day spend from the key day slices" of use
// case 7 for the rollup spans of a plan: UTC-day rows for whole UTC days, and
// at each edge either its hour rows or, when it holds more than half a day,
// the row of its UTC day minus the hour rows of that day outside the span.
func planSpanSlices(plan rollupPlan) spanBuckets {
	b := spanBuckets{hours: map[int64]int64{}, days: map[int64]int64{}}
	hours := func(from, to, sign int64) {
		for h := from; h < to; h += rollupHourMS {
			b.hours[h/1000] += sign
		}
	}
	for _, span := range plan {
		if span.raw {
			continue
		}
		lo, hi := span.from, span.to
		dayFrom, dayTo := floorRollupDay(lo+rollupDayMS-1), floorRollupDay(hi)
		if dayFrom >= dayTo {
			hours(lo, hi, 1)
			continue
		}
		for d := dayFrom; d < dayTo; d += rollupDayMS {
			b.days[d/1000]++
		}
		if dayFrom-lo > rollupDayMS/2 {
			b.days[(dayFrom-rollupDayMS)/1000]++
			hours(dayFrom-rollupDayMS, lo, -1)
		} else {
			hours(lo, dayFrom, 1)
		}
		if hi-dayTo > rollupDayMS/2 {
			b.days[dayTo/1000]++
			hours(hi, dayTo+rollupDayMS, -1)
		} else {
			hours(dayTo, hi, 1)
		}
	}
	return b
}

// spanSlices reads the key slice rows of the buckets in one statement: the
// bucket lists travel as JSON arrays and drive primary-key lookups.
func (r *rollupRead) spanSlices(ctx context.Context, b spanBuckets, sums *spanSums) error {
	keys := func(m map[int64]int64) []int64 {
		out := make([]int64, 0, len(m))
		for bucket, sign := range m {
			if sign != 0 {
				out = append(out, bucket)
			}
		}
		return out
	}
	hourList, dayList := keys(b.hours), keys(b.days)
	if len(hourList)+len(dayList) == 0 {
		return nil
	}
	hours, err := json.Marshal(hourList)
	if err != nil {
		return err
	}
	days, err := json.Marshal(dayList)
	if err != nil {
		return err
	}
	rows, err := r.tx.QueryContext(ctx, `WITH b(grain,bucket) AS (SELECT 3600,value FROM json_each(?) UNION ALL SELECT 86400,value FROM json_each(?))
 SELECT s.grain,s.bucket_epoch,s.dim,s.requests,s.cost_microdollars
 FROM b CROSS JOIN usage_rollup_slice s WHERE s.kind='key' AND s.grain=b.grain AND s.bucket_epoch=b.bucket`, string(hours), string(days))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grain, bucket, requests, micro int64
		var dim string
		if err := rows.Scan(&grain, &bucket, &dim, &requests, &micro); err != nil {
			return err
		}
		sign := b.days[bucket]
		if grain == 3600 {
			sign = b.hours[bucket]
		}
		sums.add(dim, sign*requests, sign*micro, 0)
	}
	return rows.Err()
}
