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

// keyPageMaxDays bounds the local days of one key page read (730 report days
// plus a daylight-saving margin).
const keyPageMaxDays = 732

// keyPageMaxPaceModels bounds the model filters of one pace read.
const keyPageMaxPaceModels = 64

// KeyPageUsage returns one key's usage for the key page (dashboard redesign
// use case 4, "read the key limits and period rollups in one snapshot"): the
// period by local day, model, user agent and account with latency
// histograms and the previous period (keyPeriodUsage), the period's error
// codes (keyErrorCodes), the limit pace window (keyLimitPace) and the first
// page of recent requests, in one analytics snapshot. The rollup readers use
// a number of statements independent of the period; while the rollups are
// unusable the same values come from the key's raw events.
func (s *Store) KeyPageUsage(ctx context.Context, q domain.KeyPageUsageQuery) (domain.KeyPageUsage, error) {
	if err := checkedKeyPageQuery(q); err != nil {
		return domain.KeyPageUsage{}, err
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.KeyPageUsage, error) {
		return r.keyPageUsage(ctx, q)
	}, func(tx *sql.Tx) (domain.KeyPageUsage, error) {
		return keyPageUsageRaw(ctx, tx, q)
	})
}

func checkedKeyPageQuery(q domain.KeyPageUsageQuery) error {
	n := len(q.DayStarts)
	if q.KeyID == "" || n == 0 || n > keyPageMaxDays || !q.PreviousStart.Before(q.DayStarts[0]) ||
		!q.DayStarts[n-1].Before(q.End) || !q.PaceStart.Before(q.Now) || len(q.PaceModels) > keyPageMaxPaceModels ||
		q.Recent.KeyID != q.KeyID {
		return fmt.Errorf("key page query: %w", ErrInvalid)
	}
	for i := 1; i < n; i++ {
		if !q.DayStarts[i-1].Before(q.DayStarts[i]) {
			return fmt.Errorf("key page days: %w", ErrInvalid)
		}
	}
	return checkedKeyRequestsQuery(q.Recent)
}

func checkedKeyRequestsQuery(q domain.KeyRequestsQuery) error {
	if q.KeyID == "" || q.Limit < 1 || q.Limit > domain.KeyRequestsMaxLimit {
		return fmt.Errorf("key requests query: %w", ErrInvalid)
	}
	return nil
}

// keyScope sums measures with raw microdollars and folded legacy costs apart,
// combined like every report.
type keyScope struct {
	m      rollupMeasures
	legacy float64
}

func (s *keyScope) add(m rollupMeasures, legacy float64) {
	s.m = s.m.plus(m)
	s.legacy += legacy
}

func (s keyScope) cost() float64 { return roundReport(reportCostUSD(s.legacy, s.m.cost), 6) }

func (s keyScope) measures() domain.KeyUsageMeasures {
	return domain.KeyUsageMeasures{Requests: s.m.requests, Errors: s.m.errors, Cancelled: s.m.cancelled,
		InputTokens: s.m.input, OutputTokens: s.m.output, CachedInputTokens: s.m.cached,
		ReasoningTokens: s.m.reasoning, ReasoningKnown: s.m.known, CostUSD: s.cost()}
}

// denseHist counts histogram buckets by index: merging thousands of rollup
// rows into a few scopes costs one addition per bucket and no allocation.
type denseHist []int64

func (d *denseHist) addIndex(index int, n int64) {
	if n == 0 {
		return
	}
	if index >= len(*d) {
		grown := make(denseHist, index+1, max(index+1, 2*len(*d)))
		copy(grown, *d)
		*d = grown
	}
	(*d)[index] += n
}

func (d *denseHist) add(v, n int64) { d.addIndex(rollupHistIndex(v), n) }

func (d *denseHist) addHist(h rollupHist) {
	for _, bucket := range h {
		d.addIndex(bucket.index, bucket.count)
	}
}

// quantile is histQuantile of the counts divided by scale and rounded to two
// places; nil without samples.
func (d denseHist) quantile(q, scale float64) *float64 {
	var h rollupHist
	for index, n := range d {
		if n != 0 {
			h = append(h, rollupHistBucket{index: index, count: n})
		}
	}
	if h.total() <= 0 {
		return nil
	}
	value := roundReport(histQuantile(h, q)/scale, 2)
	return &value
}

