package sqlite

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"

	"codex-lb/internal/domain"
)

// requestLogFacets counts events by facet value in the raw report encoding;
// a value is present while its count is positive.
type requestLogFacets struct {
	accounts, keys, statuses map[string]int64
	models                   map[[2]string]int64
}

func newRequestLogFacets() *requestLogFacets {
	return &requestLogFacets{accounts: map[string]int64{}, keys: map[string]int64{}, statuses: map[string]int64{},
		models: map[[2]string]int64{}}
}

func (f *requestLogFacets) add(account, key, model, effort, status string, n int64) {
	f.accounts[account] += n
	f.keys[key] += n
	f.models[[2]string{model, effort}] += n
	f.statuses[status] += n
}

func (f *requestLogFacets) addImage(img *rollupImage, sign int64) {
	if sign != 0 {
		f.add(rawReportDimension(img.account), rawReportDimension(img.key), img.model, rawReportDimension(img.effort), img.status, sign)
	}
}

// requestLogCubeEligible reports whether the log cube and the dimension
// table can evaluate the filter: search and conversation filters stay raw.
func requestLogCubeEligible(filter domain.RequestLogFilter) bool {
	return filter.Search == "" && filter.ConversationID == ""
}

// requestLogUnfiltered reports a filter without time bounds or dimension
// filters, which the global facet values and the state counters serve.
func requestLogUnfiltered(filter domain.RequestLogFilter) bool {
	where, _ := requestLogCubeWhere(filter)
	return filter.Since == nil && filter.Until == nil && where == "1=1"
}

// requestLogCubeWhere is requestLogWhere's dimension filter over the log
// cube (alias c); time bounds are planned, search and conversations stay raw.
func requestLogCubeWhere(filter domain.RequestLogFilter) (string, []any) {
	parts := []string{"1=1"}
	var args []any
	parts, args = appendInCondition(parts, args, "c.account_dim", rollupDimValues(filter.AccountIDs))
	parts, args = appendInCondition(parts, args, "c.api_key_dim", rollupDimValues(filter.APIKeyIDs))
	parts, args = appendInCondition(parts, args, "c.status", filter.Statuses)
	parts, args = appendInCondition(parts, args, "c.model", filter.Models)
	parts, args = appendInCondition(parts, args, "c.effort_dim", rollupDimValues(filter.ReasoningEfforts))
	var options []string
	for _, option := range filter.ModelOptions {
		model, effort, hasEffort := strings.Cut(option, ":::")
		if model == "" {
			continue
		}
		options = append(options, "(c.model=? AND c.effort_dim=?)")
		if hasEffort && effort != "" {
			args = append(args, model, rollupDimValue(effort))
		} else {
			args = append(args, model, "\x1f")
		}
	}
	if len(options) > 0 {
		parts = append(parts, "("+strings.Join(options, " OR ")+")")
	}
	return strings.Join(parts, " AND "), args
}

// requestLogPlan plans the filter's time bounds; open bounds reach the
// first and last instant.
func requestLogPlan(filter domain.RequestLogFilter) rollupPlan {
	start, end := int64(math.MinInt64), int64(math.MaxInt64)
	if filter.Since != nil {
		start = millis(*filter.Since)
	}
	if filter.Until != nil {
		end = millis(*filter.Until)
	}
	return planRollupWindow(start, end, nil)
}

// requestLogEventWhere is requestLogWhere without time bounds over an event
// image of alias e.
func requestLogEventWhere(filter domain.RequestLogFilter) (string, []any) {
	filter.Since, filter.Until, filter.Before = nil, nil, nil
	return requestLogWhere(filter)
}

