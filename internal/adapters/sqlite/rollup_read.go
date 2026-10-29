package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Bounds of the unfolded tail and the pending journal a rollup read combines
// (design §4.1); beyond them, or before a backfill, reads use raw events.
const (
	rollupReadTailLimit    = 20000
	rollupReadJournalLimit = 20000
)

// errRollupDeclined makes a rollup reader hand the request to the raw path in
// the same snapshot: a filter or a pending change it cannot evaluate exactly.
var errRollupDeclined = errors.New("usage rollups cannot serve this read")

// rollupRead is one analytics read snapshot whose rollups can serve it.
type rollupRead struct {
	tx    *sql.Tx
	state rollupState
}

// openRollupRead checks, inside the read transaction, that the rollups are
// usable: layout 1 and ready, an unfolded tail and a journal within their
// bounds, the cursor anchor in place and the import watermark the rollups were
// folded with. It returns nil when the raw path must serve.
func openRollupRead(ctx context.Context, tx *sql.Tx) (*rollupRead, error) {
	state, err := loadRollupState(ctx, tx)
	if err != nil || !state.ready() {
		return nil, err
	}
	var top, journal int64
	var anchor sql.NullString
	var watermark sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT coalesce(max(rowid),0) FROM usage_events),
 (SELECT request_id FROM usage_events WHERE rowid=?),
 (SELECT count(*) FROM (SELECT 1 FROM usage_rollup_journal LIMIT ?)),
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1)`, state.foldedRowid, rollupReadJournalLimit+1).
		Scan(&top, &anchor, &journal, &watermark); err != nil {
		return nil, err
	}
	switch {
	case top < state.foldedRowid, top-state.foldedRowid > rollupReadTailLimit, journal > rollupReadJournalLimit:
		return nil, nil
	case state.foldedRowid > 0 && anchor.Valid && (!state.anchor.Valid || state.anchor.String != anchor.String):
		return nil, nil
	case !sameWatermark(watermark, state.watermark) && (state.foldedRowid > 0 || journal > 0):
		return nil, nil
	}
	return &rollupRead{tx: tx, state: state}, nil
}

// analyticsRead is the workflow "open a read snapshot --> rollups usable?
// [yes] rollup reader [no, or declined] raw reader" shared by every analytics
// read; both readers return identical results for the same snapshot.
func analyticsRead[T any](ctx context.Context, s *Store, rollup func(*rollupRead) (T, error), raw func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	reader, err := openRollupRead(ctx, tx)
	if err != nil {
		return zero, err
	}
	if reader != nil {
		result, err := rollup(reader)
		if !errors.Is(err, errRollupDeclined) {
			if err != nil {
				return result, err
			}
			return result, tx.Commit()
		}
	}
	result, err := raw(tx)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// analyticsQuerier is a read transaction or a database handle.
type analyticsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// images is the workflow "raw spans --> every event | rollup spans --> events
// after the cursor and journal rows" that feeds add with the signed event
// images the rollup rows of plan miss (design §4.1). where (alias e) selects
// the reader's events in usage_events and the journal alike.
func (r *rollupRead) images(ctx context.Context, plan rollupPlan, where string, args []any, add func(img *rollupImage, sign int64)) error {
	if spans := plan.raws(); len(spans) > 0 {
		values := strings.TrimSuffix(strings.Repeat("(?,?),", len(spans)), ",")
		spanArgs := make([]any, 0, 2*len(spans)+len(args))
		for _, span := range spans {
			spanArgs = append(spanArgs, span.from, span.to)
		}
		if err := r.scanImages(ctx, `WITH spans(lo,hi) AS (VALUES `+values+`) SELECT 1,`+rollupImageSQL("e")+`
 FROM spans s CROSS JOIN usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=s.lo AND e.requested_at<s.hi AND `+where, append(spanArgs, args...), nil, add); err != nil {
			return err
		}
	}
	return r.unfoldedImages(ctx, plan, where, args, add)
}

// unfoldedImages feeds add with the events after the cursor and the journal
// rows at instants plan reads from rollups.
func (r *rollupRead) unfoldedImages(ctx context.Context, plan rollupPlan, where string, args []any, add func(img *rollupImage, sign int64)) error {
	if err := r.scanImages(ctx, `SELECT 1,`+rollupImageSQL("e")+` FROM usage_events e NOT INDEXED
 WHERE e.rowid>? AND `+where, append([]any{r.state.foldedRowid}, args...), plan, add); err != nil {
		return err
	}
	return r.scanImages(ctx, `SELECT e.sign,`+rollupImageSQL("e")+` FROM usage_rollup_journal e WHERE `+where, args, plan, add)
}

// scanImages feeds add with the images of query (sign first), keeping only
// instants plan reads from rollups when plan is set.
func (r *rollupRead) scanImages(ctx context.Context, query string, args []any, plan rollupPlan, add func(*rollupImage, int64)) error {
	rows, err := r.tx.QueryContext(ctx, query, args...)
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
		if plan == nil || plan.rolledUp(img.at) {
			add(&img, sign)
		}
	}
	return rows.Err()
}

// rollupDimValue encodes a filter value like rawReportDimension encodes the
// stored id it must equal.
func rollupDimValue(value string) string {
	return rawReportDimension(sql.NullString{String: value, Valid: true})
}

// rollupDimValues encodes filter values.
func rollupDimValues(values []string) []string {
	encoded := make([]string, len(values))
	for i, value := range values {
		encoded[i] = rollupDimValue(value)
	}
	return encoded
}

// decodeRollupDim reverses rawReportDimension: \x1f is NULL.
func decodeRollupDim(dim string) sql.NullString {
	if dim == "\x1f" {
		return sql.NullString{}
	}
	return sql.NullString{String: fromLegacyDimension(dim), Valid: true}
}

// reportUserAgentLabel is the user-agent breakdown label of a stored group.
func reportUserAgentLabel(group sql.NullString) string {
	if !group.Valid || group.String == "" {
		return "Missing User-Agent"
	}
	return group.String
}

// reportEventWhere is the report filter of rawReportWhere without the time
// range, over an event image of alias e.
func reportEventWhere(filter reportFilterDims) (string, []any) {
	parts := []string{rollupInReportsSQL("e")}
	var args []any
	if len(filter.accounts) > 0 {
		parts, args = appendInCondition(parts, args, "e.account_id", filter.accounts)
	}
	if len(filter.keys) > 0 {
		parts, args = appendInCondition(parts, args, "e.api_key_id", filter.keys)
	}
	if filter.model != "" {
		parts = append(parts, "e.model=?")
		args = append(args, filter.model)
	}
	if filter.userAgent != "" {
		if filter.userAgent == "Missing User-Agent" {
			parts = append(parts, "e.useragent_group IS NULL")
		} else {
			parts = append(parts, "e.useragent_group=?")
			args = append(args, filter.userAgent)
		}
	}
	return strings.Join(parts, " AND "), args
}

// reportFilterDims are the dimension filters of a report.
type reportFilterDims struct {
	accounts, keys   []string
	model, userAgent string
}

func (f reportFilterDims) empty() bool {
	return len(f.accounts) == 0 && len(f.keys) == 0 && f.model == "" && f.userAgent == ""
}

// rollupDimWhere is the filter over rollup rows of alias c with the account,
// key, model and user-agent dimensions of the cube and conversation tables.
func (f reportFilterDims) rollupDimWhere(alias string) (string, []any) {
	parts := []string{"1=1"}
	var args []any
	if len(f.accounts) > 0 {
		parts, args = appendInCondition(parts, args, alias+".account_dim", rollupDimValues(f.accounts))
	}
	if len(f.keys) > 0 {
		parts, args = appendInCondition(parts, args, alias+".api_key_dim", rollupDimValues(f.keys))
	}
	if f.model != "" {
		parts = append(parts, alias+".model=?")
		args = append(args, f.model)
	}
	if f.userAgent != "" {
		parts = append(parts, alias+".ua_dim=?")
		if f.userAgent == "Missing User-Agent" {
			args = append(args, "\x1f")
		} else {
			args = append(args, rollupDimValue(f.userAgent))
		}
	}
	return strings.Join(parts, " AND "), args
}

// cubeIndex names the cube index for the most selective filter.
func (f reportFilterDims) cubeIndex() string {
	switch {
	case len(f.keys) > 0:
		return " INDEXED BY idx_usage_rollup_cube_key"
	case len(f.accounts) > 0:
		return " INDEXED BY idx_usage_rollup_cube_account"
	case f.model != "":
		return " INDEXED BY idx_usage_rollup_cube_model"
	}
	return ""
}

// eventsExist is the workflow "net image counts per hour --> rollup hours
// newest first until one has events --> remaining image hours" for a
// comparison window's existence check: deletions still in the journal may
// empty an hour that rollup rows hold.
func (r *rollupRead) eventsExist(ctx context.Context, plan rollupPlan, dims reportFilterDims) (bool, error) {
	where, args := reportEventWhere(dims)
	net := map[int64]int64{}
	if err := r.images(ctx, plan, where, args, func(img *rollupImage, sign int64) {
		if sign != 0 {
			net[floorRollupHour(img.at)/1000] += sign
		}
	}); err != nil {
		return false, err
	}
	from, to, ok := plan.hours()
	if ok {
		query := `SELECT bucket_epoch,requests FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?
 ORDER BY bucket_epoch DESC`
		queryArgs := []any{from, to}
		if !dims.empty() {
			cubeWhere, cubeArgs := dims.rollupDimWhere("c")
			query = `SELECT c.bucket_epoch,sum(c.requests) FROM usage_rollup_cube_hour c` + dims.cubeIndex() + `
 WHERE ` + cubeWhere + ` AND c.bucket_epoch>=? AND c.bucket_epoch<? GROUP BY c.bucket_epoch ORDER BY c.bucket_epoch DESC`
			queryArgs = append(cubeArgs, from, to)
		}
		rows, err := r.tx.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var hour, requests int64
			if err := rows.Scan(&hour, &requests); err != nil {
				return false, err
			}
			if !plan.rollupHour(hour) {
				continue
			}
			if requests+net[hour] > 0 {
				return true, nil
			}
			delete(net, hour)
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	for _, n := range net {
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}
