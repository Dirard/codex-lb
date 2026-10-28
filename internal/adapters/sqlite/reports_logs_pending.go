package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"codex-lb/internal/domain"
)

// schemaV43 indexes the reservations the request log shows as pending entries
// (still reserved and flagged for reconciliation) with every column the log
// filters, groups and orders them by, so facets, totals and pages read the
// index alone. Partial: ordinary reservations and their settlement never
// touch it. Additive: one index older code ignores; building it reads the
// reservations table once (about 0.2 s for 1.6 M rows, 74 k of them pending).
const schemaV43 = `
CREATE INDEX idx_usage_reservations_pending ON usage_reservations(account_id,account_generation,api_key_id,model,created_at,id)
 WHERE status='reserved' AND needs_reconciliation=1;
`

const pendingLogStatus = "reconciliation_required"

// pendingLogAccount and pendingLogKey are the account and key of the
// pending entry of a reservation (columns of alias t): a deleted account
// generation, no account and the warmup key read as NULL.
func pendingLogAccount(t string) string {
	return `CASE WHEN EXISTS (SELECT 1 FROM account_deletions d WHERE d.account_id=` + t + `.account_id
 AND d.generation=` + t + `.account_generation) THEN NULL ELSE nullif(` + t + `.account_id,'') END`
}

func pendingLogKey(t string) string {
	return `nullif(` + t + `.api_key_id,'` + domain.WarmupKeyID + `')`
}

// pendingLogProjection maps a reservation r to the request-log columns of
// its pending entry (requestLogColumns): a read projection, not a usage
// event, so held budget is never presented as actual usage.
var pendingLogProjection = `r.id,NULL,r.created_at,` + pendingLogAccount("r") + `,` + pendingLogKey("r") + `,'',
 CASE WHEN r.api_key_id='` + domain.WarmupKeyID + `' THEN 'warmup' ELSE 'unknown' END,r.model,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,
 '` + pendingLogStatus + `','usage_reconciliation_required',NULL,NULL,NULL,0,NULL,NULL,
 NULL,NULL,NULL,NULL`

// pendingLogRows are the reservations that can be pending entries, read from
// their covering partial index. Ordinary live reservations remain invisible.
const pendingLogRows = ` FROM usage_reservations r INDEXED BY idx_usage_reservations_pending
 WHERE r.status='reserved' AND r.needs_reconciliation=1`

// pendingLogInGroup restricts pendingLogRows to the group g of groupsSQL.
const pendingLogInGroup = ` AND r.account_id IS g.raw_account_id AND r.account_generation=g.account_generation
 AND r.api_key_id=g.raw_api_key_id AND r.model=g.model`

// pendingLogRecordedLimit bounds the recorded reservations handled by id;
// with more, every pending row probes usage_events.
const pendingLogRecordedLimit = 1000

// pendingLogPlan is requestLogWhere over the pending entries, which have no
// conversation, effort, usage or user agent: the conditions over reservation
// rows (alias r) and over their (account, generation, key, model) groups,
// whose projected account and key are evaluated once per group.
type pendingLogPlan struct {
	none bool // the filter selects no pending entry
	// rows are the row conditions, each prefixed with " AND ". They include
	// the raw account and key a projected value requires, so the index is
	// searched instead of scanned.
	rows    string
	rowArgs []any
	// groups are the conditions on the projected account and key (alias g).
	groups    []string
	groupArgs []any
	// search is the LIKE pattern of a search: it matches the group (model,
	// error code, account alias, key name) or the row (request id).
	search string
}

func planPendingLog(filter domain.RequestLogFilter) pendingLogPlan {
	var p pendingLogPlan
	if filter.ConversationID != "" || len(filter.ReasoningEfforts) > 0 ||
		len(filter.Statuses) > 0 && !slices.Contains(filter.Statuses, pendingLogStatus) {
		p.none = true
		return p
	}
	var rows []string
	if filter.Since != nil {
		rows = append(rows, "r.created_at>=?")
		p.rowArgs = append(p.rowArgs, millis(*filter.Since))
	}
	if filter.Until != nil {
		rows = append(rows, "r.created_at<?")
		p.rowArgs = append(p.rowArgs, millis(*filter.Until))
	}
	if filter.Before != nil {
		rows = append(rows, "(r.created_at,r.id)<(?,?)")
		p.rowArgs = append(p.rowArgs, millis(filter.Before.RequestedAt), filter.Before.RequestID)
	}
	rows, p.rowArgs = appendInCondition(rows, p.rowArgs, "r.account_id", filter.AccountIDs)
	p.groups, p.groupArgs = appendInCondition(p.groups, p.groupArgs, "g.account_id", filter.AccountIDs)
	rows, p.rowArgs = appendInCondition(rows, p.rowArgs, "r.api_key_id", filter.APIKeyIDs)
	p.groups, p.groupArgs = appendInCondition(p.groups, p.groupArgs, "g.api_key_id", filter.APIKeyIDs)
	rows, p.rowArgs = appendInCondition(rows, p.rowArgs, "r.model", filter.Models)
	// A model option with an effort never matches the NULL effort of a
	// pending entry; options are ignored only when none is valid.
	var models []string
	valid := false
	for _, option := range filter.ModelOptions {
		model, effort, hasEffort := strings.Cut(option, ":::")
		if model == "" {
			continue
		}
		valid = true
		if !hasEffort || effort == "" {
			models = append(models, model)
		}
	}
	if valid && len(models) == 0 {
		p.none = true
		return p
	}
	rows, p.rowArgs = appendInCondition(rows, p.rowArgs, "r.model", models)
	for _, row := range rows {
		p.rows += " AND " + row
	}
	if filter.Search != "" {
		p.search = "%" + escapeLike(filter.Search) + "%"
	}
	return p
}

