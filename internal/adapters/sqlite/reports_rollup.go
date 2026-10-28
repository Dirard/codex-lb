package sqlite

import (
	"context"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// rollupMeasureColumnsOf qualifies the measures M with alias.
func rollupMeasureColumnsOf(alias string) string {
	columns := strings.Split(rollupMeasureColumns, ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ",")
}

// rollupMeasureSums sums the measures M of alias in field order.
func rollupMeasureSums(alias string) string {
	columns := strings.Split(rollupMeasureColumns, ",")
	for i, column := range columns {
		columns[i] = "sum(" + alias + "." + strings.TrimSpace(column) + ")"
	}
	return strings.Join(columns, ",")
}

// addRollup adds rollup or image measures; their cost is raw microdollars.
func (m *reportMeasures) addRollup(r rollupMeasures) {
	m.requests += r.requests
	m.input += r.input
	m.output += r.output
	m.reasoning += r.reasoning
	m.known += r.known
	m.cached += r.cached
	m.errors += r.errors
	m.cancelled += r.cancelled
	m.cost.micro += r.cost
}

func (m *reportMeasures) addAccount(account string) {
	if m.accounts == nil {
		m.accounts = map[string]struct{}{}
	}
	m.accounts[account] = struct{}{}
}

func addRollupMeasures(scopes map[string]*rollupMeasures, key string, m rollupMeasures) {
	scope := scopes[key]
	if scope == nil {
		scope = &rollupMeasures{}
		scopes[key] = scope
	}
	*scope = scope.plus(m)
}

// rollupReport accumulates signed rollup rows and event images of a report
// window. Events count with signed requests, so a scope that journal rows
// emptied disappears like it does from raw events; finish merges the scopes
// with events into the pass that holds the folded legacy facts.
type rollupReport struct {
	pass                          *reportPass
	window                        rollupMeasures
	days                          []rollupMeasures
	dayAccounts, dayConversations []map[string]int64
	models, accounts, userAgents  map[string]*rollupMeasures
	conversations                 map[string]int64
	removesConversations          bool
}

func newRollupReport(pass *reportPass) *rollupReport {
	return &rollupReport{pass: pass, days: make([]rollupMeasures, len(pass.days)),
		dayAccounts: make([]map[string]int64, len(pass.days)), dayConversations: make([]map[string]int64, len(pass.days)),
		models: map[string]*rollupMeasures{}, accounts: map[string]*rollupMeasures{}, userAgents: map[string]*rollupMeasures{},
		conversations: map[string]int64{}}
}

func addDayCount(days []map[string]int64, day int, key string, n int64) {
	if days[day] == nil {
		days[day] = map[string]int64{}
	}
	days[day][key] += n
}

func (a *rollupReport) addTotal(at int64, m rollupMeasures, latency *rollupLatency) {
	a.window = a.window.plus(m)
	if day, ok := a.pass.day(at); ok {
		a.days[day] = a.days[day].plus(m)
		if latency != nil {
			a.pass.days[day].latency = a.pass.days[day].latency.merge(*latency, 1)
		}
	}
}

func (a *rollupReport) addAccount(at int64, account string, m rollupMeasures) {
	addRollupMeasures(a.accounts, account, m)
	if day, ok := a.pass.day(at); ok && account != "\x1f" && m.requests != 0 {
		addDayCount(a.dayAccounts, day, account, m.requests)
	}
}

func (a *rollupReport) addConversation(at int64, conversation string, n int64) {
	a.conversations[conversation] += n
	if day, ok := a.pass.day(at); ok {
		addDayCount(a.dayConversations, day, conversation, n)
	}
}

// addImage counts one signed event image in every scope.
func (a *rollupReport) addImage(img *rollupImage, sign int64) {
	var m rollupMeasures
	m.add(img, sign)
	a.addTotal(img.at, m, nil)
	if day, ok := a.pass.day(img.at); ok && sign != 0 {
		a.pass.days[day].latency.observe(img.ttft, img.queue, img.output, img.total, sign)
	}
	a.addAccount(img.at, rawReportDimension(img.account), m)
	addRollupMeasures(a.models, img.model, m)
	addRollupMeasures(a.userAgents, reportUserAgentLabel(img.userAgent), m)
	if sign != 0 && img.conversation != "" {
		a.addConversation(img.at, img.conversation, sign)
		a.removesConversations = a.removesConversations || sign < 0
	}
}

// finish merges the scopes that hold events into the pass.
func (a *rollupReport) finish() {
	p := a.pass
	p.window.addRollup(a.window)
	for i := range a.days {
		p.days[i].measures.addRollup(a.days[i])
		for account, n := range a.dayAccounts[i] {
			if n > 0 {
				p.days[i].measures.addAccount(account)
			}
		}
		for conversation, n := range a.dayConversations[i] {
			if n > 0 {
				if p.days[i].conversations == nil {
					p.days[i].conversations = map[string]struct{}{}
				}
				p.days[i].conversations[conversation] = struct{}{}
			}
		}
	}
	for _, scope := range []struct {
		rollup map[string]*rollupMeasures
		pass   map[string]*reportMeasures
	}{{a.models, p.models}, {a.accounts, p.accounts}} {
		for key, m := range scope.rollup {
			if m.requests <= 0 && scope.pass[key] == nil {
				continue
			}
			if scope.pass[key] == nil {
				scope.pass[key] = &reportMeasures{}
			}
			scope.pass[key].addRollup(*m)
		}
	}
	for account, m := range a.accounts {
		if m.requests > 0 && account != "\x1f" {
			p.window.addAccount(account)
		}
	}
	for label, m := range a.userAgents {
		if m.requests > 0 {
			p.userAgents[label] = &reportUserAgent{cost: m.cost, requests: m.requests}
		}
	}
	for conversation, n := range a.conversations {
		if n > 0 {
			p.conversations[conversation] = struct{}{}
		}
	}
}

// queryReports is the rollup reader of QueryReports (design §4.2): folded
// legacy facts as the raw path reads them; total hours, account slices and
// model and user-agent slices without a filter, the cube with one; the
// conversation table for distinct conversations; images for partial hours,
// split hours of fractional zones, the tail and the journal.
func (r *rollupRead) queryReports(ctx context.Context, filter domain.ReportFilter) (domain.ReportsResponse, error) {
	pass := newReportPass(filter)
	if err := pass.readHourly(ctx, r.tx); err != nil {
		return emptyReportsResponse(), err
	}
	if err := pass.readHourlyConversations(ctx, r.tx); err != nil {
		return emptyReportsResponse(), err
	}
	report := newRollupReport(pass)
	dims := reportDims(filter)
	plan := planRollupWindow(millis(filter.Start), millis(filter.End), pass.bounds)
	where, args := reportEventWhere(dims)
	if err := r.images(ctx, plan, where, args, report.addImage); err != nil {
		return emptyReportsResponse(), err
	}
	var err error
	if dims.empty() {
		err = r.reportTotals(ctx, plan, report)
	} else {
		err = r.reportCube(ctx, plan, dims, report)
	}
	if err == nil {
		err = r.conversationHours(ctx, plan, dims, report.removesConversations, report.addConversation)
	}
	if err != nil {
		return emptyReportsResponse(), err
	}
	report.finish()
	previous, err := r.windowAggregate(ctx, filter, filter.PreviousStart, filter.Start)
	if err != nil {
		return emptyReportsResponse(), err
	}
	canCompare, err := r.reportHasActivityBefore(ctx, filter, filter.PreviousStart)
	if err != nil {
		return emptyReportsResponse(), err
	}
	return pass.response(ctx, r.tx, previous, canCompare)
}

// reportTotals reads an unfiltered window: total hours with histograms,
// hour-grain account slices for daily active accounts, and model and
// user-agent slices, day grain for whole UTC days.
func (r *rollupRead) reportTotals(ctx context.Context, plan rollupPlan, report *rollupReport) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,`+rollupMeasureColumns+`,ttft_hist,queue_hist,tps_hist
 FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, from, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var hour int64
		var m rollupMeasures
		var ttft, queue, tps []byte
		if err := rows.Scan(append(append([]any{&hour}, m.fields()...), &ttft, &queue, &tps)...); err != nil {
			rows.Close()
			return err
		}
		if !plan.rollupHour(hour) {
			continue
		}
		latency, err := decodeRollupLatency(ttft, queue, tps, nil)
		if err != nil {
			rows.Close()
			return err
		}
		report.addTotal(hour*1000, m, &latency)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err := r.sliceRows(ctx, plan, "account", true, func(hour int64, dim string, m rollupMeasures) {
		report.addAccount(hour*1000, dim, m)
	}); err != nil {
		return err
	}
	if err := r.sliceRows(ctx, plan, "model", false, func(_ int64, dim string, m rollupMeasures) {
		addRollupMeasures(report.models, dim, m)
	}); err != nil {
		return err
	}
	return r.sliceRows(ctx, plan, "useragent", false, func(_ int64, dim string, m rollupMeasures) {
		addRollupMeasures(report.userAgents, reportUserAgentLabel(decodeRollupDim(dim)), m)
	})
}

