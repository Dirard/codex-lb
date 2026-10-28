package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
)

var rollupFixtureBase = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

// insertRollupFixture adds n random events with every dimension edge case of
// design §6.1: NULL, empty and \x1f-prefixed ids, all status classes, empty
// and missing user agents, conversations with spaces or blank, warmups,
// imported rows on both sides of the watermark, deleted history, missing
// TTFT and negative queue latency.
func insertRollupFixture(t testing.TB, s *Store, random *rand.Rand, first, n int, span time.Duration) {
	t.Helper()
	insertRollupFixtureAt(t, s, random, first, n, rollupFixtureBase, span)
}

// insertRollupFixtureAt is insertRollupFixture over [base, base+span).
func insertRollupFixtureAt(t testing.TB, s *Store, random *rand.Rand, first, n int, base time.Time, span time.Duration) {
	t.Helper()
	accounts := []any{"acct-0", "acct-1", nil, "", "\x1fodd"}
	keys := []any{"key-0", "key-1", nil, "\x1fk"}
	statuses := []string{"success", "completed", "cancelled", "error", "quota"}
	codes := []string{"", "upstream_error", "client_disconnected"}
	agents := []any{"codex", "sdk", "", nil, "\x1fua"}
	efforts := []any{nil, "low", "high", ""}
	conversations := []any{"conv-a", " conv-a ", "conv-b", "", "  ", nil, "conv-c"}
	kinds := []string{"normal", "normal", "normal", "warmup", "limit_warmup", "compact"}
	for i := first; i < first+n; i++ {
		var ttft, legacy any
		if random.IntN(5) > 0 {
			ttft = random.Int64N(9000) - 10
		}
		if i%23 == 0 {
			legacy = fmt.Sprintf("legacy-%d", i)
		}
		if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,error_code,
 request_kind,requested_at,conversation_id,useragent_group,reasoning_effort,input_tokens,output_tokens,
 cached_input_tokens,reasoning_tokens,reasoning_tokens_known,cost_microdollars,queue_latency_ms,total_latency_ms,
 latency_first_token_ms,legacy_request_id,legacy_deleted) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("req-%06d", i), keys[random.IntN(len(keys))], accounts[random.IntN(len(accounts))],
			fmt.Sprintf("model-%d", random.IntN(3)), statuses[random.IntN(len(statuses))], codes[random.IntN(len(codes))],
			kinds[random.IntN(len(kinds))], millis(base)+random.Int64N(span.Milliseconds()),
			conversations[random.IntN(len(conversations))], agents[random.IntN(len(agents))], efforts[random.IntN(len(efforts))],
			random.Int64N(5000), random.Int64N(800), random.Int64N(6000), random.Int64N(300), random.IntN(2),
			random.Int64N(90000), random.Int64N(60)-5, random.Int64N(40000), ttft, legacy, random.IntN(7)/6); err != nil {
			t.Fatal(err)
		}
	}
}