// grouped reports whether a group condition or a search applies, which
// pendingLogGroupsSQL evaluates per group.
func (p pendingLogPlan) grouped() bool { return len(p.groups) > 0 || p.search != "" }

// matchedSQL is the part of the search a whole group matches (requestLogWhere
// over the projection); NULL counts as no match, as in the row filter.
func (p pendingLogPlan) matchedSQL() (string, []any) {
	if p.search == "" {
		return "1", nil
	}
	return `(g.model LIKE ? ESCAPE '\' OR 'usage_reconciliation_required' LIKE ? ESCAPE '\'
 OR a.alias LIKE ? ESCAPE '\' OR k.name LIKE ? ESCAPE '\')`, []any{p.search, p.search, p.search, p.search}
}

// groupsSQL selects the groups of the rows of source (pendingLogRows or a
// restriction of it) the row conditions select, with their raw and projected
// account and key, as g joined with the accounts and keys a search reads and
// restricted by the group conditions. columns are selected from g, a and k;
// counts adds the group's rows n and the rows whose request id matches the
// search n_id.
func (p pendingLogPlan) groupsSQL(columns string, columnArgs []any, source string, sourceArgs []any, counts bool) (string, []any) {
	args := append([]any{}, columnArgs...)
	aggregates := ""
	if counts {
		aggregates = ",count(*) AS n,0 AS n_id"
		if p.search != "" {
			aggregates = `,count(*) AS n,sum(r.id LIKE ? ESCAPE '\') AS n_id`
			args = append(args, p.search)
		}
	}
	query := `SELECT ` + columns + ` FROM (SELECT p.account_id AS raw_account_id,p.account_generation,
 p.api_key_id AS raw_api_key_id,p.model,` + pendingLogAccount("p") + ` AS account_id,` + pendingLogKey("p") + ` AS api_key_id`
	if counts {
		query += `,p.n,p.n_id`
	}
	query += ` FROM (SELECT r.account_id,r.account_generation,r.api_key_id,r.model` + aggregates + source + p.rows +
		` GROUP BY r.account_id,r.account_generation,r.api_key_id,r.model) p) g`
	args = append(append(args, sourceArgs...), p.rowArgs...)
	if p.search != "" {
		query += ` LEFT JOIN accounts a ON a.id=g.account_id LEFT JOIN api_keys k ON k.id=g.api_key_id`
	}
	if len(p.groups) > 0 {
		query += " WHERE " + strings.Join(p.groups, " AND ")
	}
	return query, append(args, p.groupArgs...)
}

// pendingLogGroup is the projected account, key and model of pending entries.
type pendingLogGroup struct {
	account, key sql.NullString
	model        string
}

