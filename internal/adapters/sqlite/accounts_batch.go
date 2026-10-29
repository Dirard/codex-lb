package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"codex-lb/internal/domain"
)

// ListAccountViews is the read of the accounts list (dashboard redesign use
// case 8, "GET /api/accounts from batch reads"): in one analytics snapshot,
// the non-deleted accounts with their quotas, credit status, quota refusal
// times, group memberships, additional quotas, lifetime usage totals and
// token ciphertexts, and the usage of each account over the
// AccountUsageWindowDuration ending at now. The number of statements does not
// depend on the number of accounts. Usage comes from the account slices (UTC
// days inside the window, hours at its edges) with raw partial hours, the
// unfolded tail, the journal and folded legacy hours; while the rollups are
// unusable, from the raw events of the window, with identical results.
func (s *Store) ListAccountViews(ctx context.Context, now time.Time) (domain.AccountViews, error) {
	if now.IsZero() {
		return domain.AccountViews{}, ErrInvalid
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.AccountViews, error) {
		views, err := listAccountViewsTx(ctx, r.tx)
		if err != nil {
			return domain.AccountViews{}, err
		}
		sums := newAccountUsageSums(views, now)
		if err := r.accountUsage(ctx, sums); err != nil {
			return domain.AccountViews{}, err
		}
		return sums.result(views, true), nil
	}, func(tx *sql.Tx) (domain.AccountViews, error) {
		views, err := listAccountViewsTx(ctx, tx)
		if err != nil {
			return domain.AccountViews{}, err
		}
		sums := newAccountUsageSums(views, now)
		if err := sums.readEvents(ctx, tx); err != nil {
			return domain.AccountViews{}, err
		}
		return sums.result(views, false), nil
	})
}