func setRollupWatermark(t testing.TB, s *Store, at time.Time) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO legacy_import_state(id,source_sha256,key_fingerprint,folded_through,
 hourly_folded_through,imported_at,sidecar_path) VALUES(1,'x','y',?,?,?,'')
 ON CONFLICT(id) DO UPDATE SET hourly_folded_through=excluded.hourly_folded_through`, millis(at), millis(at), millis(at)); err != nil {
		t.Fatal(err)
	}
}

// foldRollups folds until no backlog in jobs of batch rows.
func foldRollups(t testing.TB, s *Store, batch int) application.UsageRollupFold {
	t.Helper()
	var total application.UsageRollupFold
	for {
		fold, err := s.FoldUsageRollups(context.Background(), batch, batch)
		if err != nil {
			t.Fatal(err)
		}
		if fold.Rebuild != "" || fold.Rebuilding || fold.Foreign {
			t.Fatalf("fold needs attention: %+v", fold)
		}
		total.Rows += fold.Rows
		total.Journal += fold.Journal
		if !fold.Backlog {
			return total
		}
	}
}

// rollupRows formats every row of a query as one string.
func rollupRows(t testing.TB, q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string, args ...any) []string {
	t.Helper()
	rows, err := q.Query(query, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(values))
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				value = string(raw)
			}
			parts[i] = fmt.Sprintf("%q", fmt.Sprint(value))
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// rollupDump lists every rollup row without generated ids.
func rollupDump(t testing.TB, s *Store) map[string][]string {
	t.Helper()
	measures := rollupMeasureColumns
	hists := `hex(ttft_hist),hex(queue_hist),hex(tps_hist),hex(total_hist)`
	return map[string][]string{
		"total": rollupRows(t, s.readDB, `SELECT bucket_epoch,`+measures+`,conversation_requests,`+hists+` FROM usage_rollup_total_hour`),
		"cube": rollupRows(t, s.readDB, `SELECT bucket_epoch,account_dim,api_key_dim,model,ua_dim,deleted,`+measures+`,`+hists+`
 FROM usage_rollup_cube_hour`),
		"slice": rollupRows(t, s.readDB, `SELECT kind,grain,bucket_epoch,dim,`+measures+` FROM usage_rollup_slice`),
		"error": rollupRows(t, s.readDB, `SELECT bucket_epoch,error_code,account_dim,api_key_dim,errors FROM usage_rollup_error_hour`),
		"conversation": rollupRows(t, s.readDB, `SELECT bucket_epoch,conversation_id,account_dim,api_key_dim,model,effort_dim,
 ua_dim,deleted,in_reports,requests,input_tokens,output_tokens,cached_input_tokens,cost_microdollars,total_latency_ms,
 first_at,last_at,last_request_id FROM usage_rollup_conversation_hour`),
		"log": rollupRows(t, s.readDB, `SELECT bucket_epoch,account_dim,api_key_dim,model,effort_dim,status,requests FROM usage_rollup_log_hour`),
		"dim": rollupRows(t, s.readDB, `SELECT kind,value,detail,requests FROM usage_rollup_dim`),
	}
}

// oracleDim is the raw report dimension encoding written in SQL.
func oracleDim(column string) string {
	return `CASE WHEN ` + column + ` IS NULL THEN char(31) WHEN substr(` + column + `,1,1)=char(31) THEN char(31)||` +
		column + ` ELSE ` + column + ` END`
}

const oracleMeasures = `count(*),sum(e.status NOT IN ('success','completed','cancelled')),sum(e.status='cancelled'),
 sum(e.input_tokens),sum(e.output_tokens),sum(e.reasoning_tokens),sum(e.reasoning_tokens_known),
 sum(max(0,min(e.cached_input_tokens,e.input_tokens))),sum(e.cost_microdollars)`

// rawRollupOracle recomputes every rollup table from the raw events with SQL
// aggregates; histograms are built from raw values per group in Go.
func rawRollupOracle(t testing.TB, s *Store) map[string][]string {
	t.Helper()
	hour, day := `e.requested_at/3600000*3600`, `e.requested_at/86400000*86400`
	inReports := rollupInReportsSQL("e")
	conversation := `CASE WHEN ` + rollupConversationSQL("e") + ` THEN trim(e.conversation_id) ELSE '' END`
	withHists := func(keys, where string, extra string) []string {
		base := rollupRows(t, s.readDB, `SELECT `+keys+`,`+oracleMeasures+extra+` FROM usage_events e WHERE `+where+` GROUP BY `+keys)
		hists := map[string]*rollupLatency{}
		rows, err := s.readDB.Query(`SELECT ` + keys + `,'|',e.latency_first_token_ms,e.queue_latency_ms,e.output_tokens,
 e.total_latency_ms FROM usage_events e WHERE ` + where)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			n := len(columns) - 5
			parts := make([]string, n)
			for i := range parts {
				parts[i] = fmt.Sprintf("%q", fmt.Sprint(values[i]))
			}
			key := strings.Join(parts, " ")
			if hists[key] == nil {
				hists[key] = &rollupLatency{}
			}
			ttft := sql.NullInt64{}
			if v, ok := values[n+1].(int64); ok {
				ttft = sql.NullInt64{Int64: v, Valid: true}
			}
			hists[key].observe(ttft, values[n+2].(int64), values[n+3].(int64), values[n+4].(int64), 1)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		keyCount := len(splitTopLevel(keys))
		out := make([]string, 0, len(base))
		for _, row := range base {
			fields := strings.SplitAfterN(row, `" `, keyCount+1)
			key := strings.TrimSpace(strings.Join(fields[:keyCount], ""))
			latency := hists[key]
			if latency == nil {
				t.Fatalf("no histogram for %s", key)
				return nil
			}
			ttft, queue, tps, total := latency.encode()
			for _, blob := range [][]byte{ttft, queue, tps, total} {
				row += " " + fmt.Sprintf("%q", strings.ToUpper(hex.EncodeToString(blob)))
			}
			out = append(out, row)
		}
		sort.Strings(out)
		return out
	}
	var slices []string
	for _, kind := range []struct{ name, dim string }{{"account", oracleDim("e.account_id")}, {"key", oracleDim("e.api_key_id")},
		{"model", "e.model"}, {"useragent", oracleDim("e.useragent_group")}} {
		for _, grain := range []struct {
			seconds int
			bucket  string
		}{{3600, hour}, {86400, day}} {
			slices = append(slices, rollupRows(t, s.readDB, fmt.Sprintf(`SELECT '%s',%d,%s,%s,%s FROM usage_events e WHERE %s GROUP BY 3,4`,
				kind.name, grain.seconds, grain.bucket, kind.dim, oracleMeasures, inReports))...)
		}
	}
	sort.Strings(slices)
	conversationKeys := hour + `,trim(e.conversation_id),` + oracleDim("e.account_id") + `,` + oracleDim("e.api_key_id") +
		`,e.model,` + oracleDim("e.reasoning_effort") + `,` + oracleDim("e.useragent_group") + `,e.legacy_deleted,(` + inReports + `)`
	dims := append(append(append(rollupRows(t, s.readDB, `SELECT 'account',`+oracleDim("e.account_id")+`,'',count(*) FROM usage_events e GROUP BY 2`),
		rollupRows(t, s.readDB, `SELECT 'key',`+oracleDim("e.api_key_id")+`,'',count(*) FROM usage_events e GROUP BY 2`)...),
		rollupRows(t, s.readDB, `SELECT 'model',e.model,`+oracleDim("e.reasoning_effort")+`,count(*) FROM usage_events e GROUP BY 2,3`)...),
		rollupRows(t, s.readDB, `SELECT 'status',e.status,'',count(*) FROM usage_events e GROUP BY 2`)...)
	sort.Strings(dims)
	return map[string][]string{
		"total": withHists(hour, inReports, `,sum(`+conversation+`<>'')`),
		"cube": withHists(hour+`,`+oracleDim("e.account_id")+`,`+oracleDim("e.api_key_id")+`,e.model,`+oracleDim("e.useragent_group")+
			`,e.legacy_deleted`, inReports, ""),
		"slice": slices,
		"error": rollupRows(t, s.readDB, `SELECT `+hour+`,e.error_code,`+oracleDim("e.account_id")+`,`+oracleDim("e.api_key_id")+`,count(*)
 FROM usage_events e WHERE `+inReports+` AND e.status NOT IN ('success','completed','cancelled') AND e.error_code<>'' GROUP BY 1,2,3,4`),
		"conversation": rollupRows(t, s.readDB, `SELECT k0,k1,k2,k3,k4,k5,k6,k7,k8,count(*),sum(input_tokens),sum(output_tokens),
 sum(cached_input_tokens),sum(cost_microdollars),sum(total_latency_ms),min(requested_at),max(requested_at),
 max(CASE WHEN rank=1 THEN request_id END) FROM (SELECT `+keyAliases(conversationKeys)+`,e.*,row_number() OVER
 (PARTITION BY `+conversationKeys+` ORDER BY e.requested_at DESC,e.request_id DESC) AS rank
 FROM usage_events e WHERE `+rollupConversationSQL("e")+`) GROUP BY k0,k1,k2,k3,k4,k5,k6,k7,k8`),
		"log": rollupRows(t, s.readDB, `SELECT `+hour+`,`+oracleDim("e.account_id")+`,`+oracleDim("e.api_key_id")+`,e.model,`+
			oracleDim("e.reasoning_effort")+`,e.status,count(*) FROM usage_events e GROUP BY 1,2,3,4,5,6`),
		"dim": dims,
	}
}