// pendingLogGroups returns the groups of pending entries the filter selects,
// for the facets: a group is present once one of its matching rows has no
// usage event, so at most one probe of usage_events per group usually runs.
func pendingLogGroups(ctx context.Context, q analyticsQuerier, filter domain.RequestLogFilter) ([]pendingLogGroup, error) {
	plan := planPendingLog(filter)
	if plan.none {
		return nil, nil
	}
	matched, matchedArgs := plan.matchedSQL()
	query, args := plan.groupsSQL("g.account_id,g.api_key_id,g.model", nil, pendingLogRows, nil, false)
	if len(plan.groups) > 0 {
		query += " AND "
	} else {
		query += " WHERE "
	}
	query += `EXISTS (SELECT 1` + pendingLogRows + pendingLogInGroup + plan.rows
	args = append(args, plan.rowArgs...)
	if plan.search != "" {
		query += ` AND (` + matched + ` OR r.id LIKE ? ESCAPE '\')`
		args = append(append(args, matchedArgs...), plan.search)
	}
	query += ` AND NOT EXISTS (SELECT 1 FROM usage_events x WHERE x.reservation_id=r.id))`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []pendingLogGroup
	for rows.Next() {
		var group pendingLogGroup
		if err := rows.Scan(&group.account, &group.key, &group.model); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// pendingLogReader counts and pages the pending entries of one read snapshot.
// It finds the reservations flagged for reconciliation that already have a
// usage event once (usually none): a count subtracts theirs and a page reads
// as many more candidates, so the queries scan the pending index alone.
type pendingLogReader struct {
	q        analyticsQuerier
	loaded   bool
	recorded []any // ids of recorded reservations; nil with probe set
	probe    bool  // too many to handle by id: every row probes usage_events
}

func newPendingLogReader(q analyticsQuerier) *pendingLogReader { return &pendingLogReader{q: q} }

// load finds the recorded reservations. The open index yields flagged
// reservations in id order, so the probes walk the event index forward.
func (p *pendingLogReader) load(ctx context.Context) error {
	if p.loaded {
		return nil
	}
	rows, err := p.q.QueryContext(ctx, `SELECT r.id FROM usage_reservations r INDEXED BY idx_usage_reservations_open
 CROSS JOIN usage_events x ON x.reservation_id=r.id WHERE r.status='reserved' AND r.needs_reconciliation=1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var recorded []any
	for rows.Next() {
		if len(recorded) == pendingLogRecordedLimit {
			p.loaded, p.probe = true, true
			return nil
		}
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		recorded = append(recorded, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	p.loaded, p.recorded = true, recorded
	return nil
}

// source is pendingLogRows, without the recorded reservations when they are
// too many to handle by id.
func (p *pendingLogReader) source() string {
	if p.probe {
		return pendingLogRows + ` AND NOT EXISTS (SELECT 1 FROM usage_events x WHERE x.reservation_id=r.id)`
	}
	return pendingLogRows
}

// recordedIn is the condition "column is a recorded reservation".
func (p *pendingLogReader) recordedIn(column string) string {
	return column + ` IN (?` + strings.Repeat(",?", len(p.recorded)-1) + `)`
}

// countSQL counts the rows of source the plan selects.
func (p pendingLogPlan) countSQL(source string, sourceArgs []any) (string, []any) {
	switch {
	case !p.grouped():
		return `SELECT count(*)` + source + p.rows, append(append([]any{}, sourceArgs...), p.rowArgs...)
	case p.search == "":
		return p.groupsSQL("coalesce(sum(g.n),0)", nil, source, sourceArgs, true)
	}
	matched, matchedArgs := p.matchedSQL()
	return p.groupsSQL("coalesce(sum(CASE WHEN "+matched+" THEN g.n ELSE g.n_id END),0)", matchedArgs, source, sourceArgs, true)
}

// recordedSource is pendingLogRows restricted to the recorded reservations,
// looked up by id instead of scanned.
func (p *pendingLogReader) recordedSource() string {
	return ` FROM usage_reservations r WHERE ` + p.recordedIn("r.id") + ` AND r.status='reserved' AND r.needs_reconciliation=1`
}

// count counts the pending entries the filter selects: the rows of the pending
// index minus the recorded ones, which match the same conditions.
func (p *pendingLogReader) count(ctx context.Context, filter domain.RequestLogFilter) (int64, error) {
	plan := planPendingLog(filter)
	if plan.none {
		return 0, nil
	}
	if err := p.load(ctx); err != nil {
		return 0, err
	}
	var total, recorded int64
	query, args := plan.countSQL(p.source(), nil)
	if err := p.q.QueryRowContext(ctx, query, args...).Scan(&total); err != nil || len(p.recorded) == 0 {
		return total, err
	}
	query, args = plan.countSQL(p.recordedSource(), p.recorded)
	err := p.q.QueryRowContext(ctx, query, args...).Scan(&recorded)
	return total - recorded, err
}

// pageCTE defines pending_page(request_id, requested_at): the first n pending
// entries the filter selects in log order, which are all that can reach the
// first n rows merged with events. It is empty when the filter selects none.
// The candidates include one more row per recorded reservation, and the
// recorded ones are dropped from them.
func (p *pendingLogReader) pageCTE(ctx context.Context, filter domain.RequestLogFilter, n int) (cte string, args []any, err error) {
	plan := planPendingLog(filter)
	if plan.none {
		return "", nil, nil
	}
	if err := p.load(ctx); err != nil {
		return "", nil, err
	}
	from := p.source()
	if plan.grouped() {
		matched, matchedArgs := plan.matchedSQL()
		groups, groupArgs := plan.groupsSQL("g.raw_account_id,g.account_generation,g.raw_api_key_id,g.model,"+matched+" AS matched",
			matchedArgs, pendingLogRows, nil, false)
		cte, args = `pending_groups AS MATERIALIZED (`+groups+`), `, groupArgs
		from = ` FROM pending_groups g CROSS JOIN` + strings.TrimPrefix(from, " FROM") + pendingLogInGroup
	}
	candidates := `SELECT r.id AS request_id,r.created_at AS requested_at` + from + plan.rows
	args = append(args, plan.rowArgs...)
	if plan.search != "" {
		candidates += ` AND (g.matched OR r.id LIKE ? ESCAPE '\')`
		args = append(args, plan.search)
	}
	candidates += ` ORDER BY r.created_at DESC,r.id DESC LIMIT ?`
	if len(p.recorded) == 0 {
		return cte + `pending_page AS MATERIALIZED (` + candidates + `), `, append(args, n), nil
	}
	args = append(append(append(args, n+len(p.recorded)), p.recorded...), n)
	return cte + `pending_page AS MATERIALIZED (SELECT request_id,requested_at FROM (` + candidates + `)
 WHERE NOT ` + p.recordedIn("request_id") + ` ORDER BY requested_at DESC,request_id DESC LIMIT ?), `, args, nil
}