// listAccountViewsTx is the step "batch-read the entities": the overview's
// account health read (accounts, quotas, credits, refusals, memberships) plus
// additional quotas, lifetime totals and token ciphertexts, eight statements
// for any number of accounts.
func listAccountViewsTx(ctx context.Context, tx *sql.Tx) ([]domain.AccountView, error) {
	health, err := listAccountHealth(ctx, tx)
	if err != nil {
		return nil, err
	}
	views := make([]domain.AccountView, len(health))
	index := make(map[string]int, len(health))
	for i, entry := range health {
		views[i] = domain.AccountView{Account: entry.Account, Quotas: entry.Quotas, Credits: entry.Credits,
			RefusalAt: entry.RefusalAt, GroupIDs: entry.GroupIDs, AdditionalQuotas: []domain.AccountAdditionalQuota{}}
		index[entry.Account.ID] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT q.account_id,q.quota_key,q.window,q.limit_name,q.metered_feature,
 q.used_percent,q.reset_at,q.window_minutes,q.observed_at FROM account_additional_quotas q
 JOIN accounts a ON a.id=q.account_id WHERE `+notDeletedAccount+` ORDER BY q.account_id,q.quota_key,q.window`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q domain.AccountAdditionalQuota
		var reset, minutes sql.NullInt64
		var observed int64
		if err := rows.Scan(&q.AccountID, &q.QuotaKey, &q.Window, &q.LimitName, &q.MeteredFeature,
			&q.UsedPercent, &reset, &minutes, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		q.ResetAt, q.WindowMinutes, q.ObservedAt = optionalTime(reset), optionalInt(minutes), fromMillis(observed)
		if i, ok := index[q.AccountID]; ok {
			views[i].AdditionalQuotas = append(views[i].AdditionalQuotas, q)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT scope_id,request_count,failed_count,input_tokens,output_tokens,
 cached_input_tokens,reasoning_tokens,cost_microdollars FROM usage_totals WHERE scope='account'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var totals domain.UsageTotals
		if err := rows.Scan(&id, &totals.RequestCount, &totals.FailedCount, &totals.Usage.InputTokens,
			&totals.Usage.OutputTokens, &totals.Usage.CachedInputTokens, &totals.Usage.ReasoningTokens,
			&totals.Usage.CostMicrodollars); err != nil {
			rows.Close()
			return nil, err
		}
		// As UsageTotals: cached input never exceeds input.
		totals.Usage.CachedInputTokens = min(totals.Usage.CachedInputTokens, totals.Usage.InputTokens)
		if i, ok := index[id]; ok {
			views[i].Totals = totals
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	// External API keys are never read: the list describes ChatGPT tokens only.
	rows, err = tx.QueryContext(ctx, `SELECT c.account_id,c.access_token_encrypted,c.refresh_token_encrypted,
 c.id_token_encrypted,a.generation,a.route_revision FROM account_credentials c JOIN accounts a ON a.id=c.account_id
 WHERE `+notDeletedAccount)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c domain.AccountCredential
		if err := rows.Scan(&c.AccountID, &c.AccessTokenEncrypted, &c.RefreshTokenEncrypted, &c.IDTokenEncrypted,
			&c.Generation, &c.RouteRevision); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[c.AccountID]; ok {
			views[i].Credential = &c
		}
	}
	return views, closeRows(rows)
}

// accountUsageCell sums one account over the window: rollup or image
// measures with raw microdollars, and the folded legacy cost.
type accountUsageCell struct {
	m      rollupMeasures
	legacy float64
}

// accountUsageSums accumulates the window of every listed account, keyed by
// the account's rollup dimension.
type accountUsageSums struct {
	from, to int64
	index    map[string]int
	cells    []accountUsageCell
}

func newAccountUsageSums(views []domain.AccountView, now time.Time) *accountUsageSums {
	to := millis(now)
	sums := &accountUsageSums{from: to - domain.AccountUsageWindowDuration.Milliseconds(), to: to,
		index: make(map[string]int, len(views)), cells: make([]accountUsageCell, len(views))}
	for i, view := range views {
		sums.index[rollupDimValue(view.Account.ID)] = i
	}
	return sums
}

func (a *accountUsageSums) cell(dim string) *accountUsageCell {
	if i, ok := a.index[dim]; ok {
		return &a.cells[i]
	}
	return nil
}

func (a *accountUsageSums) addImage(img *rollupImage, sign int64) {
	if cell := a.cell(rawReportDimension(img.account)); cell != nil {
		cell.m.add(img, sign)
	}
}

// accountUsageMeasures is the select list of per-account sums over events of
// alias e, in the order scanMeasures reads them.
const accountUsageMeasures = `count(*),coalesce(sum(e.input_tokens),0),coalesce(sum(e.output_tokens),0),
 coalesce(sum(e.cost_microdollars),0)`

// scanMeasures adds per-dimension sums (dimension, requests, input, output,
// microdollars) to the cells; decode maps the scanned dimension.
func (a *accountUsageSums) scanMeasures(rows *sql.Rows, decode func(sql.NullString) string) error {
	defer rows.Close()
	for rows.Next() {
		var dim sql.NullString
		var m rollupMeasures
		if err := rows.Scan(&dim, &m.requests, &m.input, &m.output, &m.cost); err != nil {
			return err
		}
		if cell := a.cell(decode(dim)); cell != nil {
			cell.m = cell.m.plus(m)
		}
	}
	return rows.Err()
}

// addLegacy adds folded legacy hours whose start lies in the window, as every
// report attributes them.
func (a *accountUsageSums) addLegacy(ctx context.Context, q analyticsQuerier) error {
	facts, args := legacyReportFacts(domain.ReportFilter{}, fromMillis(a.from), fromMillis(a.to))
	rows, err := q.QueryContext(ctx, facts+`SELECT account_id,coalesce(sum(request_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_tokens),0),coalesce(sum(cost_usd),0) FROM facts GROUP BY account_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dim string
		var requests, input, output int64
		var cost float64
		if err := rows.Scan(&dim, &requests, &input, &output, &cost); err != nil {
			return err
		}
		if cell := a.cell(dim); cell != nil {
			cell.m.requests += requests
			cell.m.input += input
			cell.m.output += output
			cell.legacy += cost
		}
	}
	return rows.Err()
}

