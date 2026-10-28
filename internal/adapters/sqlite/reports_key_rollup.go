package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// keyTrendBuckets is the rollup reader of KeyTrendBuckets (design §4.2):
// hour-grain key slices through their dimension index, folded legacy hours
// as the raw path sums them, and images for the partial first and last hours,
// the tail and the journal. An hour appears when it holds folded facts or
// events.
func (r *rollupRead) keyTrendBuckets(ctx context.Context, keyID string, since, until time.Time) ([]domain.APIKeyTrendBucket, error) {
	filter := domain.ReportFilter{APIKeyIDs: []string{keyID}}
	facts, args := legacyReportFacts(filter, since, until)
	legacy, err := keyTrendFacts(ctx, r.tx, facts, args)
	if err != nil {
		return nil, err
	}
	type hourSum struct {
		requests, tokens, micro int64
		legacyCost              float64
		folded                  bool
	}
	hours := map[int64]*hourSum{}
	at := func(hour int64) *hourSum {
		sum := hours[hour]
		if sum == nil {
			sum = &hourSum{}
			hours[hour] = sum
		}
		return sum
	}
	for _, hour := range legacy {
		sum := at(hour.at)
		sum.tokens, sum.legacyCost, sum.folded = hour.tokens, hour.legacyCost, true
	}
	plan := planRollupWindow(millis(since), millis(until), nil)
	where, whereArgs := reportEventWhere(reportFilterDims{keys: filter.APIKeyIDs})
	if err := r.images(ctx, plan, where, whereArgs, func(img *rollupImage, sign int64) {
		var m rollupMeasures
		m.add(img, sign)
		sum := at(floorRollupHour(img.at))
		sum.requests += m.requests
		sum.tokens += m.input + m.output
		sum.micro += m.cost
	}); err != nil {
		return nil, err
	}
	if from, to, ok := plan.hours(); ok {
		rows, err := r.tx.QueryContext(ctx, `SELECT bucket_epoch,requests,input_tokens+output_tokens,cost_microdollars
 FROM usage_rollup_slice WHERE kind='key' AND dim=? AND grain=3600 AND bucket_epoch>=? AND bucket_epoch<?`,
			rollupDimValue(keyID), from, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var hour, requests, tokens, micro int64
			if err := rows.Scan(&hour, &requests, &tokens, &micro); err != nil {
				return nil, err
			}
			sum := at(hour * 1000)
			sum.requests += requests
			sum.tokens += tokens
			sum.micro += micro
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	order := make([]int64, 0, len(hours))
	for hour, sum := range hours {
		if sum.folded || sum.requests > 0 {
			order = append(order, hour)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	result := []domain.APIKeyTrendBucket{}
	for _, hour := range order {
		sum := hours[hour]
		result = append(result, domain.APIKeyTrendBucket{At: fromMillis(hour), Tokens: sum.tokens,
			CostUSD: reportCostUSD(sum.legacyCost, sum.micro)})
	}
	return result, nil
}

// keyUsage7Day is the rollup reader of KeyUsage7Day (design §4.2): one pass
// over the key's cube rows through the key index grouped by account and
// deleted flag gives the totals and the account costs, folded legacy hours
// are read as the raw path reads them and images add the partial hours, the
// tail and the journal.
func (r *rollupRead) keyUsage7Day(ctx context.Context, keyID string, since, until time.Time) (domain.APIKeyUsage7DayResponse, error) {
	filter := domain.ReportFilter{APIKeyIDs: []string{keyID}}
	empty := domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}
	legacy, legacyCost, err := legacyWindow(ctx, r.tx, filter, since, until)
	if err != nil {
		return empty, err
	}
	folded, err := keyAccountGroups(ctx, r.tx, filter, since, until, "", nil)
	if err != nil {
		return empty, err
	}
	type groupKey struct {
		account string
		deleted bool
	}
	type groupSum struct {
		m      rollupMeasures
		group  keyAccountGroup
		folded bool
	}
	groups := map[groupKey]*groupSum{}
	at := func(key groupKey) *groupSum {
		sum := groups[key]
		if sum == nil {
			sum = &groupSum{group: keyAccountGroup{account: key.account, deleted: key.deleted}}
			groups[key] = sum
		}
		return sum
	}
	for _, group := range folded {
		sum := at(groupKey{group.account, group.deleted})
		sum.group, sum.folded = group, true
	}
	plan := planRollupWindow(millis(since), millis(until), nil)
	where, args := reportEventWhere(reportFilterDims{keys: filter.APIKeyIDs})
	if err := r.images(ctx, plan, where, args, func(img *rollupImage, sign int64) {
		sum := at(groupKey{rawReportDimension(img.account), img.deleted})
		sum.m.add(img, sign)
	}); err != nil {
		return empty, err
	}
	if from, to, ok := plan.hours(); ok {
		// A plan without splits has one rollup span: the bucket range is exact.
		rows, err := r.tx.QueryContext(ctx, `SELECT c.account_dim,c.deleted,`+rollupMeasureSums("c")+`
 FROM usage_rollup_cube_hour c INDEXED BY idx_usage_rollup_cube_key
 WHERE c.api_key_dim=? AND c.bucket_epoch>=? AND c.bucket_epoch<? GROUP BY c.account_dim,c.deleted`,
			rollupDimValue(keyID), from, to)
		if err != nil {
			return empty, err
		}
		defer rows.Close()
		for rows.Next() {
			var key groupKey
			var m rollupMeasures
			if err := rows.Scan(append([]any{&key.account, &key.deleted}, m.fields()...)...); err != nil {
				return empty, err
			}
			sum := at(key)
			sum.m = sum.m.plus(m)
		}
		if err := rows.Err(); err != nil {
			return empty, err
		}
	}
	var events rollupMeasures
	var ordered []*groupSum
	var missing []string
	for _, sum := range groups {
		events = events.plus(sum.m)
		if !sum.folded && sum.m.requests <= 0 {
			continue
		}
		sum.group.micro = sum.m.cost
		ordered = append(ordered, sum)
		if !sum.folded && sum.group.account != "\x1f" {
			missing = append(missing, fromLegacyDimension(sum.group.account))
		}
	}
	emails, err := r.accountEmails(ctx, missing)
	if err != nil {
		return empty, err
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].group, ordered[j].group
		if a.account != b.account {
			return a.account < b.account
		}
		return !a.deleted && b.deleted
	})
	rows := make([]keyAccountGroup, len(ordered))
	for i, sum := range ordered {
		rows[i] = sum.group
		if !sum.folded && sum.group.account != "\x1f" {
			rows[i].email = emails[fromLegacyDimension(sum.group.account)]
		}
	}
	m := legacy.plus(events)
	usage := reportAggregate{requests: m.requests, input: m.input, output: m.output, cached: m.cached,
		costUSD: reportCostUSD(legacyCost, m.cost)}
	return keyUsageResponse(keyID, usage, rows), nil
}

// accountEmails looks up the emails of account ids.
func (r *rollupRead) accountEmails(ctx context.Context, ids []string) (map[string]sql.NullString, error) {
	emails := map[string]sql.NullString{}
	if len(ids) == 0 {
		return emails, nil
	}
	rows, err := r.tx.QueryContext(ctx, `SELECT id,email FROM accounts WHERE id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, anySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var email sql.NullString
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		emails[id] = email
	}
	return emails, rows.Err()
}

func anySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}