// keyLatency holds the latency histograms of a scope.
type keyLatency struct{ ttft, queue, tps, total denseHist }

// observe adds one event's samples with the guards of rollupLatency.observe.
func (l *keyLatency) observe(ttft sql.NullInt64, queue, output, total, sign int64) {
	if ttft.Valid && ttft.Int64 > 0 {
		l.ttft.add(ttft.Int64, sign)
		if output > 0 && total > ttft.Int64 {
			l.tps.add(rollupTPSSample(output, total-ttft.Int64), sign)
		}
	}
	if queue >= 0 {
		l.queue.add(queue, sign)
	}
	if total > 0 {
		l.total.add(total, sign)
	}
}

func (l *keyLatency) addRow(row rollupLatency) {
	l.ttft.addHist(row.ttft)
	l.queue.addHist(row.queue)
	l.tps.addHist(row.tps)
	l.total.addHist(row.total)
}

func (l keyLatency) result() domain.KeyLatency {
	return domain.KeyLatency{TTFTP50: l.ttft.quantile(0.5, 1), TTFTP95: l.ttft.quantile(0.95, 1),
		TotalP50: l.total.quantile(0.5, 1), TotalP95: l.total.quantile(0.95, 1),
		QueueP95: l.queue.quantile(0.95, 1), TPSP50: l.tps.quantile(0.5, rollupTPSScale)}
}

type keyDaySums struct {
	keyScope
	latency keyLatency
	models  map[string]*keyScope
}

type keyModelSums struct {
	keyScope
	ttft denseHist
}

// keyAccountDim is an account in the raw report encoding with the deleted
// flag of discarded history.
type keyAccountDim struct {
	dim     string
	deleted bool
}

// keyPeriodSums accumulates one key's period and previous period over UTC
// millisecond bounds: legacy hours, rollup rows and signed event images
// alike, so both read paths end in identical sums.
type keyPeriodSums struct {
	bounds        []int64 // day starts, then the end of the last day
	previousFrom  int64
	days          []keyDaySums
	total         keyScope
	totalLatency  keyLatency
	previous      keyScope
	models        map[string]*keyModelSums
	agents        map[string]*keyScope
	accounts      map[keyAccountDim]*keyScope
	codes         map[string]map[int64]int64 // code -> hour (seconds) -> errors
	legacyThrough int64
}

func newKeyPeriodSums(q domain.KeyPageUsageQuery) *keyPeriodSums {
	sums := &keyPeriodSums{previousFrom: millis(q.PreviousStart), days: make([]keyDaySums, len(q.DayStarts)),
		models: map[string]*keyModelSums{}, agents: map[string]*keyScope{}, accounts: map[keyAccountDim]*keyScope{},
		codes: map[string]map[int64]int64{}}
	for _, start := range q.DayStarts {
		sums.bounds = append(sums.bounds, millis(start))
	}
	sums.bounds = append(sums.bounds, millis(q.End))
	for i := range sums.days {
		sums.days[i].models = map[string]*keyScope{}
	}
	return sums
}

func (k *keyPeriodSums) start() int64 { return k.bounds[0] }
func (k *keyPeriodSums) end() int64   { return k.bounds[len(k.bounds)-1] }

// day returns the local day holding instant at.
func (k *keyPeriodSums) day(at int64) (int, bool) {
	i := sort.Search(len(k.bounds), func(i int) bool { return k.bounds[i] > at }) - 1
	return i, i >= 0 && i < len(k.bounds)-1
}

// splits are the instants the period attributes to different scopes.
func (k *keyPeriodSums) splits() []int64 { return append([]int64{}, k.bounds...) }

