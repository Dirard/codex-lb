package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"codex-lb/internal/application"
)

// usageRollupLayout identifies the rollup tables and histogram precision this
// code folds and reads (usage_rollup_state.layout); another layout belongs to
// the release that wrote it.
const usageRollupLayout = 1

// rollupDeleteBatch bounds one rebuild delete job.
const rollupDeleteBatch = 5000

// rollupMaxBatch bounds the rows one fold job may read.
const rollupMaxBatch = 100000

// errRollupInconsistent rolls a fold job back when a rollup row would get a
// negative count or keep measures without requests; the folder then rebuilds.
var errRollupInconsistent = errors.New("usage rollups are inconsistent")

type rollupState struct {
	layout                  int64
	status                  string
	foldedRowid, foldedRows int64
	anchor                  sql.NullString
	watermark               sql.NullInt64
}

func (st rollupState) ready() bool { return st.layout == usageRollupLayout && st.status == "ready" }

type rollupRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadRollupState(ctx context.Context, q rollupRowQuerier) (rollupState, error) {
	var st rollupState
	err := q.QueryRowContext(ctx, `SELECT layout,status,folded_rowid,folded_rows,anchor_request_id,import_watermark
 FROM usage_rollup_state WHERE id=1`).Scan(&st.layout, &st.status, &st.foldedRowid, &st.foldedRows, &st.anchor, &st.watermark)
	return st, err
}

// rollupImage is one event image of usage_events or usage_rollup_journal with
// the shared predicates evaluated: conversation is the trimmed id when the
// event counts for a conversation, inReports the in_reports flag.
type rollupImage struct {
	requestID                                                   string
	at                                                          int64
	key, account, userAgent, effort                             sql.NullString
	model, status, errorCode                                    string
	deleted                                                     bool
	input, output, cached, reasoning, known, cost, queue, total int64
	ttft                                                        sql.NullInt64
	conversation                                                string
	inReports                                                   bool
}

// rollupImageSQL selects the image columns of alias for scanRollupImage.
func rollupImageSQL(alias string) string {
	return strings.ReplaceAll(`@.request_id,@.requested_at,@.api_key_id,@.account_id,@.model,@.status,
 @.error_code,@.legacy_deleted,@.useragent_group,@.reasoning_effort,@.input_tokens,@.output_tokens,
 @.cached_input_tokens,@.reasoning_tokens,@.reasoning_tokens_known,@.cost_microdollars,@.queue_latency_ms,
 @.total_latency_ms,@.latency_first_token_ms,CASE WHEN `, "@", alias) + rollupConversationSQL(alias) +
		` THEN trim(` + alias + `.conversation_id) ELSE '' END,(` + rollupInReportsSQL(alias) + `)`
}

func (img *rollupImage) fields() []any {
	return []any{&img.requestID, &img.at, &img.key, &img.account, &img.model, &img.status, &img.errorCode,
		&img.deleted, &img.userAgent, &img.effort, &img.input, &img.output, &img.cached, &img.reasoning,
		&img.known, &img.cost, &img.queue, &img.total, &img.ttft, &img.conversation, &img.inReports}
}

// rollupStatusClass counts success/completed as ok, cancelled as cancelled
// and every other status as an error, as every report does.
func rollupStatusClass(status string) (errored, cancelled int64) {
	switch status {
	case "success", "completed":
		return 0, 0
	case "cancelled":
		return 0, 1
	}
	return 1, 0
}

// rollupMeasureColumns are the measures M of design §2.2, in field order.
const rollupMeasureColumns = `requests,errors,cancelled,input_tokens,output_tokens,reasoning_tokens,
 reasoning_known,cached_input_clamped,cost_microdollars`

type rollupMeasures struct {
	requests, errors, cancelled, input, output, reasoning, known, cached, cost int64
}

// add counts an image with sign; sign 0 is a cost-only delta (repricing).
func (m *rollupMeasures) add(img *rollupImage, sign int64) {
	if sign == 0 {
		m.cost += img.cost
		return
	}
	errored, cancelled := rollupStatusClass(img.status)
	m.requests += sign
	m.errors += sign * errored
	m.cancelled += sign * cancelled
	m.input += sign * img.input
	m.output += sign * img.output
	m.reasoning += sign * img.reasoning
	m.known += sign * img.known
	m.cached += sign * max(0, min(img.cached, img.input))
	m.cost += sign * img.cost
}

