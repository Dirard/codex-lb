package sqlite

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type reportAggregate struct {
	requests, input, output, reasoning, known, cached, errors, cancelled, accounts int64
	costUSD                                                                        float64
}

// reportCost is the canonical cost of a report scope (design §1.5): folded
// legacy REAL costs summed in query order, then the exact integer microdollars
// of raw events added as one last term. Rollups store microdollars too, so a
// scope read from rollups returns the same float as one read from raw events.
type reportCost struct {
	legacy kbnSum
	micro  int64
}

func (c reportCost) value() float64 {
	sum := c.legacy
	sum.add(float64(c.micro) / 1000000.0)
	return sum.value()
}

// reportCostUSD combines an SQL sum of legacy costs with raw microdollars.
func reportCostUSD(legacy float64, micro int64) float64 {
	return reportCost{legacy: kbnSum{sum: legacy}, micro: micro}.value()
}

// QueryReports evaluates the admin report in a bounded number of statements
// from rollups (reports_rollup.go) or, as fallback and oracle, from raw
// events: folded hourly measures, folded hourly conversations and one ordered
// pass over raw events feed every per-day row, median, breakdown and the
// summary. Per-day rows previously cost five statements per day of the window.
func (s *Store) QueryReports(ctx context.Context, filter domain.ReportFilter) (domain.ReportsResponse, error) {
	if err := checkedReportWindow(filter.Start, filter.End); err != nil {
		return emptyReportsResponse(), err
	}
	if filter.Location == nil {
		filter.Location = time.UTC
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.ReportsResponse, error) { return r.queryReports(ctx, filter) },
		func(tx *sql.Tx) (domain.ReportsResponse, error) { return queryReportsRaw(ctx, tx, filter) })
}

func emptyReportsResponse() domain.ReportsResponse {
	return domain.ReportsResponse{Daily: []domain.DailyReportRow{}, ByModel: []domain.ModelCostEntry{},
		ByUserAgent: []domain.UserAgentCostEntry{}, ByAccount: []domain.AccountCostEntry{}}
}

func queryReportsRaw(ctx context.Context, tx *sql.Tx, filter domain.ReportFilter) (domain.ReportsResponse, error) {
	pass := newReportPass(filter)
	if err := pass.readHourly(ctx, tx); err != nil {
		return emptyReportsResponse(), err
	}
	if err := pass.readHourlyConversations(ctx, tx); err != nil {
		return emptyReportsResponse(), err
	}
	if err := pass.readRawEvents(ctx, tx); err != nil {
		return emptyReportsResponse(), err
	}
	previous, err := reportWindowTotals(ctx, tx, filter, filter.PreviousStart, filter.Start)
	if err != nil {
		return emptyReportsResponse(), err
	}
	var canCompare bool
	facts, args := reportFacts(filter, time.Unix(0, 0), filter.PreviousStart)
	if err := tx.QueryRowContext(ctx, facts+"SELECT EXISTS(SELECT 1 FROM facts)", args...).Scan(&canCompare); err != nil {
		return emptyReportsResponse(), err
	}
	return pass.response(ctx, tx, previous, canCompare)
}

