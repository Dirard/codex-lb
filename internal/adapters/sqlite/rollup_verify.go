package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// rollupVerifyHours is the window the verifier recomputes from raw events:
// the current hour and the 24 before it.
const rollupVerifyHours = 25

// rollupReadCheck compares one analytics read path between the rollups and
// raw events inside the verifier's snapshot and names it on a mismatch.
type rollupReadCheck func(ctx context.Context, tx *sql.Tx, state rollupState, now time.Time) (mismatch string, err error)

// usageRollupReadChecks are the read-path comparisons of design §3.6: the
// one-day dashboard and the seven-day UTC report through the rollup and the
// raw path, which must agree exactly.
func (s *Store) usageRollupReadChecks() []rollupReadCheck {
	return []rollupReadCheck{verifyDashboardRead, verifyReportRead}
}

// verifyDashboardRead compares the one-day dashboard traffic ending at now.
func verifyDashboardRead(ctx context.Context, tx *sql.Tx, state rollupState, now time.Time) (string, error) {
	start := now.Add(-24 * time.Hour)
	got, err := (&rollupRead{tx: tx, state: state}).dashboardTraffic(ctx, start, now, 3600, 24)
	if err != nil {
		return "", err
	}
	want, err := queryDashboardTrafficRaw(ctx, tx, start, now, 3600, 24)
	if err != nil || reflect.DeepEqual(got, want) {
		return "", err
	}
	return "dashboard_1d", nil
}

// verifyReportRead compares the unfiltered seven-day UTC report ending with
// the day of now.
func verifyReportRead(ctx context.Context, tx *sql.Tx, state rollupState, now time.Time) (string, error) {
	day := now.UTC().Truncate(24 * time.Hour)
	filter := domain.ReportFilter{Start: day.AddDate(0, 0, -6), End: day.AddDate(0, 0, 1),
		PreviousStart: day.AddDate(0, 0, -13), Location: time.UTC}
	got, err := (&rollupRead{tx: tx, state: state}).queryReports(ctx, filter)
	if err != nil {
		return "", err
	}
	want, err := queryReportsRaw(ctx, tx, filter)
	if err != nil || reflect.DeepEqual(got, want) {
		return "", err
	}
	return "report_7d", nil
}