// add counts measures of instant at (an event, or the start of a rollup or
// legacy hour) in the previous period, or in its day, model, user-agent and
// account scopes; agent is empty for legacy hours, which have none. It
// returns the day and model scopes of a period instant.
func (k *keyPeriodSums) add(at int64, model, agent string, account keyAccountDim, m rollupMeasures, legacy float64) (*keyDaySums, *keyModelSums) {
	if at >= k.previousFrom && at < k.start() {
		k.previous.add(m, legacy)
		return nil, nil
	}
	d, ok := k.day(at)
	if !ok {
		return nil, nil
	}
	day := &k.days[d]
	day.add(m, legacy)
	k.total.add(m, legacy)
	dayModel := day.models[model]
	if dayModel == nil {
		dayModel = &keyScope{}
		day.models[model] = dayModel
	}
	dayModel.add(m, legacy)
	periodModel := k.models[model]
	if periodModel == nil {
		periodModel = &keyModelSums{}
		k.models[model] = periodModel
	}
	periodModel.add(m, legacy)
	if agent != "" {
		scope := k.agents[agent]
		if scope == nil {
			scope = &keyScope{}
			k.agents[agent] = scope
		}
		scope.add(m, legacy)
	}
	scope := k.accounts[account]
	if scope == nil {
		scope = &keyScope{}
		k.accounts[account] = scope
	}
	scope.add(m, legacy)
	return day, periodModel
}

// addImage counts one signed event image; sign 0 is a cost-only delta.
func (k *keyPeriodSums) addImage(img *rollupImage, sign int64) {
	var m rollupMeasures
	m.add(img, sign)
	day, model := k.add(img.at, img.model, reportUserAgentLabel(img.userAgent),
		keyAccountDim{rawReportDimension(img.account), img.deleted}, m, 0)
	if day == nil || sign == 0 {
		return
	}
	day.latency.observe(img.ttft, img.queue, img.output, img.total, sign)
	k.totalLatency.observe(img.ttft, img.queue, img.output, img.total, sign)
	if img.ttft.Valid && img.ttft.Int64 > 0 {
		model.ttft.add(img.ttft.Int64, sign)
	}
	if errored, _ := rollupStatusClass(img.status); errored == 1 && img.errorCode != "" {
		k.addCode(img.errorCode, img.at/rollupHourMS*3600, sign)
	}
}

// addRow counts one cube row of a rollup hour (seconds).
func (k *keyPeriodSums) addRow(hour int64, account, model, agent string, deleted bool, m rollupMeasures, latency rollupLatency) {
	day, periodModel := k.add(hour*1000, model, reportUserAgentLabel(decodeRollupDim(agent)), keyAccountDim{account, deleted}, m, 0)
	if day == nil {
		return
	}
	day.latency.addRow(latency)
	k.totalLatency.addRow(latency)
	periodModel.ttft.addHist(latency.ttft)
}

func (k *keyPeriodSums) addCode(code string, hour, n int64) {
	hours := k.codes[code]
	if hours == nil {
		hours = map[int64]int64{}
		k.codes[code] = hours
	}
	hours[hour] += n
}

// addLegacy adds the key's folded legacy hours of both periods; each hour
// belongs to the day holding its start, as in every report.
func (k *keyPeriodSums) addLegacy(ctx context.Context, q analyticsQuerier, keyID string) error {
	where, args := hourlyReportWhere(domain.ReportFilter{APIKeyIDs: []string{keyID}}, fromMillis(k.previousFrom), fromMillis(k.end()))
	rows, err := q.QueryContext(ctx, `SELECT h.bucket_epoch,h.account_id,h.is_deleted,h.model,
 sum(h.request_count),sum(h.error_count),sum(h.cancelled_count),sum(h.input_tokens),sum(h.output_or_reasoning_tokens),
 sum(h.reasoning_tokens),sum(h.reasoning_known_count),sum(h.cached_input_tokens_clamped),sum(h.cost_usd)
 FROM legacy_hourly_usage h WHERE `+where+` GROUP BY 1,2,3,4 ORDER BY 1,2,3,4`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour int64
		var account, model string
		var deleted bool
		var m rollupMeasures
		var cost float64
		if err := rows.Scan(&hour, &account, &deleted, &model, &m.requests, &m.errors, &m.cancelled, &m.input, &m.output,
			&m.reasoning, &m.known, &m.cached, &cost); err != nil {
			return err
		}
		if day, _ := k.add(hour*1000, model, "", keyAccountDim{account, deleted}, m, cost); day != nil {
			k.legacyThrough = max(k.legacyThrough, hour*1000+rollupHourMS)
		}
	}
	return rows.Err()
}

