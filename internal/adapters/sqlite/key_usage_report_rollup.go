package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"codex-lb/internal/domain"
)

// KeyUsageReport reads, in one analytics snapshot, every group, the current
// keys of the selection with their projected limits, per-day measures of each
// resolved key for the local days of the query and the previous period's
// totals (dashboard redesign use case 5, "resolve the selection --> read the
// period"). The rollup reader combines key slices, folded legacy hours, the
// journal, the unfolded tail and raw partial hours in a fixed number of
// statements; the raw reader evaluates the same measures from events.
func (s *Store) KeyUsageReport(ctx context.Context, q domain.KeyUsageQuery) (domain.KeyUsageData, error) {
	if err := checkedKeyUsageQuery(q); err != nil {
		return domain.KeyUsageData{}, err
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.KeyUsageData, error) {
		sums, err := newKeyUsageSums(ctx, r.tx, q)
		if err != nil {
			return domain.KeyUsageData{}, err
		}
		if err := r.keyUsage(ctx, sums); err != nil {
			return domain.KeyUsageData{}, err
		}
		return sums.result(true), nil
	}, func(tx *sql.Tx) (domain.KeyUsageData, error) {
		sums, err := newKeyUsageSums(ctx, tx, q)
		if err != nil {
			return domain.KeyUsageData{}, err
		}
		if err := sums.readEvents(ctx, tx); err != nil {
			return domain.KeyUsageData{}, err
		}
		return sums.result(false), nil
	})
}

func checkedKeyUsageQuery(q domain.KeyUsageQuery) error {
	n := len(q.DayStarts)
	if n == 0 || n > domain.KeyUsageMaxDays || q.Now.IsZero() || !q.DayStarts[n-1].Before(q.End) ||
		(!q.SkipPrevious && !q.PreviousStart.Before(q.DayStarts[0])) {
		return fmt.Errorf("key usage query: %w", ErrInvalid)
	}
	for i := 1; i < n; i++ {
		if !q.DayStarts[i-1].Before(q.DayStarts[i]) {
			return fmt.Errorf("key usage days: %w", ErrInvalid)
		}
	}
	return nil
}

// keyUsageCell sums one key and day: rollup or image measures with raw
// microdollars and the folded legacy costs, combined like every report.
type keyUsageCell struct {
	m      rollupMeasures
	legacy kbnSum
}

// keyUsageSums accumulates the report of one snapshot over day bounds and the
// previous period in UTC milliseconds.
type keyUsageSums struct {
	q                domain.KeyUsageQuery
	groups           []domain.OverviewGroupRef
	keys             []domain.OverviewKey
	bounds           []int64 // day starts, then the end
	prevFrom, prevTo int64
	from             int64 // first instant read: the previous period's start or the first day
	index            map[string]int
	days             [][]keyUsageCell
	previous         keyUsageCell
	// filter is the resolved keys when the selection is a strict subset;
	// every read then restricts the key dimension to them.
	filter []string
}

// newKeyUsageSums is the step "resolve groups to their current member keys
// and unite them with the explicit keys and the ungrouped set", read in the
// report's snapshot.
func newKeyUsageSums(ctx context.Context, tx *sql.Tx, q domain.KeyUsageQuery) (*keyUsageSums, error) {
	groups, err := listGroupRefs(ctx, tx)
	if err != nil {
		return nil, err
	}
	keys, err := listKeyRows(ctx, tx, q.Now)
	if err != nil {
		return nil, err
	}
	resolved := q.Selection.Resolve(keys)
	if len(resolved)*len(q.DayStarts) > domain.KeyUsageMaxKeyDays {
		return nil, domain.ErrReportTooLarge
	}
	sums := &keyUsageSums{q: q, groups: groups, keys: resolved, index: make(map[string]int, len(resolved)),
		days: make([][]keyUsageCell, len(resolved))}
	for _, start := range q.DayStarts {
		sums.bounds = append(sums.bounds, millis(start))
	}
	sums.bounds = append(sums.bounds, millis(q.End))
	sums.from = sums.bounds[0]
	if !q.SkipPrevious {
		sums.prevFrom, sums.prevTo = millis(q.PreviousStart), sums.bounds[0]
		sums.from = sums.prevFrom
	}
	for i, key := range resolved {
		sums.index[rollupDimValue(key.ID)] = i
	}
	if !q.Selection.Empty() {
		sums.filter = make([]string, len(resolved))
		for i, key := range resolved {
			sums.filter[i] = key.ID
		}
	}
	return sums, nil
}

