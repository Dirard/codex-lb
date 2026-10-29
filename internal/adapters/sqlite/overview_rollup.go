package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// overviewMaxDays bounds the local days of one overview usage read.
const overviewMaxDays = 32

// overviewSliceMaxRanges bounds the bucket ranges of the key slice query; a
// plan with more reads the hour rows of its whole range instead.
const overviewSliceMaxRanges = 64

// clientDisconnected is the error code that never counts as an error.
const clientDisconnected = "client_disconnected"

// OverviewUsage returns per-key and pool requests and cost for each local day
// of the query, the pool's spend at the same time yesterday and the
// last-hour and trailing error windows (dashboard redesign use case 2.1,
// "derive spend from member keys"). It reads key slices, total hours and
// error hours from the rollups with the journal, the unfolded tail and raw
// partial hours (overview_rollup.go) in a fixed number of statements; while
// the rollups are unusable it reads only the raw events from today's local
// midnight or the trailing window start, whichever is earlier.
func (s *Store) OverviewUsage(ctx context.Context, q domain.OverviewUsageQuery) (domain.OverviewUsage, error) {
	if err := checkedOverviewQuery(q); err != nil {
		return domain.OverviewUsage{}, err
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.OverviewUsage, error) {
		return r.overviewUsage(ctx, q)
	}, func(tx *sql.Tx) (domain.OverviewUsage, error) {
		from := q.DayStarts[len(q.DayStarts)-1]
		if q.TrailingStart.Before(from) {
			from = q.TrailingStart
		}
		return overviewUsageFromEvents(ctx, tx, q, from)
	})
}

func checkedOverviewQuery(q domain.OverviewUsageQuery) error {
	n := len(q.DayStarts)
	if n == 0 || n > overviewMaxDays || q.Now.IsZero() || q.DayStarts[n-1].After(q.Now) ||
		!q.TrailingStart.Before(q.LastHourStart) || q.LastHourStart.After(q.Now) || q.TrailingStart.Before(q.DayStarts[0]) {
		return fmt.Errorf("overview usage query: %w", ErrInvalid)
	}
	for i := 1; i < n; i++ {
		if !q.DayStarts[i-1].Before(q.DayStarts[i]) {
			return fmt.Errorf("overview days: %w", ErrInvalid)
		}
	}
	if n >= 2 && (q.YesterdaySameTime.Before(q.DayStarts[n-2]) || q.YesterdaySameTime.After(q.DayStarts[n-1])) {
		return fmt.Errorf("overview yesterday: %w", ErrInvalid)
	}
	return nil
}

// overviewDay sums one scope's day: raw microdollars and the SQL sum of
// folded legacy costs, combined like every report.
type overviewDay struct {
	requests, micro int64
	legacy          float64
}

type overviewScope struct {
	days               []overviewDay
	lastHour, trailing domain.ErrorWindow
}

// overviewSums accumulates the overview's scopes over day and error bounds in
// UTC milliseconds.
type overviewSums struct {
	bounds                           []int64 // day starts, then now
	yesterdayFrom, yesterdayTo       int64
	lastHourFrom, trailingFrom, upTo int64
	pool                             overviewScope
	yesterday                        overviewDay
	keys                             map[string]*overviewScope
}

func newOverviewSums(q domain.OverviewUsageQuery) *overviewSums {
	sums := &overviewSums{lastHourFrom: millis(q.LastHourStart), trailingFrom: millis(q.TrailingStart), upTo: millis(q.Now),
		keys: map[string]*overviewScope{}}
	for _, start := range q.DayStarts {
		sums.bounds = append(sums.bounds, millis(start))
	}
	sums.bounds = append(sums.bounds, millis(q.Now))
	sums.pool.days = make([]overviewDay, len(q.DayStarts))
	if n := len(q.DayStarts); n >= 2 {
		sums.yesterdayFrom, sums.yesterdayTo = millis(q.DayStarts[n-2]), millis(q.YesterdaySameTime)
	}
	return sums
}