// sliceRows reads the slice rows of kind covering the plan's rollup hours:
// hour grain only when hourly is set (per-day scopes), otherwise day rows for
// whole UTC days and hour rows for the rest, in one statement.
func (r *rollupRead) sliceRows(ctx context.Context, plan rollupPlan, kind string, hourly bool, add func(hour int64, dim string, m rollupMeasures)) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	hours, days, bounded := plan.sliceRanges()
	if hourly || !bounded {
		hours, days = [][2]int64{{from, to}}, nil
	}
	const arm = `SELECT bucket_epoch,dim,` + rollupMeasureColumns + ` FROM usage_rollup_slice
 WHERE kind=? AND grain=? AND bucket_epoch>=? AND bucket_epoch<?`
	var arms []string
	var args []any
	for _, grain := range []struct {
		seconds int64
		ranges  [][2]int64
	}{{3600, hours}, {86400, days}} {
		for _, bounds := range grain.ranges {
			arms = append(arms, arm)
			args = append(args, kind, grain.seconds, bounds[0], bounds[1])
		}
	}
	rows, err := r.tx.QueryContext(ctx, strings.Join(arms, "\n UNION ALL "), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int64
		var dim string
		var m rollupMeasures
		if err := rows.Scan(append([]any{&bucket, &dim}, m.fields()...)...); err != nil {
			return err
		}
		// Day rows lie inside one rollup span; hour rows of a whole-range
		// read may fall on a raw hour.
		if plan.rollupHour(bucket) {
			add(bucket, dim, m)
		}
	}
	return rows.Err()
}