func (m rollupMeasures) plus(o rollupMeasures) rollupMeasures {
	return rollupMeasures{m.requests + o.requests, m.errors + o.errors, m.cancelled + o.cancelled,
		m.input + o.input, m.output + o.output, m.reasoning + o.reasoning, m.known + o.known,
		m.cached + o.cached, m.cost + o.cost}
}

// consistent: the counts derived from statuses stay within the requests and
// a row without requests holds nothing else. Stored values such as tokens are
// not checked for sign: imported history may carry negative values, which
// must not rebuild the rollups forever.
func (m rollupMeasures) consistent() bool {
	if m.requests < 0 || m.errors < 0 || m.cancelled < 0 || m.errors+m.cancelled > m.requests {
		return false
	}
	return m.requests > 0 || m == rollupMeasures{}
}

func (m *rollupMeasures) fields() []any {
	return []any{&m.requests, &m.errors, &m.cancelled, &m.input, &m.output, &m.reasoning, &m.known, &m.cached, &m.cost}
}

func (m rollupMeasures) values() []any {
	return []any{m.requests, m.errors, m.cancelled, m.input, m.output, m.reasoning, m.known, m.cached, m.cost}
}

type rollupTotal struct {
	m             rollupMeasures
	conversations int64
	latency       rollupLatency
}

type rollupCubeKey struct {
	hour                       int64
	account, key, model, agent string
	deleted                    bool
}

type rollupCube struct {
	m       rollupMeasures
	latency rollupLatency
}

// consistentLatency: no histogram has more samples than the row has requests.
func consistentLatency(m rollupMeasures, l rollupLatency) bool {
	if l.negative() {
		return false
	}
	for _, h := range []rollupHist{l.ttft, l.queue, l.tps, l.total} {
		if h.total() > m.requests {
			return false
		}
	}
	return true
}

type rollupSliceKey struct {
	kind          string
	grain, bucket int64
	dim           string
}

type rollupErrorKey struct {
	hour               int64
	code, account, key string
}

type rollupConversationKey struct {
	hour                                             int64
	conversation, account, key, model, effort, agent string
	deleted, inReports                               bool
}

// rollupConversation is one conversation-hour row or its delta: first and
// last cover only the positive images of a delta; negative marks a delta that
// removed events, after which first and last are recomputed.
type rollupConversation struct {
	requests, input, output, cached, cost, latency int64
	first, last                                    int64
	lastRequest                                    string
	positive, negative                             bool
}

func newRollupConversation() *rollupConversation {
	return &rollupConversation{first: math.MaxInt64, last: math.MinInt64}
}

// observe extends first and last by one event.
func (c *rollupConversation) observe(at int64, requestID string) {
	c.positive = true
	c.first = min(c.first, at)
	if at > c.last || at == c.last && requestID > c.lastRequest {
		c.last, c.lastRequest = at, requestID
	}
}

type rollupLogKey struct {
	hour                                int64
	account, key, model, effort, status string
}

type rollupDimKey struct{ kind, value, detail string }

// rollupDelta accumulates signed event images by rollup row key. The folder
// applies one to the tables; the verifier compares one built from raw events
// with the tables.
type rollupDelta struct {
	totals        map[int64]*rollupTotal
	cubes         map[rollupCubeKey]*rollupCube
	slices        map[rollupSliceKey]*rollupMeasures
	errors        map[rollupErrorKey]int64
	conversations map[rollupConversationKey]*rollupConversation
	logs          map[rollupLogKey]int64
	dims          map[rollupDimKey]int64
}

func newRollupDelta() *rollupDelta {
	return &rollupDelta{totals: map[int64]*rollupTotal{}, cubes: map[rollupCubeKey]*rollupCube{},
		slices: map[rollupSliceKey]*rollupMeasures{}, errors: map[rollupErrorKey]int64{},
		conversations: map[rollupConversationKey]*rollupConversation{}, logs: map[rollupLogKey]int64{},
		dims: map[rollupDimKey]int64{}}
}