// splitTopLevel splits SQL expressions at commas outside parentheses.
func splitTopLevel(expressions string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range expressions {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, expressions[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, expressions[start:])
}

// keyAliases names the expressions k0, k1, … at top level.
func keyAliases(expressions string) string {
	parts := splitTopLevel(expressions)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s AS k%d", parts[i], i)
	}
	return strings.Join(parts, ",")
}

// assertRollupsMatchRaw compares every rollup table with the oracle, and the
// state with the live folded events; the verifier must agree.
func assertRollupsMatchRaw(t testing.TB, s *Store) {
	t.Helper()
	got, want := rollupDump(t, s), rawRollupOracle(t, s)
	for _, table := range []string{"total", "cube", "slice", "error", "conversation", "log", "dim"} {
		if !reflect.DeepEqual(got[table], want[table]) {
			t.Fatalf("rollup table %s differs from raw events:\n%s", table, firstDifference(got[table], want[table]))
		}
	}
	var foldedRows, live, journal int64
	if err := s.readDB.QueryRow(`SELECT folded_rows,(SELECT count(*) FROM usage_events WHERE rowid<=folded_rowid),
 (SELECT count(*) FROM usage_rollup_journal) FROM usage_rollup_state`).Scan(&foldedRows, &live, &journal); err != nil {
		t.Fatal(err)
	}
	if foldedRows != live || journal != 0 {
		t.Fatalf("state counts %d folded rows of %d live, journal %d", foldedRows, live, journal)
	}
	var latest int64
	if err := s.readDB.QueryRow(`SELECT coalesce(max(requested_at),0) FROM usage_events`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{latest, millis(rollupFixtureBase.Add(30 * time.Hour))} {
		check, err := s.VerifyUsageRollups(context.Background(), fromMillis(at))
		if err != nil || !check.Checked || check.Mismatch != "" {
			t.Fatalf("verifier at %s: %+v %v", fromMillis(at), check, err)
		}
	}
}

func firstDifference(got, want []string) string {
	for i := 0; i < max(len(got), len(want)); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			return fmt.Sprintf("row %d of %d/%d:\n got %s\nwant %s", i, len(got), len(want), g, w)
		}
	}
	return "equal"
}