func (k *keyPeriodSums) result(rollups bool) domain.KeyPageUsage {
	usage := domain.KeyPageUsage{RollupsUsable: rollups, Days: make([]domain.KeyUsageDay, len(k.days)),
		Total: k.total.measures(), TotalLatency: k.totalLatency.result(), Previous: k.previous.measures(),
		Models: []domain.KeyModelUsage{}, UserAgents: []domain.KeyUserAgentUsage{}, Accounts: []domain.KeyAccountUsage{},
		ErrorCodes: []domain.KeyErrorCode{}}
	for i := range k.days {
		day := domain.KeyUsageDay{KeyUsageMeasures: k.days[i].measures(), Latency: k.days[i].latency.result(),
			Models: []domain.KeyModelAmount{}}
		for model, scope := range k.days[i].models {
			if scope.m.requests > 0 {
				day.Models = append(day.Models, domain.KeyModelAmount{Model: model, Requests: scope.m.requests,
					Tokens: scope.m.input + scope.m.output, CostUSD: scope.cost()})
			}
		}
		sort.Slice(day.Models, func(a, b int) bool {
			return keyAmountLess(day.Models[a].CostUSD, day.Models[b].CostUSD, day.Models[a].Requests, day.Models[b].Requests,
				day.Models[a].Model, day.Models[b].Model)
		})
		usage.Days[i] = day
	}
	for model, scope := range k.models {
		if scope.m.requests > 0 {
			usage.Models = append(usage.Models, domain.KeyModelUsage{Model: model, KeyUsageMeasures: scope.measures(),
				TTFTP50: scope.ttft.quantile(0.5, 1)})
		}
	}
	sort.Slice(usage.Models, func(a, b int) bool {
		x, y := usage.Models[a], usage.Models[b]
		return keyAmountLess(x.CostUSD, y.CostUSD, x.Requests, y.Requests, x.Model, y.Model)
	})
	for agent, scope := range k.agents {
		if scope.m.requests > 0 {
			usage.UserAgents = append(usage.UserAgents, domain.KeyUserAgentUsage{Group: agent, Requests: scope.m.requests, CostUSD: scope.cost()})
		}
	}
	sort.Slice(usage.UserAgents, func(a, b int) bool {
		x, y := usage.UserAgents[a], usage.UserAgents[b]
		return keyAmountLess(float64(x.Requests), float64(y.Requests), 0, 0, x.Group, y.Group)
	})
	type accountRow struct {
		dim  keyAccountDim
		cost float64
		n    int64
	}
	var accounts []accountRow
	for dim, scope := range k.accounts {
		if scope.m.requests > 0 {
			accounts = append(accounts, accountRow{dim, scope.cost(), scope.m.requests})
		}
	}
	sort.Slice(accounts, func(a, b int) bool {
		x, y := accounts[a], accounts[b]
		if x.cost != y.cost {
			return x.cost > y.cost
		}
		if x.dim.dim != y.dim.dim {
			return x.dim.dim < y.dim.dim
		}
		return !x.dim.deleted && y.dim.deleted
	})
	for _, row := range accounts {
		entry := domain.KeyAccountUsage{Deleted: row.dim.deleted, Requests: row.n, CostUSD: row.cost}
		if id := decodeRollupDim(row.dim.dim); id.Valid {
			entry.AccountID = &id.String
		}
		usage.Accounts = append(usage.Accounts, entry)
	}
	for code, hours := range k.codes {
		var count, last int64
		seen := false
		for hour, n := range hours {
			count += n
			if n > 0 && (!seen || hour > last) {
				last, seen = hour, true
			}
		}
		if count > 0 && seen {
			usage.ErrorCodes = append(usage.ErrorCodes, domain.KeyErrorCode{Code: code, Count: count, LastSeenAt: time.Unix(last, 0).UTC()})
		}
	}
	sort.Slice(usage.ErrorCodes, func(a, b int) bool {
		x, y := usage.ErrorCodes[a], usage.ErrorCodes[b]
		return keyAmountLess(float64(x.Count), float64(y.Count), 0, 0, x.Code, y.Code)
	})
	if k.legacyThrough > 0 {
		at := fromMillis(k.legacyThrough)
		usage.DetailsFrom = &at
	}
	return usage
}