// reportCube reads a filtered window from the cube through the index of the
// most selective filter.
func (r *rollupRead) reportCube(ctx context.Context, plan rollupPlan, dims reportFilterDims, report *rollupReport) error {
	from, to, ok := plan.hours()
	if !ok {
		return nil
	}
	where, args := dims.rollupDimWhere("c")
	rows, err := r.tx.QueryContext(ctx, `SELECT c.bucket_epoch,c.account_dim,c.model,c.ua_dim,`+
		rollupMeasureColumnsOf("c")+`,c.ttft_hist,c.queue_hist,c.tps_hist
 FROM usage_rollup_cube_hour c`+dims.cubeIndex()+` WHERE `+where+` AND c.bucket_epoch>=? AND c.bucket_epoch<?`,
		append(args, from, to)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hour int64
		var account, model, agent string
		var m rollupMeasures
		var ttft, queue, tps []byte
		if err := rows.Scan(append(append([]any{&hour, &account, &model, &agent}, m.fields()...), &ttft, &queue, &tps)...); err != nil {
			return err
		}
		if !plan.rollupHour(hour) {
			continue
		}
		latency, err := decodeRollupLatency(ttft, queue, tps, nil)
		if err != nil {
			return err
		}
		report.addTotal(hour*1000, m, &latency)
		report.addAccount(hour*1000, account, m)
		addRollupMeasures(report.models, model, m)
		addRollupMeasures(report.userAgents, reportUserAgentLabel(decodeRollupDim(agent)), m)
	}
	return rows.Err()
}