func TestUsageRollupFoldMatchesRawEventsForAnyBatchSize(t *testing.T) {
	var dumps []map[string][]string
	for _, batch := range []int{1, 7, 5000} {
		s, _ := testStore(t)
		setRollupWatermark(t, s, rollupFixtureBase.Add(36*time.Hour))
		insertRollupFixture(t, s, rand.New(rand.NewPCG(21, 22)), 0, 700, 4*24*time.Hour)
		fold := foldRollups(t, s, batch)
		if fold.Rows != 700 || fold.Journal != 0 {
			t.Fatalf("batch %d folded %+v", batch, fold)
		}
		assertRollupsMatchRaw(t, s)
		dumps = append(dumps, rollupDump(t, s))
	}
	if !reflect.DeepEqual(dumps[0], dumps[1]) || !reflect.DeepEqual(dumps[0], dumps[2]) {
		t.Fatal("rollups depend on the batch size")
	}
	if len(dumps[0]["total"]) < 80 || len(dumps[0]["conversation"]) < 50 || len(dumps[0]["error"]) == 0 {
		t.Fatalf("fixture does not exercise the rollups: %d totals %d conversations", len(dumps[0]["total"]), len(dumps[0]["conversation"]))
	}
}

// A failure at any statement of a fold job leaves state and tables as they
// were; the retry then equals a clean fold.
func TestUsageRollupFoldFailureRollsBackTheWholeBatch(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(23, 24))
	insertRollupFixture(t, s, random, 0, 300, 2*24*time.Hour)
	foldRollups(t, s, 100)
	insertRollupFixture(t, s, random, 300, 200, 2*24*time.Hour)
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE rowid%5=0 AND rowid<=200`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET cost_microdollars=cost_microdollars+7 WHERE rowid%3=0 AND rowid<=300`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET account_id=NULL,legacy_deleted=1 WHERE rowid%7=1 AND rowid<=300`); err != nil {
		t.Fatal(err)
	}
	before := rollupDump(t, s)
	state := func() string {
		return strings.Join(rollupRows(t, s.readDB, `SELECT folded_rowid,folded_rows,anchor_request_id,
 (SELECT count(*) FROM usage_rollup_journal) FROM usage_rollup_state`), "")
	}
	stateBefore := state()
	failed := errors.New("failpoint")
	for _, at := range []int{1, 2, 17, 150, 400} {
		calls := 0
		s.rollupFailpoint = func() error {
			if calls++; calls == at {
				return failed
			}
			return nil
		}
		if _, err := s.FoldUsageRollups(ctx, 1000, 1000); !errors.Is(err, failed) {
			t.Fatalf("failpoint at %d: %v after %d writes", at, err, calls)
		}
		if after := rollupDump(t, s); !reflect.DeepEqual(after, before) || state() != stateBefore {
			t.Fatalf("failure at write %d changed the rollups", at)
		}
	}
	s.rollupFailpoint = nil
	foldRollups(t, s, 1000)
	assertRollupsMatchRaw(t, s)
	// Folding again changes nothing.
	settled := rollupDump(t, s)
	if fold, err := s.FoldUsageRollups(ctx, 1000, 1000); err != nil || fold.Rows != 0 || fold.Journal != 0 || fold.Backlog ||
		!reflect.DeepEqual(rollupDump(t, s), settled) {
		t.Fatalf("idle fold %+v %v", fold, err)
	}
}

// Events inserted with an old requested_at fold into their hour; once they
// fall behind retention, their pruning journals them out again.
func TestUsageRollupFoldsLateEventsAndTheirRetention(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(25, 26))
	insertRollupFixture(t, s, random, 0, 300, 3*24*time.Hour)
	foldRollups(t, s, 64)
	// Reconciliation inserts late events into hours folded long ago.
	insertRollupFixture(t, s, random, 300, 40, 6*time.Hour)
	foldRollups(t, s, 64)
	assertRollupsMatchRaw(t, s)
	if n, err := s.FoldAndPruneRequestLogs(ctx, rollupFixtureBase.Add(12*time.Hour), 500); err != nil || n == 0 {
		t.Fatalf("retention %d %v", n, err)
	}
	if fold := foldRollups(t, s, 64); fold.Journal == 0 {
		t.Fatal("retention journaled nothing")
	}
	assertRollupsMatchRaw(t, s)
}

// Deleting the folded maximum lets the next insert take its rowid; the cursor
// moved below it, so the new event is folded.
func TestUsageRollupFoldsAReusedRowid(t *testing.T) {
	s, _ := testStore(t)
	insertRollupFixture(t, s, rand.New(rand.NewPCG(27, 28)), 0, 50, 24*time.Hour)
	foldRollups(t, s, 1000)
	var top int64
	if err := s.db.QueryRow(`SELECT max(rowid) FROM usage_events`).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE rowid=?`, top); err != nil {
		t.Fatal(err)
	}
	insertRollupFixture(t, s, rand.New(rand.NewPCG(29, 30)), 50, 1, 24*time.Hour)
	var reused int64
	if err := s.db.QueryRow(`SELECT rowid FROM usage_events WHERE request_id='req-000050'`).Scan(&reused); err != nil || reused != top {
		t.Fatalf("rowid %d not reused (%d): %v", reused, top, err)
	}
	if fold := foldRollups(t, s, 1000); fold.Rows != 1 || fold.Journal != 1 {
		t.Fatalf("fold after reuse %+v", fold)
	}
	assertRollupsMatchRaw(t, s)
}