// none reports a strict-subset selection without keys: nothing to read.
func (k *keyUsageSums) none() bool { return k.filter != nil && len(k.filter) == 0 }

// day returns the local day holding instant at.
func (k *keyUsageSums) day(at int64) (int, bool) {
	if at < k.bounds[0] || at >= k.bounds[len(k.bounds)-1] {
		return 0, false
	}
	lo, hi := 0, len(k.bounds)-1
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if k.bounds[mid] <= at {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, true
}

// cell returns the cell of a key dimension and day (-1 is the previous
// period), or nil for a dimension outside the selection.
func (k *keyUsageSums) cell(dim string, day int) *keyUsageCell {
	i, ok := k.index[dim]
	if !ok {
		return nil
	}
	if day < 0 {
		return &k.previous
	}
	if k.days[i] == nil {
		k.days[i] = make([]keyUsageCell, len(k.bounds)-1)
	}
	return &k.days[i][day]
}

// cellAt returns the cell of a key dimension at an instant, or nil.
func (k *keyUsageSums) cellAt(dim string, at int64) *keyUsageCell {
	if d, ok := k.day(at); ok {
		return k.cell(dim, d)
	}
	if at >= k.prevFrom && at < k.prevTo {
		return k.cell(dim, -1)
	}
	return nil
}

func (k *keyUsageSums) addImage(img *rollupImage, sign int64) {
	if cell := k.cellAt(rawReportDimension(img.key), img.at); cell != nil {
		cell.m.add(img, sign)
	}
}

// addLegacy adds folded legacy hours of the read window: each hour belongs to
// the day holding its start, as in every report.
func (k *keyUsageSums) addLegacy(ctx context.Context, q analyticsQuerier) error {
	facts, args := legacyReportFacts(domain.ReportFilter{APIKeyIDs: k.filter}, fromMillis(k.from), fromMillis(k.bounds[len(k.bounds)-1]))
	rows, err := q.QueryContext(ctx, facts+`SELECT at_ms,api_key_id,coalesce(sum(request_count),0),
 coalesce(sum(error_count),0),coalesce(sum(cancelled_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(reasoning_tokens),0),coalesce(sum(reasoning_known_count),0),
 coalesce(sum(cached_input_tokens),0),coalesce(sum(cost_usd),0)
 FROM facts GROUP BY at_ms,api_key_id ORDER BY at_ms,api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at int64
		var dim string
		var m rollupMeasures
		var cost float64
		if err := rows.Scan(&at, &dim, &m.requests, &m.errors, &m.cancelled, &m.input, &m.output, &m.reasoning,
			&m.known, &m.cached, &cost); err != nil {
			return err
		}
		if cell := k.cellAt(dim, at); cell != nil {
			cell.m = cell.m.plus(m)
			cell.legacy.add(cost)
		}
	}
	return rows.Err()
}

func (k *keyUsageSums) eventWhere() (string, []any) {
	return reportEventWhere(reportFilterDims{keys: k.filter})
}

// readEvents is the raw evaluation: folded legacy hours and every reported
// event of the selection in the read window.
func (k *keyUsageSums) readEvents(ctx context.Context, tx *sql.Tx) error {
	if k.none() {
		return nil
	}
	if err := k.addLegacy(ctx, tx); err != nil {
		return err
	}
	where, args := k.eventWhere()
	rows, err := tx.QueryContext(ctx, `SELECT 1,`+rollupImageSQL("e")+` FROM usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=? AND e.requested_at<? AND `+where, append([]any{k.from, k.bounds[len(k.bounds)-1]}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var img rollupImage
		var sign int64
		if err := rows.Scan(append([]any{&sign}, img.fields()...)...); err != nil {
			return err
		}
		k.addImage(&img, sign)
	}
	return rows.Err()
}

func (k *keyUsageSums) result(rollups bool) domain.KeyUsageData {
	data := domain.KeyUsageData{Groups: k.groups, Keys: k.keys, Days: map[string][]domain.UsageSums{},
		Previous: k.previous.sums(), RollupsUsable: rollups}
	if data.Groups == nil {
		data.Groups = []domain.OverviewGroupRef{}
	}
	if data.Keys == nil {
		data.Keys = []domain.OverviewKey{}
	}
	for i, cells := range k.days {
		if cells == nil || !slices.ContainsFunc(cells, func(c keyUsageCell) bool { return !c.zero() }) {
			// Corrections that cancel out leave no usage, as raw events do.
			k.days[i] = nil
			continue
		}
		days := make([]domain.UsageSums, len(cells))
		for d := range cells {
			days[d] = cells[d].sums()
		}
		data.Days[k.keys[i].ID] = days
		k.days[i] = nil
	}
	return data
}

func (c keyUsageCell) zero() bool { return c.m == rollupMeasures{} && c.legacy == kbnSum{} }

func (c *keyUsageCell) sums() domain.UsageSums {
	return domain.UsageSums{Requests: c.m.requests, InputTokens: c.m.input, OutputTokens: c.m.output,
		CachedInputTokens: c.m.cached, ReasoningTokens: c.m.reasoning, ReasoningKnown: c.m.known,
		Errors: c.m.errors, Cancelled: c.m.cancelled, CostUSD: reportCost{legacy: c.legacy, micro: c.m.cost}.value()}
}

// keyUsage is the rollup reader of KeyUsageReport: folded legacy hours, images
// for raw hours, the tail and the journal, and the key slice rows of the slice
// plan in one statement.
func (r *rollupRead) keyUsage(ctx context.Context, sums *keyUsageSums) error {
	if sums.none() {
		return nil
	}
	if err := sums.addLegacy(ctx, r.tx); err != nil {
		return err
	}
	splits := append([]int64{}, sums.bounds...)
	if sums.prevTo > sums.prevFrom {
		splits = append(splits, sums.prevFrom)
	}
	plan := planRollupWindow(sums.from, sums.bounds[len(sums.bounds)-1], splits)
	where, args := sums.eventWhere()
	if err := r.keyUsageRawSpans(ctx, plan, sums, where, args); err != nil {
		return err
	}
	if err := r.unfoldedImages(ctx, plan, where, args, sums.addImage); err != nil {
		return err
	}
	return r.keyUsageSlices(ctx, planKeyUsageSlices(plan, sums), sums)
}

// keyUsageRawSpans reads the raw spans of plan (partial hours and the split
// hours of fractional-offset zones) as per-key sums in SQL: the spans are cut
// at the day bounds so that each part belongs to one day or the previous
// period, which keeps a split hour from being decoded event by event.
func (r *rollupRead) keyUsageRawSpans(ctx context.Context, plan rollupPlan, sums *keyUsageSums, where string, args []any) error {
	cuts := append([]int64{sums.prevFrom, sums.prevTo}, sums.bounds...)
	var parts [][2]int64
	for _, span := range plan.raws() {
		from := span.from
		for _, cut := range cuts {
			if cut > from && cut < span.to {
				parts = append(parts, [2]int64{from, cut})
				from = cut
			}
		}
		parts = append(parts, [2]int64{from, span.to})
	}
	if len(parts) == 0 {
		return nil
	}
	values := strings.TrimSuffix(strings.Repeat("(?,?),", len(parts)), ",")
	spanArgs := make([]any, 0, 2*len(parts)+len(args))
	for _, part := range parts {
		spanArgs = append(spanArgs, part[0], part[1])
	}
	rows, err := r.tx.QueryContext(ctx, `WITH spans(lo,hi) AS (VALUES `+values+`)
 SELECT s.lo,e.api_key_id,count(*),coalesce(sum(CASE WHEN e.status NOT IN ('success','completed','cancelled') THEN 1 ELSE 0 END),0),
 coalesce(sum(CASE WHEN e.status='cancelled' THEN 1 ELSE 0 END),0),coalesce(sum(e.input_tokens),0),coalesce(sum(e.output_tokens),0),
 coalesce(sum(e.reasoning_tokens),0),coalesce(sum(e.reasoning_tokens_known),0),
 coalesce(sum(max(0,min(e.cached_input_tokens,e.input_tokens))),0),coalesce(sum(e.cost_microdollars),0)
 FROM spans s CROSS JOIN usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=s.lo AND e.requested_at<s.hi AND `+where+` GROUP BY s.lo,e.api_key_id`, append(spanArgs, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at int64
		var key sql.NullString
		var m rollupMeasures
		if err := rows.Scan(append([]any{&at, &key}, m.fields()...)...); err != nil {
			return err
		}
		if cell := sums.cellAt(rawReportDimension(key), at); cell != nil {
			cell.m = cell.m.plus(m)
		}
	}
	return rows.Err()
}

// keyUsageBuckets maps slice buckets (seconds) of each grain to the days they
// contribute to; day -1 is the previous period.
type keyUsageBuckets struct {
	hours, days map[int64][]sliceTerm
}

func (b keyUsageBuckets) hour(at int64, day int, sign int64) {
	b.hours[at/1000] = append(b.hours[at/1000], sliceTerm{day, sign})
}

const rollupDayMS = 24 * rollupHourMS

// planKeyUsageSlices is the step "local-day sums per key" of use case 2.1
// for every day of a report: the rollup hours of a local day are read as the
// UTC-day row D of its midpoint minus the hour rows of D outside them plus
// their hour rows outside D, when every hour of D inside the day is a rollup
// hour and that reads fewer rows than the rollup hours themselves; otherwise
// as their hour rows. Raw split hours at fractional-zone midnights stay with
// the images. The previous period reads UTC-day rows for whole UTC days of
// each rollup span and hour rows for the rest.
func planKeyUsageSlices(plan rollupPlan, sums *keyUsageSums) keyUsageBuckets {
	b := keyUsageBuckets{hours: map[int64][]sliceTerm{}, days: map[int64][]sliceTerm{}}
	for day := 0; day+1 < len(sums.bounds); day++ {
		from, to := sums.bounds[day], sums.bounds[day+1]
		hourFrom, hourTo := floorRollupHour(from+rollupHourMS-1), floorRollupHour(to)
		var rolled []int64
		for h := hourFrom; h < hourTo; h += rollupHourMS {
			if plan.rolledUp(h) {
				rolled = append(rolled, h)
			}
		}
		if len(rolled) == 0 {
			continue
		}
		utcDay := floorRollupDay(from + (to-from)/2)
		utcEnd := utcDay + rollupDayMS
		inFrom, inTo := max(hourFrom, utcDay), min(hourTo, utcEnd)
		usable := inFrom < inTo
		for h := inFrom; usable && h < inTo; h += rollupHourMS {
			usable = plan.rolledUp(h)
		}
		outside := 0
		for _, h := range rolled {
			if h < utcDay || h >= utcEnd {
				outside++
			}
		}
		if usable && 1+int((rollupDayMS-(inTo-inFrom))/rollupHourMS)+outside < len(rolled) {
			b.days[utcDay/1000] = append(b.days[utcDay/1000], sliceTerm{day, 1})
			for h := utcDay; h < inFrom; h += rollupHourMS {
				b.hour(h, day, -1)
			}
			for h := inTo; h < utcEnd; h += rollupHourMS {
				b.hour(h, day, -1)
			}
			for _, h := range rolled {
				if h < utcDay || h >= utcEnd {
					b.hour(h, day, 1)
				}
			}
			continue
		}
		for _, h := range rolled {
			b.hour(h, day, 1)
		}
	}
	if sums.prevTo > sums.prevFrom {
		for _, span := range plan {
			lo, hi := max(span.from, sums.prevFrom), min(span.to, sums.prevTo)
			if span.raw || lo >= hi {
				continue
			}
			dayFrom, dayTo := floorRollupDay(lo+rollupDayMS-1), floorRollupDay(hi)
			if dayFrom >= dayTo {
				dayFrom, dayTo = hi, hi
			}
			for h := lo; h < dayFrom; h += rollupHourMS {
				b.hour(h, -1, 1)
			}
			for d := dayFrom; d < dayTo; d += rollupDayMS {
				b.days[d/1000] = append(b.days[d/1000], sliceTerm{-1, 1})
			}
			for h := max(dayTo, lo); h < hi; h += rollupHourMS {
				b.hour(h, -1, 1)
			}
		}
	}
	return b
}

func floorRollupDay(ms int64) int64 {
	day := ms / rollupDayMS
	if ms%rollupDayMS < 0 {
		day--
	}
	return day * rollupDayMS
}

// keyUsageSlices reads the key slice rows of the buckets in one statement:
// the bucket lists travel as JSON arrays and drive primary-key lookups.
func (r *rollupRead) keyUsageSlices(ctx context.Context, b keyUsageBuckets, sums *keyUsageSums) error {
	if len(b.hours)+len(b.days) == 0 {
		return nil
	}
	hours, err := json.Marshal(mapKeys(b.hours))
	if err != nil {
		return err
	}
	days, err := json.Marshal(mapKeys(b.days))
	if err != nil {
		return err
	}
	query := `WITH b(grain,bucket) AS (SELECT 3600,value FROM json_each(?) UNION ALL SELECT 86400,value FROM json_each(?))
 SELECT s.grain,s.bucket_epoch,s.dim,` + rollupMeasureColumnsOf("s") + `
 FROM b CROSS JOIN usage_rollup_slice s WHERE s.kind='key' AND s.grain=b.grain AND s.bucket_epoch=b.bucket`
	args := []any{string(hours), string(days)}
	if sums.filter != nil {
		dims, err := json.Marshal(rollupDimValues(sums.filter))
		if err != nil {
			return err
		}
		query += ` AND s.dim IN (SELECT value FROM json_each(?))`
		args = append(args, string(dims))
	}
	rows, err := r.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grain, bucket int64
		var dim string
		var m rollupMeasures
		if err := rows.Scan(append([]any{&grain, &bucket, &dim}, m.fields()...)...); err != nil {
			return err
		}
		terms := b.days[bucket]
		if grain == 3600 {
			terms = b.hours[bucket]
		}
		for _, term := range terms {
			cell := sums.cell(dim, term.day)
			if cell == nil {
				break
			}
			cell.m = cell.m.plus(signedMeasures(m, term.sign))
		}
	}
	return rows.Err()
}

func signedMeasures(m rollupMeasures, sign int64) rollupMeasures {
	if sign == 1 {
		return m
	}
	return rollupMeasures{sign * m.requests, sign * m.errors, sign * m.cancelled, sign * m.input, sign * m.output,
		sign * m.reasoning, sign * m.known, sign * m.cached, sign * m.cost}
}

func mapKeys(m map[int64][]sliceTerm) []int64 {
	keys := make([]int64, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