// day returns the local day holding instant at.
func (o *overviewSums) day(at int64) (int, bool) {
	i := sort.Search(len(o.bounds), func(i int) bool { return o.bounds[i] > at }) - 1
	return i, i >= 0 && i < len(o.bounds)-1
}

func (o *overviewSums) key(dim string) *overviewScope {
	scope := o.keys[dim]
	if scope == nil {
		scope = &overviewScope{days: make([]overviewDay, len(o.pool.days))}
		o.keys[dim] = scope
	}
	return scope
}

// addErrors counts requests and errors of an instant in the error windows.
func (o *overviewSums) addErrors(scope *overviewScope, at, requests, errors int64) {
	switch {
	case at >= o.lastHourFrom && at < o.upTo:
		scope.lastHour.Requests += requests
		scope.lastHour.Errors += errors
	case at >= o.trailingFrom && at < o.lastHourFrom:
		scope.trailing.Requests += requests
		scope.trailing.Errors += errors
	}
}

// addImage counts one signed event image in the pool and its key.
func (o *overviewSums) addImage(img *rollupImage, sign int64) {
	var m rollupMeasures
	m.add(img, sign)
	key := o.key(rawReportDimension(img.key))
	if d, ok := o.day(img.at); ok {
		for _, scope := range []*overviewScope{&o.pool, key} {
			scope.days[d].requests += m.requests
			scope.days[d].micro += m.cost
		}
	}
	if img.at >= o.yesterdayFrom && img.at < o.yesterdayTo {
		o.yesterday.requests += m.requests
		o.yesterday.micro += m.cost
	}
	if sign == 0 {
		return
	}
	errored, _ := rollupStatusClass(img.status)
	if img.errorCode == clientDisconnected {
		errored = 0
	}
	o.addErrors(&o.pool, img.at, sign, sign*errored)
	o.addErrors(key, img.at, sign, sign*errored)
}