// A conversation-hour that loses its first or last event gets both
// recomputed from the remaining folded events.
func TestUsageRollupRecomputesConversationBoundsAfterDeletion(t *testing.T) {
	s, _ := testStore(t)
	for i, at := range []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 40 * time.Minute} {
		if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,model,status,requested_at,conversation_id) VALUES(?,'m','success',?,'conv')`,
			fmt.Sprintf("c-%d", i), millis(rollupFixtureBase.Add(at))); err != nil {
			t.Fatal(err)
		}
	}
	foldRollups(t, s, 1000)
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE request_id IN ('c-0','c-3')`); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, s, 1000)
	var first, last int64
	var lastID string
	if err := s.readDB.QueryRow(`SELECT first_at,last_at,last_request_id FROM usage_rollup_conversation_hour`).Scan(&first, &last, &lastID); err != nil {
		t.Fatal(err)
	}
	if first != millis(rollupFixtureBase.Add(20*time.Minute)) || last != millis(rollupFixtureBase.Add(30*time.Minute)) || lastID != "c-2" {
		t.Fatalf("bounds %s..%s %s", fromMillis(first), fromMillis(last), lastID)
	}
	assertRollupsMatchRaw(t, s)
}

// rebuildRollups runs the service's response to an inconsistency.
func rebuildRollups(t testing.TB, s *Store) {
	t.Helper()
	if err := s.RebuildUsageRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, s, 1000)
}