// add maps one image to every rollup row it counts in (design §2.2): report
// tables for in_reports events, conversation-hours for conversation events,
// request-log facets for all events. Dimensions use the raw report encoding.
func (d *rollupDelta) add(img *rollupImage, sign int64) {
	hour := img.at / 3600000 * 3600
	account, key := rawReportDimension(img.account), rawReportDimension(img.key)
	agent, effort := rawReportDimension(img.userAgent), rawReportDimension(img.effort)
	if img.inReports {
		total := d.totals[hour]
		if total == nil {
			total = &rollupTotal{}
			d.totals[hour] = total
		}
		cubeKey := rollupCubeKey{hour, account, key, img.model, agent, img.deleted}
		cube := d.cubes[cubeKey]
		if cube == nil {
			cube = &rollupCube{}
			d.cubes[cubeKey] = cube
		}
		total.m.add(img, sign)
		cube.m.add(img, sign)
		if sign != 0 {
			if img.conversation != "" {
				total.conversations += sign
			}
			total.latency.observe(img.ttft, img.queue, img.output, img.total, sign)
			cube.latency.observe(img.ttft, img.queue, img.output, img.total, sign)
		}
		day := img.at / 86400000 * 86400
		for _, slice := range [...]struct{ kind, dim string }{{"account", account}, {"key", key}, {"model", img.model}, {"useragent", agent}} {
			for _, bucket := range [...][2]int64{{3600, hour}, {86400, day}} {
				sliceKey := rollupSliceKey{slice.kind, bucket[0], bucket[1], slice.dim}
				measures := d.slices[sliceKey]
				if measures == nil {
					measures = &rollupMeasures{}
					d.slices[sliceKey] = measures
				}
				measures.add(img, sign)
			}
		}
		if errored, _ := rollupStatusClass(img.status); sign != 0 && errored == 1 && img.errorCode != "" {
			d.errors[rollupErrorKey{hour, img.errorCode, account, key}] += sign
		}
	}
	if img.conversation != "" {
		conversationKey := rollupConversationKey{hour, img.conversation, account, key, img.model, effort, agent,
			img.deleted, img.inReports}
		c := d.conversations[conversationKey]
		if c == nil {
			c = newRollupConversation()
			d.conversations[conversationKey] = c
		}
		if sign == 0 {
			c.cost += img.cost
		} else {
			c.requests += sign
			c.input += sign * img.input
			c.output += sign * img.output
			c.cached += sign * img.cached
			c.cost += sign * img.cost
			c.latency += sign * img.total
			if sign > 0 {
				c.observe(img.at, img.requestID)
			} else {
				c.negative = true
			}
		}
	}
	if sign != 0 {
		d.logs[rollupLogKey{hour, account, key, img.model, effort, img.status}] += sign
		d.dims[rollupDimKey{"account", account, ""}] += sign
		d.dims[rollupDimKey{"key", key, ""}] += sign
		d.dims[rollupDimKey{"model", img.model, effort}] += sign
		d.dims[rollupDimKey{"status", img.status, ""}] += sign
	}
}

// rollupWriter runs the fold job's statements, preparing each once; the
// failpoint, set only by tests, fails a chosen write.
type rollupWriter struct {
	tx        *sql.Tx
	failpoint func() error
	prepared  map[string]*sql.Stmt
}

func (w *rollupWriter) stmt(ctx context.Context, query string) (*sql.Stmt, error) {
	if statement := w.prepared[query]; statement != nil {
		return statement, nil
	}
	statement, err := w.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	w.prepared[query] = statement
	return statement, nil
}

func (w *rollupWriter) fail() error {
	if w.failpoint != nil {
		return w.failpoint()
	}
	return nil
}

func (w *rollupWriter) exec(ctx context.Context, query string, args ...any) error {
	if err := w.fail(); err != nil {
		return err
	}
	statement, err := w.stmt(ctx, query)
	if err != nil {
		return err
	}
	_, err = statement.ExecContext(ctx, args...)
	return err
}