// addLegacy adds folded legacy hours: each hour belongs to the day holding
// its start, as in every report.
func (o *overviewSums) addLegacy(ctx context.Context, q analyticsQuerier, start, end time.Time) error {
	facts, args := legacyReportFacts(domain.ReportFilter{}, start, end)
	rows, err := q.QueryContext(ctx, facts+`SELECT at_ms,api_key_id,coalesce(sum(request_count),0),coalesce(sum(cost_usd),0)
 FROM facts GROUP BY at_ms,api_key_id ORDER BY at_ms,api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at, requests int64
		var dim string
		var cost float64
		if err := rows.Scan(&at, &dim, &requests, &cost); err != nil {
			return err
		}
		if d, ok := o.day(at); ok {
			for _, scope := range []*overviewScope{&o.pool, o.key(dim)} {
				scope.days[d].requests += requests
				scope.days[d].legacy += cost
			}
		}
		if at >= o.yesterdayFrom && at < o.yesterdayTo {
			o.yesterday.requests += requests
			o.yesterday.legacy += cost
		}
	}
	return rows.Err()
}

func (o *overviewSums) result(rollups, daysKnown bool) domain.OverviewUsage {
	usage := domain.OverviewUsage{RollupsUsable: rollups, DaysKnown: daysKnown, Pool: o.pool.scope(),
		Keys: make(map[string]domain.OverviewScopeUsage, len(o.keys))}
	if daysKnown && len(o.pool.days) >= 2 {
		usage.YesterdaySameTime = &domain.UsageDay{Requests: o.yesterday.requests, CostUSD: reportCostUSD(o.yesterday.legacy, o.yesterday.micro)}
	}
	for dim, scope := range o.keys {
		id := decodeRollupDim(dim)
		if !id.Valid {
			continue
		}
		usage.Keys[id.String] = scope.scope()
	}
	return usage
}

func (s *overviewScope) scope() domain.OverviewScopeUsage {
	out := domain.OverviewScopeUsage{Days: make([]domain.UsageDay, len(s.days)), LastHour: s.lastHour, Trailing: s.trailing}
	for i, day := range s.days {
		out.Days[i] = domain.UsageDay{Requests: day.requests, CostUSD: reportCostUSD(day.legacy, day.micro)}
	}
	return out
}

// overviewUsage is the rollup reader of OverviewUsage: folded legacy hours,
// signed images for raw hours, the tail and the journal, total hours for the
// pool, key slices (a UTC-day row with hour corrections for each past local
// day whose zone offset is a whole number of hours, hour rows otherwise) and
// client-disconnect error hours.
func (r *rollupRead) overviewUsage(ctx context.Context, q domain.OverviewUsageQuery) (domain.OverviewUsage, error) {
	sums := newOverviewSums(q)
	start := q.DayStarts[0]
	if err := sums.addLegacy(ctx, r.tx, start, q.Now); err != nil {
		return domain.OverviewUsage{}, err
	}
	splits := append([]int64{}, sums.bounds...)
	splits = append(splits, millis(q.YesterdaySameTime), sums.lastHourFrom, sums.trailingFrom)
	plan := planRollupWindow(millis(start), millis(q.Now), splits)
	where, args := reportEventWhere(reportFilterDims{})
	if err := r.images(ctx, plan, where, args, sums.addImage); err != nil {
		return domain.OverviewUsage{}, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return sums.result(true, true), nil
	}
	if err := r.overviewTotals(ctx, plan, from, to, sums); err != nil {
		return domain.OverviewUsage{}, err
	}
	if err := r.overviewKeySlices(ctx, plan, sums); err != nil {
		return domain.OverviewUsage{}, err
	}
	if err := r.overviewDisconnects(ctx, plan, sums); err != nil {
		return domain.OverviewUsage{}, err
	}
	return sums.result(true, true), nil
}

// overviewTotals reads the pool's total hours.
func (r *rollupRead) overviewTotals(ctx context.Context, plan rollupPlan, from, to int64, sums *overviewSums) error {
	rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,requests,errors,cost_microdollars
 FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, requests, errors, micro int64
		if err := rows.Scan(&hour, &requests, &errors, &micro); err != nil {
			return err
		}
		if !plan.rollupHour(hour) {
			continue
		}
		at := hour * 1000
		if d, ok := sums.day(at); ok {
			sums.pool.days[d].requests += requests
			sums.pool.days[d].micro += micro
		}
		if at >= sums.yesterdayFrom && at+rollupHourMS <= sums.yesterdayTo {
			sums.yesterday.requests += requests
			sums.yesterday.micro += micro
		}
		sums.addErrors(&sums.pool, at, requests, errors)
	}
	return rows.Err()
}

// sliceTerm is one signed contribution of a slice row to a local day.
type sliceTerm struct {
	day  int
	sign int64
}

// overviewSlicePlan maps slice buckets (seconds) of each grain to the local
// days they contribute to.
type overviewSlicePlan struct {
	hours, days map[int64][]sliceTerm
	errorHours  map[int64]bool
}

// planOverviewSlices is the step "local-day sums per key" of use case 2.1:
// a past local day [L0, L1) whose bounds are whole hours is read as the
// UTC-day row D of its midpoint minus the hour rows of D outside it plus
// the hour rows of it outside D, when every hour of D inside it is a rollup
// hour and that reads fewer rows than its hour rows; otherwise, and for
// today, from its rollup hour rows. Hour rows of the error windows are read
// too.
func planOverviewSlices(plan rollupPlan, sums *overviewSums) overviewSlicePlan {
	sp := overviewSlicePlan{hours: map[int64][]sliceTerm{}, days: map[int64][]sliceTerm{}, errorHours: map[int64]bool{}}
	addHours := func(day int, from, to int64, sign int64, rollupOnly bool) {
		for h := from; h < to; h += rollupHourMS {
			if !rollupOnly || plan.rolledUp(h) {
				sp.hours[h/1000] = append(sp.hours[h/1000], sliceTerm{day, sign})
			}
		}
	}
	last := len(sums.bounds) - 2
	for day := 0; day <= last; day++ {
		from, to := sums.bounds[day], sums.bounds[day+1]
		hourFrom := (from + rollupHourMS - 1) / rollupHourMS * rollupHourMS
		if day == last || from%rollupHourMS != 0 || to%rollupHourMS != 0 {
			addHours(day, hourFrom, floorRollupHour(to), 1, true)
			continue
		}
		mid := from + (to-from)/2
		utcDay := mid / (24 * rollupHourMS) * (24 * rollupHourMS)
		utcEnd := utcDay + 24*rollupHourMS
		inFrom, inTo := max(from, utcDay), min(to, utcEnd)
		usable := inFrom < inTo
		for h := inFrom; usable && h < inTo; h += rollupHourMS {
			usable = plan.rolledUp(h)
		}
		// Rows of the UTC day outside the local day and of the local day
		// outside the UTC day.
		outside := ((inFrom - utcDay) + (utcEnd - inTo) + (inFrom - from) + (to - inTo)) / rollupHourMS
		if !usable || 1+outside >= (to-from)/rollupHourMS {
			addHours(day, from, to, 1, true)
			continue
		}
		sp.days[utcDay/1000] = append(sp.days[utcDay/1000], sliceTerm{day, 1})
		addHours(day, utcDay, inFrom, -1, false)
		addHours(day, inTo, utcEnd, -1, false)
		addHours(day, from, inFrom, 1, true)
		addHours(day, inTo, to, 1, true)
	}
	for h := sums.trailingFrom; h < floorRollupHour(sums.upTo); h += rollupHourMS {
		if plan.rolledUp(h) {
			sp.errorHours[h/1000] = true
		}
	}
	return sp
}

// bucketRanges merges sorted bucket starts of one grain into ranges.
func bucketRanges(buckets []int64, step int64) [][2]int64 {
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
	var ranges [][2]int64
	for _, bucket := range buckets {
		if n := len(ranges); n > 0 && ranges[n-1][1] == bucket {
			ranges[n-1][1] = bucket + step
			continue
		}
		ranges = append(ranges, [2]int64{bucket, bucket + step})
	}
	return ranges
}

// overviewKeySlices reads the key slice rows of the slice plan in one
// statement of bounded range arms.
func (r *rollupRead) overviewKeySlices(ctx context.Context, plan rollupPlan, sums *overviewSums) error {
	sp := planOverviewSlices(plan, sums)
	hourSet := map[int64]bool{}
	for hour := range sp.hours {
		hourSet[hour] = true
	}
	for hour := range sp.errorHours {
		hourSet[hour] = true
	}
	hours := make([]int64, 0, len(hourSet))
	for hour := range hourSet {
		hours = append(hours, hour)
	}
	days := make([]int64, 0, len(sp.days))
	for day := range sp.days {
		days = append(days, day)
	}
	hourRanges, dayRanges := bucketRanges(hours, 3600), bucketRanges(days, 86400)
	if len(hourRanges)+len(dayRanges) > overviewSliceMaxRanges && len(hourRanges) > 0 {
		hourRanges = [][2]int64{{hourRanges[0][0], hourRanges[len(hourRanges)-1][1]}}
	}
	if len(hourRanges)+len(dayRanges) == 0 {
		return nil
	}
	const arm = `SELECT grain,bucket_epoch,dim,requests,errors,cost_microdollars FROM usage_rollup_slice
 WHERE kind='key' AND grain=? AND bucket_epoch>=? AND bucket_epoch<?`
	var arms []string
	var args []any
	for _, grain := range []struct {
		seconds int64
		ranges  [][2]int64
	}{{3600, hourRanges}, {86400, dayRanges}} {
		for _, bounds := range grain.ranges {
			arms = append(arms, arm)
			args = append(args, grain.seconds, bounds[0], bounds[1])
		}
	}
	rows, err := r.tx.QueryContext(ctx, strings.Join(arms, "\n UNION ALL "), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grain, bucket, requests, errors, micro int64
		var dim string
		if err := rows.Scan(&grain, &bucket, &dim, &requests, &errors, &micro); err != nil {
			return err
		}
		terms := sp.days[bucket]
		if grain == 3600 {
			terms = sp.hours[bucket]
			if sp.errorHours[bucket] {
				sums.addErrors(sums.key(dim), bucket*1000, requests, errors)
			}
		}
		if len(terms) == 0 {
			continue
		}
		key := sums.key(dim)
		for _, term := range terms {
			key.days[term.day].requests += term.sign * requests
			key.days[term.day].micro += term.sign * micro
		}
	}
	return rows.Err()
}

// overviewDisconnects removes client disconnects from the folded error
// counts of the error windows.
func (r *rollupRead) overviewDisconnects(ctx context.Context, plan rollupPlan, sums *overviewSums) error {
	rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,api_key_dim,sum(errors) FROM usage_rollup_error_hour
 WHERE bucket_epoch>=? AND bucket_epoch<? AND error_code=? GROUP BY bucket_epoch,api_key_dim`,
		sums.trailingFrom/1000, floorRollupHour(sums.upTo)/1000, clientDisconnected)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, errors int64
		var dim string
		if err := rows.Scan(&hour, &dim, &errors); err != nil {
			return err
		}
		if !plan.rollupHour(hour) {
			continue
		}
		sums.addErrors(&sums.pool, hour*1000, 0, -errors)
		sums.addErrors(sums.key(dim), hour*1000, 0, -errors)
	}
	return rows.Err()
}