// keyAmountLess orders by cost, then requests, descending, then by name.
func keyAmountLess(costA, costB float64, requestsA, requestsB int64, nameA, nameB string) bool {
	if costA != costB {
		return costA > costB
	}
	if requestsA != requestsB {
		return requestsA > requestsB
	}
	return nameA < nameB
}

// keyPace accumulates the pace window's consumption.
type keyPace struct {
	all     domain.KeyPaceAmounts
	byModel map[string]domain.KeyPaceAmounts
}

func newKeyPace(models []string) *keyPace {
	pace := &keyPace{byModel: map[string]domain.KeyPaceAmounts{}}
	for _, model := range models {
		pace.byModel[model] = domain.KeyPaceAmounts{}
	}
	return pace
}

func addPace(sum domain.KeyPaceAmounts, input, output, cost int64) domain.KeyPaceAmounts {
	return domain.KeyPaceAmounts{InputTokens: sum.InputTokens + input, OutputTokens: sum.OutputTokens + output,
		CostMicrodollars: sum.CostMicrodollars + cost}
}

func (p *keyPace) addImage(img *rollupImage, sign int64) {
	var m rollupMeasures
	m.add(img, sign)
	p.all = addPace(p.all, m.input, m.output, m.cost)
	if sum, ok := p.byModel[img.model]; ok {
		p.byModel[img.model] = addPace(sum, m.input, m.output, m.cost)
	}
}

func (p *keyPace) result() domain.KeyLimitPace {
	return domain.KeyLimitPace{All: p.all, ByModel: p.byModel}
}

// keyPageUsage is the rollup reader of KeyPageUsage.
func (r *rollupRead) keyPageUsage(ctx context.Context, q domain.KeyPageUsageQuery) (domain.KeyPageUsage, error) {
	sums := newKeyPeriodSums(q)
	if err := sums.addLegacy(ctx, r.tx, q.KeyID); err != nil {
		return domain.KeyPageUsage{}, err
	}
	plan := planRollupWindow(sums.previousFrom, sums.end(), sums.splits())
	if err := r.keyPeriodUsage(ctx, plan, q.KeyID, sums); err != nil {
		return domain.KeyPageUsage{}, err
	}
	if err := r.keyErrorCodes(ctx, plan, q.KeyID, sums); err != nil {
		return domain.KeyPageUsage{}, err
	}
	usage := sums.result(true)
	pace, err := r.keyLimitPace(ctx, q)
	if err != nil {
		return usage, err
	}
	usage.Pace = pace
	usage.Recent, err = keyRecentRequestsTx(ctx, r.tx, q.Recent)
	return usage, err
}