// readEvents is the raw evaluation: folded legacy hours and the reported
// events of the window summed per account.
func (a *accountUsageSums) readEvents(ctx context.Context, tx *sql.Tx) error {
	if err := a.addLegacy(ctx, tx); err != nil {
		return err
	}
	where, args := reportEventWhere(reportFilterDims{})
	rows, err := tx.QueryContext(ctx, `SELECT e.account_id,`+accountUsageMeasures+`
 FROM usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=? AND e.requested_at<? AND `+where+` GROUP BY e.account_id`, append([]any{a.from, a.to}, args...)...)
	if err != nil {
		return err
	}
	return a.scanMeasures(rows, rawReportDimension)
}

func (a *accountUsageSums) result(views []domain.AccountView, rollups bool) domain.AccountViews {
	for i := range views {
		cell := a.cells[i]
		views[i].Usage7d = domain.AccountUsageWindow{Requests: cell.m.requests, InputTokens: cell.m.input,
			OutputTokens: cell.m.output, CostUSD: reportCostUSD(cell.legacy, cell.m.cost)}
	}
	return domain.AccountViews{Accounts: views, RollupsUsable: rollups}
}

// accountUsage is the rollup reader of ListAccountViews: folded legacy hours,
// the raw partial hours at both edges summed in SQL, the tail and the journal
// at rollup instants, and the account slice rows of the window, five
// statements however many accounts and partial hours there are.
func (r *rollupRead) accountUsage(ctx context.Context, sums *accountUsageSums) error {
	if err := sums.addLegacy(ctx, r.tx); err != nil {
		return err
	}
	plan := planRollupWindow(sums.from, sums.to, nil)
	where, args := reportEventWhere(reportFilterDims{})
	spans := [][2]int64{}
	for _, span := range plan.raws() {
		spans = append(spans, [2]int64{span.from, span.to})
	}
	encoded, err := json.Marshal(spans)
	if err != nil {
		return err
	}
	rows, err := r.tx.QueryContext(ctx, `WITH spans(lo,hi) AS (SELECT json_extract(value,'$[0]'),json_extract(value,'$[1]') FROM json_each(?))
 SELECT e.account_id,`+accountUsageMeasures+`
 FROM spans s CROSS JOIN usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=s.lo AND e.requested_at<s.hi AND `+where+` GROUP BY e.account_id`, append([]any{string(encoded)}, args...)...)
	if err != nil {
		return err
	}
	if err := sums.scanMeasures(rows, rawReportDimension); err != nil {
		return err
	}
	if err := r.unfoldedImages(ctx, plan, where, args, sums.addImage); err != nil {
		return err
	}
	// A window without splits has one rollup span, which sliceRanges covers
	// with at most three ranges.
	hours, days, _ := plan.sliceRanges()
	ranges := make([][3]int64, 0, len(hours)+len(days))
	for _, h := range hours {
		ranges = append(ranges, [3]int64{3600, h[0], h[1]})
	}
	for _, d := range days {
		ranges = append(ranges, [3]int64{86400, d[0], d[1]})
	}
	if encoded, err = json.Marshal(ranges); err != nil {
		return err
	}
	rows, err = r.tx.QueryContext(ctx, `WITH b(grain,lo,hi) AS (SELECT json_extract(value,'$[0]'),json_extract(value,'$[1]'),
 json_extract(value,'$[2]') FROM json_each(?))
 SELECT s.dim,sum(s.requests),sum(s.input_tokens),sum(s.output_tokens),sum(s.cost_microdollars)
 FROM b CROSS JOIN usage_rollup_slice s WHERE s.kind='account' AND s.grain=b.grain
 AND s.bucket_epoch>=b.lo AND s.bucket_epoch<b.hi GROUP BY s.dim`, string(encoded))
	if err != nil {
		return err
	}
	return sums.scanMeasures(rows, func(dim sql.NullString) string { return dim.String })
}