// response assembles the report of a finished pass with the comparison
// window, the account aliases and the folded user-agent total.
func (p *reportPass) response(ctx context.Context, tx *sql.Tx, previous reportAggregate, canCompare bool) (domain.ReportsResponse, error) {
	response := emptyReportsResponse()
	response.Comparison.CanCompare = canCompare
	aliases, err := reportAccountAliases(ctx, tx)
	if err != nil {
		return response, err
	}
	var foldedCost float64
	var foldedRequests int64
	hourlyWhere, hourlyArgs := hourlyReportWhere(p.filter, p.filter.Start, p.filter.End)
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(h.cost_usd),0),coalesce(sum(h.request_count),0)
 FROM legacy_hourly_usage h WHERE `+hourlyWhere, hourlyArgs...).Scan(&foldedCost, &foldedRequests); err != nil {
		return response, err
	}
	current := p.window.result()
	response.Summary = domain.ReportSummary{TotalCostUSD: roundReport(current.costUSD, 4),
		TotalInputTokens: current.input, TotalOutputTokens: current.output,
		TotalReasoningTokens: current.reasoning, ReasoningUsageKnownRequests: current.known,
		TotalCachedTokens: current.cached, TotalRequests: current.requests,
		TotalCancelled: current.cancelled, TotalErrors: current.errors,
		TotalConversations: int64(len(p.conversations)), ActiveAccounts: current.accounts}
	response.Comparison.Previous.TotalCostUSD = roundReport(previous.costUSD, 4)
	response.Comparison.Previous.TotalTokens = previous.input + previous.output
	response.Comparison.Previous.TotalRequests = previous.requests
	response.Daily = p.dailyRows()
	response.ByModel = p.byModel()
	response.ByAccount = p.byAccount(aliases)
	response.ByUserAgent = p.byUserAgent(foldedCost, foldedRequests)
	return response, nil
}

// kbnSum reproduces SQLite's sum() of REAL values (Kahan-Babuska-Neumaier).
type kbnSum struct{ sum, err float64 }

func (k *kbnSum) add(value float64) {
	t := k.sum + value
	if math.Abs(k.sum) > math.Abs(value) {
		k.err += (k.sum - t) + value
	} else {
		k.err += (value - t) + k.sum
	}
	k.sum = t
}

func (k kbnSum) value() float64 { return k.sum + k.err }

// reportMeasures accumulates the facts measures of one report scope.
type reportMeasures struct {
	requests, input, output, reasoning, known, cached, errors, cancelled int64
	cost                                                                 reportCost
	accounts                                                             map[string]struct{}
}

func (m *reportMeasures) add(f reportFact) {
	m.requests += f.requests
	m.input += f.input
	m.output += f.output
	m.reasoning += f.reasoning
	m.known += f.known
	m.cached += f.cached
	m.errors += f.errors
	m.cancelled += f.cancelled
	if f.hasCost {
		m.cost.legacy.add(f.cost)
	}
	m.cost.micro += f.costMicro
	if f.account != "\x1f" {
		if m.accounts == nil {
			m.accounts = map[string]struct{}{}
		}
		m.accounts[f.account] = struct{}{}
	}
}

func (m *reportMeasures) result() reportAggregate {
	return reportAggregate{requests: m.requests, input: m.input, output: m.output, reasoning: m.reasoning,
		known: m.known, cached: m.cached, errors: m.errors, cancelled: m.cancelled,
		accounts: int64(len(m.accounts)), costUSD: m.cost.value()}
}

// reportFact is one row of the facts relation (folded hour or raw event):
// a folded hour carries a REAL cost, a raw event integer microdollars.
type reportFact struct {
	at                                                                   int64
	account, model                                                       string
	requests, input, output, reasoning, known, cached, errors, cancelled int64
	cost                                                                 float64
	hasCost                                                              bool
	costMicro                                                            int64
}

type reportDay struct {
	measures      reportMeasures
	conversations map[string]struct{}
	// latency holds the day's median histograms (design §1.3); a few hundred
	// buckets at most, independent of the number of events.
	latency rollupLatency
}

type reportPass struct {
	filter        domain.ReportFilter
	bounds        []int64 // local-midnight day starts plus the window end, UTC ms
	labels        []string
	days          []reportDay
	window        reportMeasures
	models        map[string]*reportMeasures
	accounts      map[string]*reportMeasures
	userAgents    map[string]*reportUserAgent
	conversations map[string]struct{}
}

type reportUserAgent struct{ cost, requests int64 }

func newReportPass(filter domain.ReportFilter) *reportPass {
	pass := &reportPass{filter: filter, models: map[string]*reportMeasures{}, accounts: map[string]*reportMeasures{},
		userAgents: map[string]*reportUserAgent{}, conversations: map[string]struct{}{}}
	startDay, endDay := filter.Start.In(filter.Location), filter.End.In(filter.Location)
	for day := startDay; day.Before(endDay); day = day.AddDate(0, 0, 1) {
		pass.bounds = append(pass.bounds, millis(day.UTC()))
		pass.labels = append(pass.labels, day.Format("2006-01-02"))
	}
	pass.bounds = append(pass.bounds, millis(endDay.UTC()))
	pass.days = make([]reportDay, len(pass.labels))
	return pass
}

// day maps an instant to its local report day by binary search over the
// day starts; DST days are 23 or 25 hours long.
func (p *reportPass) day(at int64) (int, bool) {
	n := len(p.labels)
	if n == 0 || at < p.bounds[0] || at >= p.bounds[n] {
		return 0, false
	}
	return sort.Search(n, func(i int) bool { return p.bounds[i+1] > at }), true
}

func (p *reportPass) addFact(f reportFact) {
	p.window.add(f)
	if day, ok := p.day(f.at); ok {
		p.days[day].measures.add(f)
	}
	if p.models[f.model] == nil {
		p.models[f.model] = &reportMeasures{}
	}
	p.models[f.model].add(f)
	if p.accounts[f.account] == nil {
		p.accounts[f.account] = &reportMeasures{}
	}
	p.accounts[f.account].add(f)
}

func (p *reportPass) addConversation(at int64, conversation string) {
	p.conversations[conversation] = struct{}{}
	if day, ok := p.day(at); ok {
		if p.days[day].conversations == nil {
			p.days[day].conversations = map[string]struct{}{}
		}
		p.days[day].conversations[conversation] = struct{}{}
	}
}

func (p *reportPass) readHourly(ctx context.Context, tx *sql.Tx) error {
	where, args := hourlyReportWhere(p.filter, p.filter.Start, p.filter.End)
	rows, err := tx.QueryContext(ctx, `SELECT h.bucket_epoch,h.account_id,h.model,h.request_count,h.input_tokens,
 h.output_or_reasoning_tokens,h.reasoning_tokens,h.reasoning_known_count,h.cached_input_tokens_clamped,
 h.cost_usd,h.error_count,h.cancelled_count FROM legacy_hourly_usage h WHERE `+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f reportFact
		var bucket int64
		var cost sql.NullFloat64
		if err := rows.Scan(&bucket, &f.account, &f.model, &f.requests, &f.input, &f.output, &f.reasoning,
			&f.known, &f.cached, &cost, &f.errors, &f.cancelled); err != nil {
			return err
		}
		f.at, f.cost, f.hasCost = bucket*1000, cost.Float64, cost.Valid
		p.addFact(f)
	}
	return rows.Err()
}

func (p *reportPass) readHourlyConversations(ctx context.Context, tx *sql.Tx) error {
	filter := p.filter
	if filter.Model != "" || filter.UserAgentGroup != "" || len(filter.APIKeyIDs) != 0 {
		return nil
	}
	query := `SELECT c.bucket_epoch,c.conversation_id FROM legacy_conversation_hourly c WHERE c.bucket_epoch>=? AND c.bucket_epoch<?`
	args := []any{filter.Start.Unix(), filter.End.Unix()}
	if len(filter.AccountIDs) > 0 {
		ids := make([]string, len(filter.AccountIDs))
		for i, id := range filter.AccountIDs {
			ids[i] = toLegacyDimension(id)
		}
		var parts []string
		parts, args = appendInCondition(parts, args, "c.account_id", ids)
		query += " AND " + parts[0]
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int64
		var conversation sql.NullString
		if err := rows.Scan(&bucket, &conversation); err != nil {
			return err
		}
		if conversation.Valid {
			p.addConversation(bucket*1000, conversation.String)
		}
	}
	return rows.Err()
}

// readRawEvents is the single ordered pass over unfolded events. It reads the
// same rows as every former per-day statement; medians come from the same
// histograms the rollups store.
func (p *reportPass) readRawEvents(ctx context.Context, tx *sql.Tx) error {
	where, args := rawReportWhere(p.filter, p.filter.Start, p.filter.End)
	rows, err := tx.QueryContext(ctx, `SELECT e.requested_at,e.account_id,e.model,e.input_tokens,e.output_tokens,
 e.reasoning_tokens,e.reasoning_tokens_known,max(0,min(e.cached_input_tokens,e.input_tokens)),
 e.cost_microdollars,e.status,e.conversation_id,
 coalesce(nullif(e.useragent_group,''),'Missing User-Agent'),
 e.latency_first_token_ms,e.queue_latency_ms,e.total_latency_ms
 FROM usage_events e WHERE `+where+` ORDER BY e.requested_at`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f reportFact
		var account, conversation sql.NullString
		var cached, ttft sql.NullInt64
		var status, userAgent string
		var queue, total int64
		if err := rows.Scan(&f.at, &account, &f.model, &f.input, &f.output, &f.reasoning, &f.known, &cached,
			&f.costMicro, &status, &conversation, &userAgent, &ttft, &queue, &total); err != nil {
			return err
		}
		f.account = rawReportDimension(account)
		f.requests, f.cached = 1, cached.Int64
		switch status {
		case "success", "completed":
		case "cancelled":
			f.cancelled = 1
		default:
			f.errors = 1
		}
		p.addFact(f)
		agent := p.userAgents[userAgent]
		if agent == nil {
			agent = &reportUserAgent{}
			p.userAgents[userAgent] = agent
		}
		agent.requests++
		agent.cost += f.costMicro
		if conversation.Valid {
			if trimmed := strings.Trim(conversation.String, " "); trimmed != "" {
				p.addConversation(f.at, trimmed)
			}
		}
		if day, ok := p.day(f.at); ok {
			p.days[day].latency.observe(ttft, queue, f.output, total, 1)
		}
	}
	return rows.Err()
}

// rawReportDimension mirrors the facts CTE mapping of a raw account/key id.
func rawReportDimension(value sql.NullString) string {
	switch {
	case !value.Valid:
		return "\x1f"
	case strings.HasPrefix(value.String, "\x1f"):
		return "\x1f" + value.String
	default:
		return value.String
	}
}

func (p *reportPass) dailyRows() []domain.DailyReportRow {
	rows := make([]domain.DailyReportRow, 0, len(p.days))
	for i := range p.days {
		d := &p.days[i]
		values := d.measures.result()
		row := domain.DailyReportRow{Date: p.labels[i], Requests: values.requests, InputTokens: values.input,
			OutputTokens: values.output, CachedInputTokens: values.cached, ActiveAccounts: values.accounts,
			CostUSD: roundReport(values.costUSD, 4), ErrorCount: values.errors, CancelledCount: values.cancelled,
			Conversations: int64(len(d.conversations))}
		row.MedianTTFTMS, row.MedianQueueMS, row.MedianTPS = d.latency.medians()
		if values.known > 0 || values.reasoning > 0 {
			reasoning := values.reasoning
			row.ReasoningTokens = &reasoning
		}
		rows = append(rows, row)
	}
	return rows
}

func (p *reportPass) byModel() []domain.ModelCostEntry {
	names := make([]string, 0, len(p.models))
	for name := range p.models {
		names = append(names, name)
	}
	sort.Strings(names)
	var total kbnSum
	entries := make([]domain.ModelCostEntry, 0, len(names))
	for _, name := range names {
		measures := p.models[name]
		cost := measures.cost.value()
		total.add(cost)
		entries = append(entries, domain.ModelCostEntry{Model: name, CostUSD: cost, Requests: measures.requests})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].CostUSD > entries[j].CostUSD })
	entries = entries[:min(len(entries), 2000)]
	for i := range entries {
		if total.value() > 0 {
			entries[i].Percentage = roundReport(entries[i].CostUSD/total.value()*100, 1)
		}
		entries[i].CostUSD = roundReport(entries[i].CostUSD, 4)
	}
	return entries
}

func (p *reportPass) byAccount(aliases map[string]string) []domain.AccountCostEntry {
	ids := make([]string, 0, len(p.accounts))
	for id := range p.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type ranked struct {
		id   string
		cost float64
	}
	order := make([]ranked, 0, len(ids))
	for _, id := range ids {
		order = append(order, ranked{id, p.accounts[id].cost.value()})
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].cost > order[j].cost })
	order = order[:min(len(order), 2000)]
	entries := make([]domain.AccountCostEntry, 0, len(order))
	for _, item := range order {
		entry := domain.AccountCostEntry{CostUSD: roundReport(item.cost, 4), Requests: p.accounts[item.id].requests}
		if item.id != "\x1f" {
			id := fromLegacyDimension(item.id)
			entry.AccountID = &id
		}
		if alias, ok := aliases[item.id]; ok {
			entry.Alias = &alias
		}
		entries = append(entries, entry)
	}
	return entries
}

func (p *reportPass) byUserAgent(foldedCost float64, foldedRequests int64) []domain.UserAgentCostEntry {
	result := make([]domain.UserAgentCostEntry, 0, len(p.userAgents)+1)
	for name, agent := range p.userAgents {
		result = append(result, domain.UserAgentCostEntry{UserAgent: name, CostUSD: float64(agent.cost) / 1000000.0, Requests: agent.requests})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CostUSD != result[j].CostUSD {
			return result[i].CostUSD > result[j].CostUSD
		}
		return result[i].UserAgent < result[j].UserAgent
	})
	result = result[:min(len(result), 2000)]
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
	return result
}

func reportAccountAliases(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,alias FROM accounts WHERE alias<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := map[string]string{}
	for rows.Next() {
		var id, alias string
		if err := rows.Scan(&id, &alias); err != nil {
			return nil, err
		}
		aliases[id] = alias
	}
	return aliases, rows.Err()
}

// reportWindowTotals sums the comparison window without the distinct-account
// pass that only the current window needs.
func reportWindowTotals(ctx context.Context, tx *sql.Tx, filter domain.ReportFilter, start, end time.Time) (reportAggregate, error) {
	var result reportAggregate
	facts, args := reportFacts(filter, start, end)
	var legacyCost float64
	var costMicro int64
	err := tx.QueryRowContext(ctx, facts+`SELECT coalesce(sum(request_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(cost_usd),0),coalesce(sum(cost_micro),0) FROM facts`, args...).
		Scan(&result.requests, &result.input, &result.output, &legacyCost, &costMicro)
	result.costUSD = reportCostUSD(legacyCost, costMicro)
	return result, err
}

func aggregateReportWindow(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, start, end time.Time) (reportAggregate, error) {
	var result reportAggregate
	facts, args := reportFacts(filter, start, end)
	query := facts + `SELECT coalesce(sum(request_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(reasoning_tokens),0),
 coalesce(sum(reasoning_known_count),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(cost_usd),0),coalesce(sum(cost_micro),0),coalesce(sum(error_count),0),
 coalesce(sum(cancelled_count),0),
 count(DISTINCT CASE WHEN account_id!=char(31) THEN account_id END) FROM facts`
	var legacyCost float64
	var costMicro int64
	err := q.QueryRowContext(ctx, query, args...).Scan(&result.requests, &result.input, &result.output,
		&result.reasoning, &result.known, &result.cached, &legacyCost, &costMicro, &result.errors,
		&result.cancelled, &result.accounts)
	result.costUSD = reportCostUSD(legacyCost, costMicro)
	return result, err
}

func reportHasActivityBefore(ctx context.Context, q analyticsQuerier, filter domain.ReportFilter, before time.Time) (bool, error) {
	facts, args := reportFacts(filter, time.Unix(0, 0), before)
	var exists bool
	err := q.QueryRowContext(ctx, facts+"SELECT EXISTS(SELECT 1 FROM facts)", args...).Scan(&exists)
	return exists, err
}

func roundReport(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