// VerifyUsageRollups is the workflow "in one read snapshot: recompute the
// hour-bucket rollup rows of the last day from folded raw events --> compare
// with the tables --> run the read-path checks" (design §3.6). It checks
// nothing while the rollups are not ready or journal rows are pending; the
// caller retries on its next run.
func (s *Store) VerifyUsageRollups(ctx context.Context, now time.Time) (application.UsageRollupCheck, error) {
	var check application.UsageRollupCheck
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return check, err
	}
	defer tx.Rollback()
	state, err := loadRollupState(ctx, tx)
	if err != nil || !state.ready() {
		return check, err
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_rollup_journal)`).Scan(&pending); err != nil || pending {
		return check, err
	}
	to := now.Unix()/3600*3600 + 3600
	from := to - rollupVerifyHours*3600
	expected, err := rawRollupHours(ctx, tx, from, to, state.foldedRowid)
	if err != nil {
		return check, err
	}
	actual, err := readRollupHours(ctx, tx, from, to)
	if err != nil {
		return check, err
	}
	check.Checked = true
	if check.Mismatch = expected.diff(actual); check.Mismatch != "" {
		return check, nil
	}
	for _, compare := range s.usageRollupReadChecks() {
		if check.Mismatch, err = compare(ctx, tx, state, now); err != nil || check.Mismatch != "" {
			return check, err
		}
	}
	return check, tx.Commit()
}

// rawRollupHours folds the events at or below cursor in [from,to) seconds
// into a delta holding only hour-bucket rows.
func rawRollupHours(ctx context.Context, tx *sql.Tx, from, to, cursor int64) (*rollupDelta, error) {
	delta := newRollupDelta()
	rows, err := tx.QueryContext(ctx, `SELECT `+rollupImageSQL("e")+` FROM usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=? AND e.requested_at<? AND e.rowid<=?`, from*1000, to*1000, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var img rollupImage
		if err := rows.Scan(img.fields()...); err != nil {
			return nil, err
		}
		delta.add(&img, 1)
	}
	for key := range delta.slices {
		if key.grain != 3600 {
			delete(delta.slices, key)
		}
	}
	delta.dims = map[rollupDimKey]int64{}
	return delta, rows.Err()
}

// readRollupHours reads the hour-bucket rollup rows of [from,to) seconds:
// totals, cube, hour slices, errors, conversation-hours and request-log
// facets. Day slices and global facet values are not hour-bucketed.
func readRollupHours(ctx context.Context, tx *sql.Tx, from, to int64) (*rollupDelta, error) {
	d := newRollupDelta()
	type reader struct {
		query string
		scan  func(*sql.Rows) error
	}
	var hists [4][]byte
	decode := func(latency *rollupLatency) error {
		decoded, err := decodeRollupLatency(hists[0], hists[1], hists[2], hists[3])
		*latency = decoded
		return err
	}
	readers := []reader{
		{`SELECT bucket_epoch,` + rollupMeasureColumns + `,conversation_requests,` + rollupHistColumns + `
 FROM usage_rollup_total_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var hour int64
			total := &rollupTotal{}
			if err := rows.Scan(append(append([]any{&hour}, total.m.fields()...), &total.conversations,
				&hists[0], &hists[1], &hists[2], &hists[3])...); err != nil {
				return err
			}
			d.totals[hour] = total
			return decode(&total.latency)
		}},
		{`SELECT bucket_epoch,account_dim,api_key_dim,model,ua_dim,deleted,` + rollupMeasureColumns + `,` + rollupHistColumns + `
 FROM usage_rollup_cube_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var key rollupCubeKey
			cube := &rollupCube{}
			if err := rows.Scan(append(append([]any{&key.hour, &key.account, &key.key, &key.model, &key.agent, &key.deleted},
				cube.m.fields()...), &hists[0], &hists[1], &hists[2], &hists[3])...); err != nil {
				return err
			}
			d.cubes[key] = cube
			return decode(&cube.latency)
		}},
		{`SELECT kind,grain,bucket_epoch,dim,` + rollupMeasureColumns + ` FROM usage_rollup_slice
 WHERE grain=3600 AND bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var key rollupSliceKey
			measures := &rollupMeasures{}
			if err := rows.Scan(append([]any{&key.kind, &key.grain, &key.bucket, &key.dim}, measures.fields()...)...); err != nil {
				return err
			}
			d.slices[key] = measures
			return nil
		}},
		{`SELECT bucket_epoch,error_code,account_dim,api_key_dim,errors FROM usage_rollup_error_hour
 WHERE bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var key rollupErrorKey
			var count int64
			if err := rows.Scan(&key.hour, &key.code, &key.account, &key.key, &count); err != nil {
				return err
			}
			d.errors[key] = count
			return nil
		}},
		{`SELECT bucket_epoch,conversation_id,account_dim,api_key_dim,model,effort_dim,ua_dim,deleted,in_reports,
 requests,input_tokens,output_tokens,cached_input_tokens,cost_microdollars,total_latency_ms,first_at,last_at,last_request_id
 FROM usage_rollup_conversation_hour WHERE bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var key rollupConversationKey
			c := &rollupConversation{positive: true}
			if err := rows.Scan(&key.hour, &key.conversation, &key.account, &key.key, &key.model, &key.effort, &key.agent,
				&key.deleted, &key.inReports, &c.requests, &c.input, &c.output, &c.cached, &c.cost, &c.latency,
				&c.first, &c.last, &c.lastRequest); err != nil {
				return err
			}
			d.conversations[key] = c
			return nil
		}},
		{`SELECT bucket_epoch,account_dim,api_key_dim,model,effort_dim,status,requests FROM usage_rollup_log_hour
 WHERE bucket_epoch>=? AND bucket_epoch<?`, func(rows *sql.Rows) error {
			var key rollupLogKey
			var count int64
			if err := rows.Scan(&key.hour, &key.account, &key.key, &key.model, &key.effort, &key.status, &count); err != nil {
				return err
			}
			d.logs[key] = count
			return nil
		}},
	}
	for _, r := range readers {
		rows, err := tx.QueryContext(ctx, r.query, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			if err := r.scan(rows); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// diff names the first rollup table whose rows differ between d and other.
func (d *rollupDelta) diff(other *rollupDelta) string {
	for _, table := range []struct {
		name        string
		left, right any
	}{
		{"usage_rollup_total_hour", d.totals, other.totals},
		{"usage_rollup_cube_hour", d.cubes, other.cubes},
		{"usage_rollup_slice", d.slices, other.slices},
		{"usage_rollup_error_hour", d.errors, other.errors},
		{"usage_rollup_conversation_hour", d.conversations, other.conversations},
		{"usage_rollup_log_hour", d.logs, other.logs},
		{"usage_rollup_dim", d.dims, other.dims},
	} {
		if !reflect.DeepEqual(table.left, table.right) {
			return table.name
		}
	}
	return ""
}