// keyPeriodUsage is the step "period rollups" of use case 4: signed images
// for raw hours, the tail and the journal; the key's cube rows with their
// histograms through the key index for the period's rollup hours; and the
// key's slice rows (UTC days inside, hours at the edges) for the previous
// period, whose rollup hours form one span because no split falls inside it.
func (r *rollupRead) keyPeriodUsage(ctx context.Context, plan rollupPlan, keyID string, sums *keyPeriodSums) error {
	if err := r.keyImages(ctx, plan, keyID, sums.addImage); err != nil {
		return err
	}
	if from, to, ok := keyPlanHours(plan, sums.start(), sums.end()); ok {
		rows, err := r.tx.QueryContext(ctx, `SELECT c.bucket_epoch,c.account_dim,c.model,c.ua_dim,c.deleted,`+
			rollupMeasureColumnsOf("c")+`,c.ttft_hist,c.queue_hist,c.tps_hist,c.total_hist
 FROM usage_rollup_cube_hour c INDEXED BY idx_usage_rollup_cube_key
 WHERE c.api_key_dim=? AND c.bucket_epoch>=? AND c.bucket_epoch<?`, rollupDimValue(keyID), from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hour int64
			var account, model, agent string
			var deleted bool
			var m rollupMeasures
			var ttft, queue, tps, total []byte
			if err := rows.Scan(append(append([]any{&hour, &account, &model, &agent, &deleted}, m.fields()...),
				&ttft, &queue, &tps, &total)...); err != nil {
				return err
			}
			if !plan.rollupHour(hour) {
				continue
			}
			latency, err := decodeRollupLatency(ttft, queue, tps, total)
			if err != nil {
				return err
			}
			sums.addRow(hour, account, model, agent, deleted, m, latency)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return r.keySlices(ctx, planRollupWindow(sums.previousFrom, sums.start(), nil), keyID, func(m rollupMeasures) {
		sums.previous.add(m, 0)
	})
}

// keyPlanHours is the range in seconds of the plan's rollup hours inside
// [from, to) milliseconds.
func keyPlanHours(plan rollupPlan, from, to int64) (int64, int64, bool) {
	first, last, ok := plan.hours()
	if !ok {
		return 0, 0, false
	}
	first = max(first, (from+rollupHourMS-1)/rollupHourMS*3600)
	last = min(last, floorRollupHour(to)/1000)
	return first, last, first < last
}

// keySlices adds the key's slice rows covering the rollup hours of plan:
// day rows for whole UTC days inside one rollup span, hour rows otherwise,
// in one statement through the slice dimension index.
func (r *rollupRead) keySlices(ctx context.Context, plan rollupPlan, keyID string, add func(rollupMeasures)) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	hours, days, bounded := plan.sliceRanges()
	if !bounded {
		hours, days = [][2]int64{{from, to}}, nil
	}
	const arm = `SELECT bucket_epoch,` + rollupMeasureColumns + ` FROM usage_rollup_slice INDEXED BY idx_usage_rollup_slice_dim
 WHERE kind='key' AND dim=? AND grain=? AND bucket_epoch>=? AND bucket_epoch<?`
	var arms []string
	var args []any
	for _, grain := range []struct {
		seconds int64
		ranges  [][2]int64
	}{{3600, hours}, {86400, days}} {
		for _, bounds := range grain.ranges {
			arms = append(arms, arm)
			args = append(args, rollupDimValue(keyID), grain.seconds, bounds[0], bounds[1])
		}
	}
	rows, err := r.tx.QueryContext(ctx, strings.Join(arms, "\n UNION ALL "), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int64
		var m rollupMeasures
		if err := rows.Scan(append([]any{&bucket}, m.fields()...)...); err != nil {
			return err
		}
		if plan.rollupHour(bucket) {
			add(m)
		}
	}
	return rows.Err()
}

// keyErrorCodes is the step "errors by code": the key's error hours of the
// period through idx_usage_rollup_error_key; images were counted by
// keyPeriodUsage.
func (r *rollupRead) keyErrorCodes(ctx context.Context, plan rollupPlan, keyID string, sums *keyPeriodSums) error {
	from, to, ok := keyPlanHours(plan, sums.start(), sums.end())
	if !ok {
		return nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,error_code,sum(errors) FROM usage_rollup_error_hour
 INDEXED BY idx_usage_rollup_error_key WHERE api_key_dim=? AND bucket_epoch>=? AND bucket_epoch<?
 GROUP BY bucket_epoch,error_code`, rollupDimValue(keyID), from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, errors int64
		var code string
		if err := rows.Scan(&hour, &code, &errors); err != nil {
			return err
		}
		if plan.rollupHour(hour) {
			sums.addCode(code, hour, errors)
		}
	}
	return rows.Err()
}

// keyLimitPace is the step "limit pace": the key's consumption over the pace
// window from hour slices and, per filtered model, cube rows, plus images.
func (r *rollupRead) keyLimitPace(ctx context.Context, q domain.KeyPageUsageQuery) (domain.KeyLimitPace, error) {
	pace := newKeyPace(q.PaceModels)
	plan := planRollupWindow(millis(q.PaceStart), millis(q.Now), nil)
	if err := r.keyImages(ctx, plan, q.KeyID, pace.addImage); err != nil {
		return domain.KeyLimitPace{}, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return pace.result(), nil
	}
	if err := r.keySlices(ctx, plan, q.KeyID, func(m rollupMeasures) {
		pace.all = addPace(pace.all, m.input, m.output, m.cost)
	}); err != nil {
		return domain.KeyLimitPace{}, err
	}
	if len(q.PaceModels) == 0 {
		return pace.result(), nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT c.bucket_epoch,c.model,sum(c.input_tokens),sum(c.output_tokens),sum(c.cost_microdollars)
 FROM usage_rollup_cube_hour c INDEXED BY idx_usage_rollup_cube_key
 WHERE c.api_key_dim=? AND c.bucket_epoch>=? AND c.bucket_epoch<? AND c.model IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(q.PaceModels)), ",")+`) GROUP BY c.bucket_epoch,c.model`,
		append([]any{rollupDimValue(q.KeyID), from, to}, anySlice(q.PaceModels)...)...)
	if err != nil {
		return domain.KeyLimitPace{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, input, output, cost int64
		var model string
		if err := rows.Scan(&hour, &model, &input, &output, &cost); err != nil {
			return domain.KeyLimitPace{}, err
		}
		if sum, ok := pace.byModel[model]; ok && plan.rollupHour(hour) {
			pace.byModel[model] = addPace(sum, input, output, cost)
		}
	}
	return pace.result(), rows.Err()
}

// keyImages is images for one key: raw spans read the key's events through
// idx_usage_events_key_time instead of every key's events of the span.
func (r *rollupRead) keyImages(ctx context.Context, plan rollupPlan, keyID string, add func(img *rollupImage, sign int64)) error {
	where, args := reportEventWhere(reportFilterDims{keys: []string{keyID}})
	if spans := plan.raws(); len(spans) > 0 {
		values := strings.TrimSuffix(strings.Repeat("(?,?),", len(spans)), ",")
		spanArgs := make([]any, 0, 2*len(spans)+1+len(args))
		for _, span := range spans {
			spanArgs = append(spanArgs, span.from, span.to)
		}
		spanArgs = append(append(spanArgs, keyID), args...)
		if err := r.scanImages(ctx, `WITH spans(lo,hi) AS (VALUES `+values+`) SELECT 1,`+rollupImageSQL("e")+`
 FROM spans s CROSS JOIN usage_events e INDEXED BY idx_usage_events_key_time
 WHERE e.api_key_id=? AND e.requested_at>=s.lo AND e.requested_at<s.hi AND `+where, spanArgs, nil, add); err != nil {
			return err
		}
	}
	return r.unfoldedImages(ctx, plan, where, args, add)
}

// keyPageUsageRaw is the raw evaluation of KeyPageUsage: the key's folded
// legacy hours and its events through idx_usage_events_key_time. It is the
// fallback while the rollups are unusable and the oracle of the rollup
// reader.
func keyPageUsageRaw(ctx context.Context, tx *sql.Tx, q domain.KeyPageUsageQuery) (domain.KeyPageUsage, error) {
	sums := newKeyPeriodSums(q)
	if err := sums.addLegacy(ctx, tx, q.KeyID); err != nil {
		return domain.KeyPageUsage{}, err
	}
	if err := keyEventsRaw(ctx, tx, q.KeyID, sums.previousFrom, sums.end(), sums.addImage); err != nil {
		return domain.KeyPageUsage{}, err
	}
	usage := sums.result(false)
	pace := newKeyPace(q.PaceModels)
	if err := keyEventsRaw(ctx, tx, q.KeyID, millis(q.PaceStart), millis(q.Now), pace.addImage); err != nil {
		return usage, err
	}
	usage.Pace = pace.result()
	var err error
	usage.Recent, err = keyRecentRequestsTx(ctx, tx, q.Recent)
	return usage, err
}

// keyEventsRaw feeds add with the key's report events of [from, to).
func keyEventsRaw(ctx context.Context, q analyticsQuerier, keyID string, from, to int64, add func(*rollupImage, int64)) error {
	where, args := reportEventWhere(reportFilterDims{keys: []string{keyID}})
	rows, err := q.QueryContext(ctx, `SELECT `+rollupImageSQL("e")+` FROM usage_events e INDEXED BY idx_usage_events_key_time
 WHERE e.api_key_id=? AND e.requested_at>=? AND e.requested_at<? AND `+where, append([]any{keyID, from, to}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var img rollupImage
		if err := rows.Scan(img.fields()...); err != nil {
			return err
		}
		add(&img, 1)
	}
	return rows.Err()
}

// KeyRecentRequests returns one page of a key's recent requests newest first
// (use case 4, "load more requests"), in its own read snapshot.
func (s *Store) KeyRecentRequests(ctx context.Context, q domain.KeyRequestsQuery) (domain.KeyRequestsPage, error) {
	if err := checkedKeyRequestsQuery(q); err != nil {
		return domain.KeyRequestsPage{Items: []domain.RequestLogEntry{}}, err
	}
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.KeyRequestsPage{}, err
	}
	defer tx.Rollback()
	page, err := keyRecentRequestsTx(ctx, tx, q)
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

// keyRecentRequestsTx reads a page of the key's request log (R23): the key's
// newest events through idx_usage_events_key_time, bounded to the page,
// merged with its pending entries like the request log; errors only reads
// keyFailedRequestsTx.
func keyRecentRequestsTx(ctx context.Context, tx *sql.Tx, q domain.KeyRequestsQuery) (domain.KeyRequestsPage, error) {
	if q.ErrorsOnly {
		return keyFailedRequestsTx(ctx, tx, q)
	}
	page := domain.KeyRequestsPage{Items: []domain.RequestLogEntry{}}
	filter := domain.RequestLogFilter{Limit: q.Limit, APIKeyIDs: []string{q.KeyID}, Before: q.Before}
	newest, args := keyNewestEvents(q, q.Limit+1)
	pending, pageArgs, err := newPendingLogReader(tx).pageCTE(ctx, filter, q.Limit+1)
	if err != nil {
		return page, err
	}
	items, next, _, err := readRequestLogPage(ctx, tx, filter, ` FROM (`+newest+`) e`, args, pending, pageArgs)
	if err != nil {
		return page, err
	}
	page.Items, page.NextCursor = items, next
	return page, nil
}

// keyNewestEvents selects the identifiers of the key's newest n events after
// the cursor of q through the key index.
func keyNewestEvents(q domain.KeyRequestsQuery, n int) (string, []any) {
	query := `SELECT x.request_id,x.requested_at,x.status FROM usage_events x INDEXED BY idx_usage_events_key_time WHERE x.api_key_id=?`
	args := []any{q.KeyID}
	if q.Before != nil {
		at := millis(q.Before.RequestedAt)
		// The first condition is the index range; the second breaks ties.
		query += ` AND x.requested_at<=? AND (x.requested_at<? OR x.request_id<?)`
		args = append(args, at, at, q.Before.RequestID)
	}
	return query + ` ORDER BY x.requested_at DESC,x.request_id DESC LIMIT ?`, append(args, n)
}

// keyFailedRequestsTx examines at most domain.KeyErrorsScanLimit of the
// key's newest rows after the cursor and returns the failed ones; when the
// bound is reached before a full page, the cursor continues after the last
// examined row. Pending entries are not failures and are left out.
func keyFailedRequestsTx(ctx context.Context, tx *sql.Tx, q domain.KeyRequestsQuery) (domain.KeyRequestsPage, error) {
	page := domain.KeyRequestsPage{Items: []domain.RequestLogEntry{}}
	newest, args := keyNewestEvents(q, domain.KeyErrorsScanLimit)
	rows, err := tx.QueryContext(ctx, newest, args...)
	if err != nil {
		return page, err
	}
	var ids []any
	var positions []domain.RequestLogCursor
	var last domain.RequestLogCursor
	scanned := 0
	for rows.Next() {
		var id, status string
		var at int64
		if err := rows.Scan(&id, &at, &status); err != nil {
			rows.Close()
			return page, err
		}
		scanned++
		last = domain.RequestLogCursor{RequestedAt: fromMillis(at), RequestID: id}
		if domain.RequestFailed(status) {
			ids = append(ids, id)
			positions = append(positions, last)
			if len(ids) > q.Limit {
				break
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	switch {
	case len(ids) > q.Limit:
		ids = ids[:q.Limit]
		cursor := positions[q.Limit-1].String()
		page.NextCursor = &cursor
	case scanned == domain.KeyErrorsScanLimit:
		cursor := last.String()
		page.NextCursor = &cursor
	}
	if len(ids) == 0 {
		return page, nil
	}
	events := ` FROM usage_events e WHERE e.request_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	page.Items, _, _, err = readRequestLogPage(ctx, tx, domain.RequestLogFilter{Limit: len(ids)}, events, ids, "", nil)
	return page, err
}