// requestLogFacetCounts counts the filter's events by facet: the dimension
// table without bounds or filters (design §5), the log cube otherwise, plus
// the images rollup rows miss.
func (r *rollupRead) requestLogFacetCounts(ctx context.Context, filter domain.RequestLogFilter) (*requestLogFacets, error) {
	facets := newRequestLogFacets()
	where, args := requestLogEventWhere(filter)
	if requestLogUnfiltered(filter) {
		if err := r.images(ctx, rollupEverything, where, args, facets.addImage); err != nil {
			return nil, err
		}
		rows, err := r.tx.QueryContext(ctx, `SELECT kind,value,detail,requests FROM usage_rollup_dim`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, value, detail string
			var n int64
			if err := rows.Scan(&kind, &value, &detail, &n); err != nil {
				return nil, err
			}
			switch kind {
			case "account":
				facets.accounts[value] += n
			case "key":
				facets.keys[value] += n
			case "model":
				facets.models[[2]string{value, detail}] += n
			case "status":
				facets.statuses[value] += n
			}
		}
		return facets, rows.Err()
	}
	plan := requestLogPlan(filter)
	if err := r.images(ctx, plan, where, args, facets.addImage); err != nil {
		return nil, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return facets, nil
	}
	cubeWhere, cubeArgs := requestLogCubeWhere(filter)
	rows, err := r.tx.QueryContext(ctx, `SELECT c.account_dim,c.api_key_dim,c.model,c.effort_dim,c.status,sum(c.requests)
 FROM usage_rollup_log_hour c WHERE c.bucket_epoch>=? AND c.bucket_epoch<? AND `+cubeWhere+` GROUP BY 1,2,3,4,5`,
		append([]any{from, to}, cubeArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var account, key, model, effort, status string
		var n int64
		if err := rows.Scan(&account, &key, &model, &effort, &status, &n); err != nil {
			return nil, err
		}
		facets.add(account, key, model, effort, status, n)
	}
	return facets, rows.Err()
}

// requestLogOptions is the rollup reader of RequestLogOptions: facet counts
// of the events, the pending entries as the raw path selects them, the key
// names joined in Go and the raw path's order and limit.
func (r *rollupRead) requestLogOptions(ctx context.Context, filter domain.RequestLogFilter) (domain.RequestLogOptions, error) {
	options := emptyRequestLogOptions()
	if !requestLogCubeEligible(filter) {
		return options, errRollupDeclined
	}
	filter.Statuses, filter.Before = nil, nil
	facets, err := r.requestLogFacetCounts(ctx, filter)
	if err != nil {
		return options, err
	}
	where, args := requestLogWhere(filter)
	rows, err := r.tx.QueryContext(ctx, pendingRequestLogSource+`SELECT e.account_id,e.api_key_id,e.model,
 e.reasoning_effort,e.status FROM pending_log_entries e WHERE `+where, args...)
	if err != nil {
		return options, err
	}
	for rows.Next() {
		var account, key, effort sql.NullString
		var model, status string
		if err := rows.Scan(&account, &key, &model, &effort, &status); err != nil {
			rows.Close()
			return options, err
		}
		facets.add(rawReportDimension(account), rawReportDimension(key), model, rawReportDimension(effort), status, 1)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return options, err
	}
	for account, n := range facets.accounts {
		if id := decodeRollupDim(account); n > 0 && id.Valid {
			options.AccountIDs = append(options.AccountIDs, id.String)
		}
	}
	sort.Strings(options.AccountIDs)
	options.AccountIDs = options.AccountIDs[:min(len(options.AccountIDs), 2000)]
	for option, n := range facets.models {
		if n > 0 {
			options.ModelOptions = append(options.ModelOptions, domain.RequestLogModelOption{Model: option[0],
				ReasoningEffort: optionalString(decodeRollupDim(option[1]))})
		}
	}
	sort.Slice(options.ModelOptions, func(i, j int) bool {
		a, b := options.ModelOptions[i], options.ModelOptions[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.ReasoningEffort == nil || b.ReasoningEffort == nil {
			return a.ReasoningEffort == nil && b.ReasoningEffort != nil
		}
		return *a.ReasoningEffort < *b.ReasoningEffort
	})
	options.ModelOptions = options.ModelOptions[:min(len(options.ModelOptions), 2000)]
	if options.APIKeys, err = r.requestLogKeyOptions(ctx, facets.keys); err != nil {
		return options, err
	}
	for status, n := range facets.statuses {
		if n > 0 {
			options.Statuses = append(options.Statuses, status)
		}
	}
	sort.Strings(options.Statuses)
	options.Statuses = options.Statuses[:min(len(options.Statuses), 2000)]
	return options, nil
}

// requestLogKeyOptions joins the present key ids with api_keys, ordered by
// name and id.
func (r *rollupRead) requestLogKeyOptions(ctx context.Context, keys map[string]int64) ([]domain.RequestLogAPIKeyOption, error) {
	options := []domain.RequestLogAPIKeyOption{}
	present := map[string]bool{}
	for key, n := range keys {
		if id := decodeRollupDim(key); n > 0 && id.Valid {
			present[id.String] = true
		}
	}
	if len(present) == 0 {
		return options, nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT id,name,key_prefix FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.RequestLogAPIKeyOption
		var prefix sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &prefix); err != nil {
			return nil, err
		}
		if present[item.ID] {
			item.KeyPrefix = optionalString(prefix)
			options = append(options, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].ID < options[j].ID
	})
	return options[:min(len(options), 2000)], nil
}

// requestLogTotal is the rollup reader of the request-log total (design §5):
// the state counters without filters, the log cube with them, plus the
// pending entries; search and conversation filters stay raw.
func (r *rollupRead) requestLogTotal(ctx context.Context, filter domain.RequestLogFilter) (int64, error) {
	if !requestLogCubeEligible(filter) {
		return 0, errRollupDeclined
	}
	where, args := requestLogWhere(filter)
	var total int64
	if err := r.tx.QueryRowContext(ctx, pendingRequestLogSource+`SELECT count(*) FROM pending_log_entries e WHERE `+where,
		args...).Scan(&total); err != nil {
		return 0, err
	}
	if requestLogUnfiltered(filter) {
		var tail int64
		if err := r.tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_events WHERE rowid>?`, r.state.foldedRowid).Scan(&tail); err != nil {
			return 0, err
		}
		return total + r.state.foldedRows + tail, nil
	}
	plan := requestLogPlan(filter)
	eventWhere, eventArgs := requestLogEventWhere(filter)
	if err := r.images(ctx, plan, eventWhere, eventArgs, func(_ *rollupImage, sign int64) { total += sign }); err != nil {
		return 0, err
	}
	from, to, ok := plan.hours()
	if !ok {
		return total, nil
	}
	cubeWhere, cubeArgs := requestLogCubeWhere(filter)
	var events int64
	if err := r.tx.QueryRowContext(ctx, `SELECT coalesce(sum(c.requests),0) FROM usage_rollup_log_hour c
 WHERE c.bucket_epoch>=? AND c.bucket_epoch<? AND `+cubeWhere, append([]any{from, to}, cubeArgs...)...).Scan(&events); err != nil {
		return 0, err
	}
	return total + events, nil
}