// scan runs a single-row query, including writes with RETURNING; found is
// false when it returned no row.
func (w *rollupWriter) scan(ctx context.Context, query string, args []any, dest ...any) (found bool, err error) {
	if err := w.fail(); err != nil {
		return false, err
	}
	statement, err := w.stmt(ctx, query)
	if err != nil {
		return false, err
	}
	err = statement.QueryRowContext(ctx, args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (w *rollupWriter) close() {
	for _, statement := range w.prepared {
		_ = statement.Close()
	}
}

// rollupMeasureUpsert adds excluded measures to an existing row.
var rollupMeasureUpsert = func() string {
	var parts []string
	for _, column := range strings.Split(rollupMeasureColumns, ",") {
		column = strings.TrimSpace(column)
		parts = append(parts, column+"="+column+"+excluded."+column)
	}
	return strings.Join(parts, ",")
}()

const rollupHistColumns = `ttft_hist,queue_hist,tps_hist,total_hist`

// applyTx writes the delta with one read or upsert per row key and deletes
// rows whose requests reach zero. It returns the conversation-hour keys a
// negative image left with requests, whose first and last need a recompute.
func (d *rollupDelta) applyTx(ctx context.Context, w *rollupWriter) (map[int64][]rollupConversationKey, error) {
	for hour, delta := range d.totals {
		if err := applyRollupTotalTx(ctx, w, hour, delta); err != nil {
			return nil, err
		}
	}
	for key, delta := range d.cubes {
		if err := applyRollupCubeTx(ctx, w, key, delta); err != nil {
			return nil, err
		}
	}
	for key, delta := range d.slices {
		if *delta == (rollupMeasures{}) {
			continue
		}
		var next rollupMeasures
		if _, err := w.scan(ctx, `INSERT INTO usage_rollup_slice(kind,grain,bucket_epoch,dim,`+rollupMeasureColumns+`)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(kind,grain,bucket_epoch,dim) DO UPDATE SET `+rollupMeasureUpsert+`
 RETURNING `+rollupMeasureColumns, append([]any{key.kind, key.grain, key.bucket, key.dim}, delta.values()...), next.fields()...); err != nil {
			return nil, err
		}
		if !next.consistent() {
			return nil, errRollupInconsistent
		}
		if next.requests == 0 {
			if err := w.exec(ctx, `DELETE FROM usage_rollup_slice WHERE kind=? AND grain=? AND bucket_epoch=? AND dim=?`,
				key.kind, key.grain, key.bucket, key.dim); err != nil {
				return nil, err
			}
		}
	}
	for key, delta := range d.errors {
		if err := applyRollupCountTx(ctx, w, delta, `INSERT INTO usage_rollup_error_hour(bucket_epoch,error_code,account_dim,api_key_dim,errors)
 VALUES(?,?,?,?,?) ON CONFLICT(bucket_epoch,error_code,account_dim,api_key_dim) DO UPDATE SET errors=errors+excluded.errors
 RETURNING errors`, `DELETE FROM usage_rollup_error_hour WHERE bucket_epoch=? AND error_code=? AND account_dim=? AND api_key_dim=?`,
			key.hour, key.code, key.account, key.key); err != nil {
			return nil, err
		}
	}
	for key, delta := range d.logs {
		if err := applyRollupCountTx(ctx, w, delta, `INSERT INTO usage_rollup_log_hour(bucket_epoch,account_dim,api_key_dim,model,effort_dim,status,requests)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(bucket_epoch,account_dim,api_key_dim,model,effort_dim,status) DO UPDATE SET requests=requests+excluded.requests
 RETURNING requests`, `DELETE FROM usage_rollup_log_hour WHERE bucket_epoch=? AND account_dim=? AND api_key_dim=? AND model=? AND effort_dim=? AND status=?`,
			key.hour, key.account, key.key, key.model, key.effort, key.status); err != nil {
			return nil, err
		}
	}
	for key, delta := range d.dims {
		if err := applyRollupCountTx(ctx, w, delta, `INSERT INTO usage_rollup_dim(kind,value,detail,requests) VALUES(?,?,?,?)
 ON CONFLICT(kind,value,detail) DO UPDATE SET requests=requests+excluded.requests RETURNING requests`,
			`DELETE FROM usage_rollup_dim WHERE kind=? AND value=? AND detail=?`, key.kind, key.value, key.detail); err != nil {
			return nil, err
		}
	}
	recompute := map[int64][]rollupConversationKey{}
	for key, delta := range d.conversations {
		remains, err := applyRollupConversationTx(ctx, w, key, delta)
		if err != nil {
			return nil, err
		}
		if remains && delta.negative {
			recompute[key.hour] = append(recompute[key.hour], key)
		}
	}
	return recompute, nil
}

// applyRollupCountTx upserts a single-count row (keyArgs, then the delta) and
// deletes it at zero.
func applyRollupCountTx(ctx context.Context, w *rollupWriter, delta int64, upsert, remove string, keyArgs ...any) error {
	if delta == 0 {
		return nil
	}
	var next int64
	if _, err := w.scan(ctx, upsert, append(append([]any{}, keyArgs...), delta), &next); err != nil {
		return err
	}
	switch {
	case next < 0:
		return errRollupInconsistent
	case next == 0:
		return w.exec(ctx, remove, keyArgs...)
	}
	return nil
}

func applyRollupTotalTx(ctx context.Context, w *rollupWriter, hour int64, delta *rollupTotal) error {
	var current rollupTotal
	var hists [4][]byte
	found, err := w.scan(ctx, `SELECT `+rollupMeasureColumns+`,conversation_requests,`+rollupHistColumns+`
 FROM usage_rollup_total_hour WHERE bucket_epoch=?`, []any{hour},
		append(current.m.fields(), &current.conversations, &hists[0], &hists[1], &hists[2], &hists[3])...)
	if err != nil {
		return err
	}
	if current.latency, err = decodeRollupLatency(hists[0], hists[1], hists[2], hists[3]); err != nil {
		return errRollupInconsistent
	}
	next := rollupTotal{m: current.m.plus(delta.m), conversations: current.conversations + delta.conversations,
		latency: current.latency.merge(delta.latency, 1)}
	if !next.m.consistent() || next.conversations < 0 || next.conversations > next.m.requests || !consistentLatency(next.m, next.latency) {
		return errRollupInconsistent
	}
	if next.m.requests == 0 {
		if !found {
			return nil
		}
		return w.exec(ctx, `DELETE FROM usage_rollup_total_hour WHERE bucket_epoch=?`, hour)
	}
	ttft, queue, tps, total := next.latency.encode()
	return w.exec(ctx, `INSERT INTO usage_rollup_total_hour(bucket_epoch,`+rollupMeasureColumns+`,conversation_requests,`+rollupHistColumns+`)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(bucket_epoch) DO UPDATE SET requests=excluded.requests,
 errors=excluded.errors,cancelled=excluded.cancelled,input_tokens=excluded.input_tokens,
 output_tokens=excluded.output_tokens,reasoning_tokens=excluded.reasoning_tokens,
 reasoning_known=excluded.reasoning_known,cached_input_clamped=excluded.cached_input_clamped,
 cost_microdollars=excluded.cost_microdollars,conversation_requests=excluded.conversation_requests,
 ttft_hist=excluded.ttft_hist,queue_hist=excluded.queue_hist,tps_hist=excluded.tps_hist,total_hist=excluded.total_hist`,
		append(append([]any{hour}, next.m.values()...), next.conversations, ttft, queue, tps, total)...)
}

func applyRollupCubeTx(ctx context.Context, w *rollupWriter, key rollupCubeKey, delta *rollupCube) error {
	var current rollupCube
	var id int64
	var hists [4][]byte
	keyArgs := []any{key.hour, key.account, key.key, key.model, key.agent, key.deleted}
	found, err := w.scan(ctx, `SELECT id,`+rollupMeasureColumns+`,`+rollupHistColumns+` FROM usage_rollup_cube_hour
 WHERE bucket_epoch=? AND account_dim=? AND api_key_dim=? AND model=? AND ua_dim=? AND deleted=?`, keyArgs,
		append(append([]any{&id}, current.m.fields()...), &hists[0], &hists[1], &hists[2], &hists[3])...)
	if err != nil {
		return err
	}
	if current.latency, err = decodeRollupLatency(hists[0], hists[1], hists[2], hists[3]); err != nil {
		return errRollupInconsistent
	}
	next := rollupCube{m: current.m.plus(delta.m), latency: current.latency.merge(delta.latency, 1)}
	if !next.m.consistent() || !consistentLatency(next.m, next.latency) {
		return errRollupInconsistent
	}
	ttft, queue, tps, total := next.latency.encode()
	switch {
	case next.m.requests == 0 && found:
		return w.exec(ctx, `DELETE FROM usage_rollup_cube_hour WHERE id=?`, id)
	case next.m.requests == 0:
		return nil
	case found:
		return w.exec(ctx, `UPDATE usage_rollup_cube_hour SET requests=?,errors=?,cancelled=?,input_tokens=?,
 output_tokens=?,reasoning_tokens=?,reasoning_known=?,cached_input_clamped=?,cost_microdollars=?,
 ttft_hist=?,queue_hist=?,tps_hist=?,total_hist=? WHERE id=?`, append(next.m.values(), ttft, queue, tps, total, id)...)
	}
	return w.exec(ctx, `INSERT INTO usage_rollup_cube_hour(bucket_epoch,account_dim,api_key_dim,model,ua_dim,deleted,`+
		rollupMeasureColumns+`,`+rollupHistColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		append(append(keyArgs, next.m.values()...), ttft, queue, tps, total)...)
}

// applyRollupConversationTx upserts one conversation-hour delta and reports
// whether the row remains.
func applyRollupConversationTx(ctx context.Context, w *rollupWriter, key rollupConversationKey, delta *rollupConversation) (bool, error) {
	if !delta.positive && !delta.negative && delta.cost == 0 {
		return false, nil
	}
	var id int64
	var next rollupConversation
	if _, err := w.scan(ctx, `INSERT INTO usage_rollup_conversation_hour(bucket_epoch,conversation_id,account_dim,
 api_key_dim,model,effort_dim,ua_dim,deleted,in_reports,requests,input_tokens,output_tokens,cached_input_tokens,
 cost_microdollars,total_latency_ms,first_at,last_at,last_request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(bucket_epoch,conversation_id,account_dim,api_key_dim,model,effort_dim,ua_dim,deleted,in_reports)
 DO UPDATE SET requests=requests+excluded.requests,input_tokens=input_tokens+excluded.input_tokens,
 output_tokens=output_tokens+excluded.output_tokens,cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,
 cost_microdollars=cost_microdollars+excluded.cost_microdollars,total_latency_ms=total_latency_ms+excluded.total_latency_ms,
 first_at=min(first_at,excluded.first_at),
 last_at=CASE WHEN (excluded.last_at,excluded.last_request_id)>(last_at,last_request_id) THEN excluded.last_at ELSE last_at END,
 last_request_id=CASE WHEN (excluded.last_at,excluded.last_request_id)>(last_at,last_request_id)
 THEN excluded.last_request_id ELSE last_request_id END
 RETURNING id,requests,input_tokens,output_tokens,cached_input_tokens,cost_microdollars,total_latency_ms`,
		[]any{key.hour, key.conversation, key.account, key.key, key.model, key.effort, key.agent, key.deleted, key.inReports,
			delta.requests, delta.input, delta.output, delta.cached, delta.cost, delta.latency, delta.first, delta.last, delta.lastRequest},
		&id, &next.requests, &next.input, &next.output, &next.cached, &next.cost, &next.latency); err != nil {
		return false, err
	}
	switch {
	case next.requests < 0, next.requests == 0 && (next.input != 0 || next.output != 0 || next.cached != 0 || next.cost != 0 || next.latency != 0):
		return false, errRollupInconsistent
	case next.requests == 0:
		return false, w.exec(ctx, `DELETE FROM usage_rollup_conversation_hour WHERE id=?`, id)
	}
	return true, nil
}

// recomputeRollupConversationsTx resets first and last of conversation-hours
// that lost events: one pass over each affected hour's folded events.
func recomputeRollupConversationsTx(ctx context.Context, w *rollupWriter, hours map[int64][]rollupConversationKey, cursor int64) error {
	for hour, keys := range hours {
		wanted := make(map[rollupConversationKey]*rollupConversation, len(keys))
		for _, key := range keys {
			wanted[key] = newRollupConversation()
		}
		if err := w.fail(); err != nil {
			return err
		}
		rows, err := w.tx.QueryContext(ctx, `SELECT `+rollupImageSQL("e")+` FROM usage_events e INDEXED BY idx_usage_events_time_id
 WHERE e.requested_at>=? AND e.requested_at<? AND e.rowid<=? AND `+rollupConversationSQL("e"), hour*1000, (hour+3600)*1000, cursor)
		if err != nil {
			return err
		}
		for rows.Next() {
			var img rollupImage
			if err := rows.Scan(img.fields()...); err != nil {
				rows.Close()
				return err
			}
			key := rollupConversationKey{hour, img.conversation, rawReportDimension(img.account), rawReportDimension(img.key),
				img.model, rawReportDimension(img.effort), rawReportDimension(img.userAgent), img.deleted, img.inReports}
			if c := wanted[key]; c != nil {
				c.observe(img.at, img.requestID)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for key, c := range wanted {
			// Without folded events the row awaits its journal rows; the next
			// batch that removes them recomputes it again.
			if !c.positive {
				continue
			}
			if err := w.exec(ctx, `UPDATE usage_rollup_conversation_hour SET first_at=?,last_at=?,last_request_id=?
 WHERE bucket_epoch=? AND conversation_id=? AND account_dim=? AND api_key_dim=? AND model=? AND effort_dim=?
 AND ua_dim=? AND deleted=? AND in_reports=?`, c.first, c.last, c.lastRequest, key.hour, key.conversation,
				key.account, key.key, key.model, key.effort, key.agent, key.deleted, key.inReports); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameWatermark(a, b sql.NullInt64) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Int64 == b.Int64)
}

// FoldUsageRollups is the workflow "check layout, anchor and import watermark
// --> apply up to maxJournal journal rows --> fold up to maxRows events after
// the cursor --> advance the cursor" as one batch-writer job (design §3.1).
// The job is atomic: an error rolls it back and the next call redoes the same
// batch. An inconsistency folds nothing and returns its reason in Rebuild.
func (s *Store) FoldUsageRollups(ctx context.Context, maxRows, maxJournal int) (application.UsageRollupFold, error) {
	var fold application.UsageRollupFold
	if maxRows < 1 || maxJournal < 1 || maxRows > rollupMaxBatch || maxJournal > rollupMaxBatch {
		return fold, ErrInvalid
	}
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		started := time.Now()
		fold = application.UsageRollupFold{}
		defer func() { fold.Held = time.Since(started) }()
		state, err := loadRollupState(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case state.layout != usageRollupLayout:
			fold.Foreign = true
			return nil
		case state.status != "ready":
			fold.Rebuilding = true
			return nil
		}
		if fold.Rebuild, err = rollupStateMismatchTx(ctx, tx, state); err != nil || fold.Rebuild != "" {
			return err
		}
		w := &rollupWriter{tx: tx, failpoint: s.rollupFailpoint, prepared: map[string]*sql.Stmt{}}
		defer w.close()
		delta := newRollupDelta()
		lastSeq, err := readRollupJournalTx(ctx, tx, delta, maxJournal, &fold.Journal)
		if err != nil {
			return err
		}
		cursor, anchor := state.foldedRowid, state.anchor
		rows, err := tx.QueryContext(ctx, `SELECT e.rowid,`+rollupImageSQL("e")+` FROM usage_events AS e NOT INDEXED
 WHERE e.rowid>? ORDER BY e.rowid LIMIT ?`, cursor, maxRows)
		if err != nil {
			return err
		}
		for rows.Next() {
			var img rollupImage
			if err := rows.Scan(append([]any{&cursor}, img.fields()...)...); err != nil {
				rows.Close()
				return err
			}
			anchor = sql.NullString{String: img.requestID, Valid: true}
			delta.add(&img, 1)
			fold.Rows++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		recompute, err := delta.applyTx(ctx, w)
		if err != nil {
			return err
		}
		if err := recomputeRollupConversationsTx(ctx, w, recompute, cursor); err != nil {
			return err
		}
		if fold.Journal > 0 {
			if err := w.exec(ctx, `DELETE FROM usage_rollup_journal WHERE seq<=?`, lastSeq); err != nil {
				return err
			}
		}
		var watermark sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT hourly_folded_through FROM legacy_import_state WHERE id=1`).Scan(&watermark); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := w.exec(ctx, `UPDATE usage_rollup_state SET folded_rowid=?,folded_rows=folded_rows+?,
 anchor_request_id=?,import_watermark=?,updated_at=? WHERE id=1`, cursor, fold.Rows, anchor, watermark, millis(time.Now())); err != nil {
			return err
		}
		var tail, journal int64
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT coalesce(max(rowid),0) FROM usage_events)-?,
 coalesce((SELECT max(seq)-min(seq)+1 FROM usage_rollup_journal),0)`, cursor).Scan(&tail, &journal); err != nil {
			return err
		}
		fold.LagRows, fold.Backlog = max(tail, 0)+journal, tail > 0 || journal > 0
		return nil
	})
	if errors.Is(err, errRollupInconsistent) {
		return application.UsageRollupFold{Rebuild: "negative_measure"}, nil
	}
	return fold, err
}

// rollupStateMismatchTx names an inconsistency the cursor shows: VACUUM or a
// restore renumbered the anchor event, or a legacy import changed the
// watermark that decides which events count in reports.
func rollupStateMismatchTx(ctx context.Context, tx *sql.Tx, state rollupState) (string, error) {
	if state.foldedRowid > 0 {
		// Deletions keep the cursor at or below the highest rowid, so a cursor
		// above it means renumbered rowids too.
		var requestID sql.NullString
		var top int64
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT request_id FROM usage_events WHERE rowid=?),
 (SELECT coalesce(max(rowid),0) FROM usage_events)`, state.foldedRowid).Scan(&requestID, &top); err != nil {
			return "", err
		}
		if top < state.foldedRowid || requestID.Valid && (!state.anchor.Valid || state.anchor.String != requestID.String) {
			return "anchor", nil
		}
	}
	var watermark sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT hourly_folded_through FROM legacy_import_state WHERE id=1`).Scan(&watermark); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if sameWatermark(watermark, state.watermark) {
		return "", nil
	}
	var journal bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_rollup_journal)`).Scan(&journal); err != nil {
		return "", err
	}
	if state.foldedRowid > 0 || journal {
		return "watermark", nil
	}
	return "", nil
}

// readRollupJournalTx adds up to limit journal rows to delta in sequence
// order and returns the last sequence read.
func readRollupJournalTx(ctx context.Context, tx *sql.Tx, delta *rollupDelta, limit int, count *int) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT j.seq,j.sign,`+rollupImageSQL("j")+`
 FROM usage_rollup_journal j ORDER BY j.seq LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var img rollupImage
		var sign int64
		if err := rows.Scan(append([]any{&last, &sign}, img.fields()...)...); err != nil {
			return 0, err
		}
		delta.add(&img, sign)
		*count++
	}
	return last, rows.Err()
}

// MarkUsageRollupsRebuilding is the first rebuild step: the cursor returns to
// zero and the journal is cleared in one job, so from here the triggers
// journal nothing and analytics read raw events until the rebuild is ready.
func (s *Store) MarkUsageRollupsRebuilding(ctx context.Context) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		state, err := loadRollupState(ctx, tx)
		if err != nil {
			return err
		}
		if state.layout != usageRollupLayout {
			return fmt.Errorf("usage rollup layout %d: %w", state.layout, ErrInvalid)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE usage_rollup_state SET status='rebuilding',folded_rowid=0,folded_rows=0,
 anchor_request_id=NULL,import_watermark=NULL,updated_at=? WHERE id=1`, millis(time.Now())); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM usage_rollup_journal`)
		return err
	})
}

// rollupTables are the rollup tables with the key a rebuild deletes by.
var rollupTables = []struct{ name, key string }{
	{"usage_rollup_total_hour", "bucket_epoch"},
	{"usage_rollup_cube_hour", "id"},
	{"usage_rollup_slice", "(kind,grain,bucket_epoch,dim)"},
	{"usage_rollup_error_hour", "(bucket_epoch,error_code,account_dim,api_key_dim)"},
	{"usage_rollup_conversation_hour", "id"},
	{"usage_rollup_log_hour", "(bucket_epoch,account_dim,api_key_dim,model,effort_dim,status)"},
	{"usage_rollup_dim", "(kind,value,detail)"},
}

// RebuildUsageRollups is the workflow "mark rebuilding --> delete rollup rows
// in jobs of 5000 --> ready" (design §3.3); the folder then backfills every
// event from rowid zero. It resumes an interrupted rebuild.
func (s *Store) RebuildUsageRollups(ctx context.Context) error {
	if err := s.MarkUsageRollupsRebuilding(ctx); err != nil {
		return err
	}
	for _, table := range rollupTables {
		key := strings.Trim(table.key, "()")
		statement := `DELETE FROM ` + table.name + ` WHERE ` + table.key + ` IN (SELECT ` + key + ` FROM ` + table.name + ` LIMIT ?)`
		for deleted := int64(rollupDeleteBatch); deleted == rollupDeleteBatch; {
			result, err := s.exec(ctx, statement, rollupDeleteBatch)
			if err != nil {
				return err
			}
			if deleted, err = result.RowsAffected(); err != nil {
				return err
			}
		}
	}
	_, err := s.exec(ctx, `UPDATE usage_rollup_state SET status='ready',updated_at=? WHERE id=1 AND status='rebuilding'`, millis(time.Now()))
	return err
}

// CheckUsageRollupCount compares folded_rows with the events at or below the
// cursor in one read snapshot, a scan of the request-id index, and the cursor
// with the highest rowid. VACUUM and restored backups may renumber rowids; a
// mismatch requires a rebuild.
func (s *Store) CheckUsageRollupCount(ctx context.Context) (application.UsageRollupCheck, error) {
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
	var live, top int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM usage_events INDEXED BY sqlite_autoindex_usage_events_1
 WHERE rowid<=?),(SELECT coalesce(max(rowid),0) FROM usage_events)`, state.foldedRowid).Scan(&live, &top); err != nil {
		return check, err
	}
	check.Checked = true
	switch {
	case top < state.foldedRowid:
		check.Mismatch = "folded_rowid"
	case live != state.foldedRows:
		check.Mismatch = "folded_rows"
	}
	return check, tx.Commit()
}