// windowMeasures sums the report measures of a window from rollups and
// images: total hours without a filter, the cube with one.
func (r *rollupRead) windowMeasures(ctx context.Context, plan rollupPlan, dims reportFilterDims) (rollupMeasures, error) {
	var sum rollupMeasures
	where, args := reportEventWhere(dims)
	if err := r.images(ctx, plan, where, args, func(img *rollupImage, sign int64) { sum.add(img, sign) }); err != nil {
		return sum, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return sum, nil
	}
	query := `SELECT c.bucket_epoch,` + rollupMeasureColumnsOf("c") + `
 FROM usage_rollup_total_hour c WHERE c.bucket_epoch>=? AND c.bucket_epoch<?`
	queryArgs := []any{from, to}
	if !dims.empty() {
		cubeWhere, cubeArgs := dims.rollupDimWhere("c")
		query = `SELECT c.bucket_epoch,` + rollupMeasureSums("c") + ` FROM usage_rollup_cube_hour c` + dims.cubeIndex() + `
 WHERE ` + cubeWhere + ` AND c.bucket_epoch>=? AND c.bucket_epoch<? GROUP BY c.bucket_epoch`
		queryArgs = append(cubeArgs, from, to)
	}
	rows, err := r.tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour int64
		var m rollupMeasures
		if err := rows.Scan(append([]any{&hour}, m.fields()...)...); err != nil {
			return sum, err
		}
		if plan.rollupHour(hour) {
			sum = sum.plus(m)
		}
	}
	return sum, rows.Err()
}

// legacyWindow sums the folded legacy hours of a report scope with the outer
// query of aggregateReportWindow; cost is SQLite's sum of their REAL costs.
func legacyWindow(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, start, end time.Time) (rollupMeasures, float64, error) {
	var m rollupMeasures
	var cost float64
	var micro int64
	facts, args := legacyReportFacts(filter, start, end)
	err := q.QueryRowContext(ctx, facts+`SELECT coalesce(sum(request_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(reasoning_tokens),0),
 coalesce(sum(reasoning_known_count),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(cost_usd),0),coalesce(sum(cost_micro),0),coalesce(sum(error_count),0),
 coalesce(sum(cancelled_count),0) FROM facts`, args...).Scan(&m.requests, &m.input, &m.output,
		&m.reasoning, &m.known, &m.cached, &cost, &micro, &m.errors, &m.cancelled)
	m.cost = micro
	return m, cost, err
}

// windowAggregate is aggregateReportWindow without the distinct accounts:
// folded legacy hours plus rollups and images, the cost combined canonically.
func (r *rollupRead) windowAggregate(ctx context.Context, filter domain.ReportFilter, start, end time.Time) (reportAggregate, error) {
	legacy, legacyCost, err := legacyWindow(ctx, r.tx, filter, start, end)
	if err != nil {
		return reportAggregate{}, err
	}
	events, err := r.windowMeasures(ctx, planRollupWindow(millis(start), millis(end), nil), reportDims(filter))
	if err != nil {
		return reportAggregate{}, err
	}
	m := legacy.plus(events)
	return reportAggregate{requests: m.requests, input: m.input, output: m.output, reasoning: m.reasoning,
		known: m.known, cached: m.cached, errors: m.errors, cancelled: m.cancelled,
		costUSD: reportCostUSD(legacyCost, m.cost)}, nil
}

// reportHasActivityBefore is reportHasActivityBefore over folded legacy hours,
// rollups and images.
func (r *rollupRead) reportHasActivityBefore(ctx context.Context, filter domain.ReportFilter, before time.Time) (bool, error) {
	facts, args := legacyReportFacts(filter, time.Unix(0, 0), before)
	var exists bool
	if err := r.tx.QueryRowContext(ctx, facts+"SELECT EXISTS(SELECT 1 FROM facts)", args...).Scan(&exists); err != nil || exists {
		return exists, err
	}
	return r.eventsExist(ctx, planRollupWindow(0, millis(before), nil), reportDims(filter))
}