// overviewUsageFromEvents is the raw evaluation of OverviewUsage over the
// events from from to now: the fallback reads from today's midnight or the
// trailing window, whichever is earlier, and reports no other days; tests
// read every day as the oracle of the rollup reader.
func overviewUsageFromEvents(ctx context.Context, tx *sql.Tx, q domain.OverviewUsageQuery, from time.Time) (domain.OverviewUsage, error) {
	sums := newOverviewSums(q)
	daysKnown := !from.After(q.DayStarts[0])
	if !daysKnown {
		// Only today is reported: earlier days stay zero and yesterday unknown.
		sums.yesterdayFrom, sums.yesterdayTo = 0, 0
	}
	legacyFrom := from
	if !daysKnown {
		legacyFrom = q.DayStarts[len(q.DayStarts)-1]
	}
	if legacyFrom.Before(q.Now) {
		if err := sums.addLegacy(ctx, tx, legacyFrom, q.Now); err != nil {
			return domain.OverviewUsage{}, err
		}
	}
	where, args := reportEventWhere(reportFilterDims{})
	rows, err := tx.QueryContext(ctx, `SELECT 1,`+rollupImageSQL("e")+` FROM usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=? AND e.requested_at<? AND `+where, append([]any{millis(from), millis(q.Now)}, args...)...)
	if err != nil {
		return domain.OverviewUsage{}, err
	}
	defer rows.Close()
	todayFrom := millis(q.DayStarts[len(q.DayStarts)-1])
	for rows.Next() {
		var img rollupImage
		var sign int64
		if err := rows.Scan(append([]any{&sign}, img.fields()...)...); err != nil {
			return domain.OverviewUsage{}, err
		}
		if !daysKnown && img.at < todayFrom {
			// Before today only the error windows count.
			errored, _ := rollupStatusClass(img.status)
			if img.errorCode == clientDisconnected {
				errored = 0
			}
			sums.addErrors(&sums.pool, img.at, 1, errored)
			sums.addErrors(sums.key(rawReportDimension(img.key)), img.at, 1, errored)
			continue
		}
		sums.addImage(&img, sign)
	}
	if err := rows.Err(); err != nil {
		return domain.OverviewUsage{}, err
	}
	return sums.result(false, daysKnown), nil
}