// A changed import watermark, a wrong anchor and a negative count each fold
// nothing and report the rebuild reason; the rebuild then matches raw events.
func TestUsageRollupInconsistenciesRequireARebuild(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	insertRollupFixture(t, s, rand.New(rand.NewPCG(31, 32)), 0, 400, 3*24*time.Hour)
	foldRollups(t, s, 1000)
	for _, test := range []struct {
		reason  string
		corrupt func()
	}{
		{"watermark", func() { setRollupWatermark(t, s, rollupFixtureBase.Add(30*time.Hour)) }},
		{"anchor", func() {
			if _, err := s.db.Exec(`UPDATE usage_rollup_state SET anchor_request_id='other'`); err != nil {
				t.Fatal(err)
			}
		}},
		{"negative_measure", func() {
			if _, err := s.db.Exec(`DELETE FROM usage_rollup_total_hour WHERE bucket_epoch=(SELECT min(bucket_epoch) FROM usage_rollup_total_hour)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM usage_events WHERE rowid=(SELECT rowid FROM usage_events e WHERE ` +
				rollupInReportsSQL("e") + ` ORDER BY requested_at LIMIT 1)`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		test.corrupt()
		before := rollupDump(t, s)
		fold, err := s.FoldUsageRollups(ctx, 1000, 1000)
		if err != nil || fold.Rebuild != test.reason || fold.Rows != 0 || !reflect.DeepEqual(rollupDump(t, s), before) {
			t.Fatalf("%s: fold %+v %v", test.reason, fold, err)
		}
		if err := s.MarkUsageRollupsRebuilding(ctx); err != nil {
			t.Fatal(err)
		}
		if fold, err := s.FoldUsageRollups(ctx, 1000, 1000); err != nil || !fold.Rebuilding {
			t.Fatalf("%s: fold while rebuilding %+v %v", test.reason, fold, err)
		}
		if check, err := s.VerifyUsageRollups(ctx, rollupFixtureBase); err != nil || check.Checked {
			t.Fatalf("%s: verifier while rebuilding %+v %v", test.reason, check, err)
		}
		rebuildRollups(t, s)
		assertRollupsMatchRaw(t, s)
	}
}

// renumberRowids compacts usage_events rowids in order, as a VACUUM of a table
// without INTEGER PRIMARY KEY may, without firing the rollup triggers.
func renumberRowids(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the mapping is a TEMP table of one connection
	var triggers []string
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type='trigger' AND tbl_name='usage_events'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var statement string
		if err := rows.Scan(&statement); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, statement)
	}
	rows.Close()
	statements := []string{`DROP TRIGGER usage_rollup_events_delete`, `DROP TRIGGER usage_rollup_events_reprice`,
		`DROP TRIGGER usage_rollup_events_update`,
		`CREATE TEMP TABLE renumber AS SELECT rowid AS old,row_number() OVER (ORDER BY rowid) AS new FROM usage_events`,
		`UPDATE usage_events SET rowid=-(SELECT new FROM renumber WHERE old=usage_events.rowid)`,
		`UPDATE usage_events SET rowid=-rowid`}
	for _, statement := range append(statements, triggers...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// A restored VACUUM INTO backup whose rowids moved under the cursor fails the
// check at folder start: with unfolded events the count of folded rows
// differs, without them the cursor is above every rowid. The rebuild then
// matches raw events.
func TestUsageRollupDetectsRenumberedRowidsOfARestoredBackup(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		tail     int
		mismatch string
	}{{0, "folded_rowid"}, {100, "folded_rows"}} {
		s, _ := testStore(t)
		random := rand.New(rand.NewPCG(33, 34))
		insertRollupFixture(t, s, random, 0, 300, 2*24*time.Hour)
		if _, err := s.db.Exec(`DELETE FROM usage_events WHERE rowid%4=0`); err != nil {
			t.Fatal(err)
		}
		foldRollups(t, s, 1000)
		insertRollupFixture(t, s, random, 300, test.tail, 2*24*time.Hour)
		before := rollupRows(t, s.readDB, `SELECT rowid,request_id FROM usage_events`)
		copyPath := filepath.Join(t.TempDir(), "restored.sqlite")
		if _, err := s.db.Exec(`VACUUM INTO ?`, copyPath); err != nil {
			t.Fatal(err)
		}
		copied, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: copyPath, RawQuery: "mode=ro"}).String())
		if err != nil {
			t.Fatal(err)
		}
		// Design Q2: whether VACUUM INTO keeps rowids decides whether every
		// restore rebuilds; detection covers both answers.
		t.Logf("VACUUM INTO preserves usage_events rowids: %v",
			reflect.DeepEqual(rollupRows(t, copied, `SELECT rowid,request_id FROM usage_events`), before))
		_ = copied.Close()
		renumberRowids(t, copyPath)
		restored := openPeerStore(t, copyPath, Options{})
		check, err := restored.CheckUsageRollupCount(ctx)
		if err != nil || !check.Checked || check.Mismatch != test.mismatch {
			t.Fatalf("tail %d: count check after renumbering: %+v %v", test.tail, check, err)
		}
		if fold, err := restored.FoldUsageRollups(ctx, 1000, 1000); err != nil || test.tail == 0 && fold.Rebuild != "anchor" {
			t.Fatalf("tail %d: fold after renumbering %+v %v", test.tail, fold, err)
		}
		rebuildRollups(t, restored)
		assertRollupsMatchRaw(t, restored)
		if check, err := restored.CheckUsageRollupCount(ctx); err != nil || check.Mismatch != "" {
			t.Fatalf("tail %d: count check after rebuild: %+v %v", test.tail, check, err)
		}
	}
}
