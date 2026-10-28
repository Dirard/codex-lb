package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// pendingLogBase starts the events and pending entries of the equivalence
// fixture, which interleave over two days.
var pendingLogBase = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

// seedPendingLogEquivalence fills a store with random events of every
// dimension edge case and pending entries of every projection case: accounts
// that are empty, NULL, deleted, reimported or \x1f-prefixed, the warmup key,
// key names with LIKE wildcards, shared timestamps among pending entries and
// with events, ordinary live and settled reservations that must stay hidden,
// and pending reservations that already have a usage event.
func seedPendingLogEquivalence(t *testing.T, s *Store, random *rand.Rand) {
	t.Helper()
	ctx := context.Background()
	if err := seedPendingLogAccounts(ctx, s, 3); err != nil {
		t.Fatal(err)
	}
	for _, key := range []domain.APIKey{testKey("\x1fk", nil), testKey("key-wild", nil)} {
		if key.ID == "key-wild" {
			key.Name = "50%_off"
		}
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	span := 48 * time.Hour
	insertRollupFixtureAt(t, s, random, 0, 3000, pendingLogBase, span)
	var eventTimes []int64
	rows, err := s.db.Query(`SELECT requested_at FROM usage_events ORDER BY rowid LIMIT 200`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		eventTimes = append(eventTimes, at)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	accounts := []any{"", nil, "acct-0", "acct-3", "acct-5", "acct-7", "acct-8", "\x1fodd"}
	keys := []string{"key-0", "key-1", "key-2", "key-wild", "\x1fk", domain.WarmupKeyID}
	models := []string{"model-0", "model-1", "model-2", "legacy-model", "pend%model"}
	statuses := []string{"pending", "pending", "pending", "pending", "live", "finalized", "released", "failed"}
	tied := millis(pendingLogBase.Add(30 * time.Hour))
	for i := range 2600 {
		at := millis(pendingLogBase) + random.Int64N(span.Milliseconds())
		switch i % 13 {
		case 0:
			at = eventTimes[random.IntN(len(eventTimes))] // shared with an event
		case 1:
			at = tied // shared among pending entries, ordered by id
		}
		id := fmt.Sprintf("pend-%05d", i)
		if i%17 == 0 {
			id = fmt.Sprintf("pend_%%%04d", i) // LIKE wildcards in the id
		}
		status, flagged := statuses[random.IntN(len(statuses))], 0
		if status == "pending" {
			status, flagged = "reserved", 1
		} else if status == "live" {
			status = "reserved"
		}
		if _, err := s.db.Exec(`INSERT INTO usage_reservations(id,api_key_id,account_id,account_generation,model,status,
 created_at,updated_at,needs_reconciliation) VALUES(?,?,?,?,?,?,?,?,?)`, id, keys[random.IntN(len(keys))],
			accounts[random.IntN(len(accounts))], random.IntN(2), models[random.IntN(len(models))], status, at, at, flagged); err != nil {
			t.Fatal(err)
		}
	}
	recordPendingLogEvents(t, s, 60)
}

// recordPendingLogEvents records a usage event for n more pending
// reservations spread over time, without settling them, which hides their
// pending entries.
func recordPendingLogEvents(t *testing.T, s *Store, n int) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,reservation_id,api_key_id,account_id,model,status,
 requested_at,input_tokens,output_tokens,cost_microdollars)
 SELECT 'recorded-'||r.id,r.id,nullif(r.api_key_id,?),nullif(r.account_id,''),r.model,'success',r.created_at,3,2,100
 FROM usage_reservations r WHERE r.status='reserved' AND r.needs_reconciliation=1
 AND NOT EXISTS (SELECT 1 FROM usage_events x WHERE x.reservation_id=r.id) ORDER BY (r.rowid*2654435761)%4294967296 LIMIT ?`,
		domain.WarmupKeyID, n); err != nil {
		t.Fatal(err)
	}
}

// pendingLogFilters is the filter matrix of the equivalence test: every
// condition alone and combined, timeframes with random edges.
func pendingLogFilters(random *rand.Rand) []domain.RequestLogFilter {
	at := func() *time.Time {
		value := pendingLogBase.Add(time.Duration(random.Int64N(int64(48 * time.Hour))))
		return &value
	}
	since, until := at(), at()
	if until.Before(*since) {
		since, until = until, since
	}
	filters := []domain.RequestLogFilter{
		{},
		{Since: since},
		{Until: until},
		{Since: since, Until: until},
		{AccountIDs: []string{"acct-0"}},
		{AccountIDs: []string{"", "acct-7"}},
		{AccountIDs: []string{"acct-8", "\x1fodd"}},
		{APIKeyIDs: []string{"key-0"}},
		{APIKeyIDs: []string{domain.WarmupKeyID, "key-wild"}},
		{APIKeyIDs: []string{"\x1fk", "key-2"}},
		{Statuses: []string{pendingLogStatus}},
		{Statuses: []string{"success"}},
		{Statuses: []string{"error", pendingLogStatus}},
		{Models: []string{"model-1"}},
		{Models: []string{"legacy-model", "pend%model"}},
		{ReasoningEfforts: []string{"low"}},
		{ReasoningEfforts: []string{""}},
		{ModelOptions: []string{"model-0"}},
		{ModelOptions: []string{"model-0:::high"}},
		{ModelOptions: []string{"model-1:::", ":::x"}},
		{ModelOptions: []string{":::x"}},
		{Search: "pend-0001"},
		{Search: "pend_%"},
		{Search: "alpha"},
		{Search: "50%"},
		{Search: "_off"},
		{Search: "LEGACY"},
		{Search: "reconciliation"},
		{Search: "unmatched"},
		{ConversationID: "conv-a"},
		{ConversationID: "not-recorded"},
		{AccountIDs: []string{"acct-0", ""}, Search: "gamma", Since: since},
		{APIKeyIDs: []string{"key-0", "key-1"}, Models: []string{"model-0", "legacy-model"}, Statuses: []string{pendingLogStatus, "success"}, Until: until},
		{AccountIDs: []string{"acct-3"}, ModelOptions: []string{"model-2", "model-1:::low"}, Search: "pend"},
	}
	return filters
}

// assertPendingLogReadsMatch compares every request-log read of a filter
// with the reference: options and totals through the rollup and raw paths
// in one snapshot, the store methods, and pages by offset and keyset that
// cross between pending entries and events.
func assertPendingLogReadsMatch(t *testing.T, s *Store, state string, filter domain.RequestLogFilter) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	options := filter
	options.Statuses = nil
	check := func(name string, got, want any, gotErr, wantErr error) {
		t.Helper()
		if gotErr != nil || wantErr != nil {
			t.Fatalf("%s %s %+v: errors %v, reference %v", state, name, filter, gotErr, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s %s %+v differs:\n got %+v\nwant %+v", state, name, filter, got, want)
		}
	}
	got, gotErr := requestLogOptionsRaw(ctx, tx, options)
	want, wantErr := legacyRequestLogOptionsRaw(ctx, tx, options)
	check("raw options", got, want, gotErr, wantErr)
	gotTotal, gotErr := requestLogTotalRaw(ctx, tx, newPendingLogReader(tx), filter)
	wantTotal, wantErr := legacyRequestLogTotalRaw(ctx, tx, filter)
	check("raw total", gotTotal, wantTotal, gotErr, wantErr)
	if reader, err := openRollupRead(ctx, tx); err != nil {
		t.Fatal(err)
	} else if reader != nil && requestLogCubeEligible(filter) {
		got, gotErr := reader.requestLogOptions(ctx, options)
		want, wantErr := reader.legacyRequestLogOptions(ctx, options)
		check("rollup options", got, want, gotErr, wantErr)
		gotTotal, gotErr := reader.requestLogTotal(ctx, newPendingLogReader(tx), filter)
		wantTotal, wantErr := reader.legacyRequestLogTotal(ctx, filter)
		check("rollup total", gotTotal, wantTotal, gotErr, wantErr)
	}
	got, gotErr = s.RequestLogOptions(ctx, filter)
	want, wantErr = s.legacyRequestLogOptions(ctx, filter)
	check("options", got, want, gotErr, wantErr)
	page := func(name string, page domain.RequestLogFilter) domain.RequestLogsResponse {
		t.Helper()
		got, gotErr := s.ListRequestLogs(ctx, page)
		want, wantErr := s.legacyListRequestLogs(ctx, page)
		check(fmt.Sprintf("%s limit %d offset %d", name, page.Limit, page.Offset), got, want, gotErr, wantErr)
		return got
	}
	for _, size := range []int{1, 25} {
		for _, offset := range []int{0, 40} {
			paged := filter
			paged.Limit, paged.Offset = size, offset
			page("offset page", paged)
		}
	}
	// A keyset walk and an offset walk over the whole result.
	walk := filter
	walk.Limit = 997
	for pages := 0; ; pages++ {
		response := page("keyset page", walk)
		if response.NextCursor == nil {
			if pages > 0 && response.HasMore {
				t.Fatalf("%s keyset walk ended with more rows", state)
			}
			break
		}
		cursor, err := domain.ParseRequestLogCursor(*response.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		walk.Before = &cursor
		short := walk
		short.Limit = 3
		page("keyset short page", short)
	}
	walk = filter
	walk.Limit = 997
	for ; ; walk.Offset += walk.Limit {
		if response := page("offset walk", walk); !response.HasMore {
			break
		}
	}
}

// The optimized pending entries match the reference reads for every filter
// with no, few (excluded by id) and many (probed) recorded reservations, with
// an unfolded tail of events and with folded rollups.
func TestRequestLogPendingEntriesMatchReference(t *testing.T) {
	random := rand.New(rand.NewPCG(43, 7))
	s, _ := testStore(t)
	seedPendingLogEquivalence(t, s, random)
	var pending, recorded int
	if err := s.db.QueryRow(`SELECT count(*),count(x.rowid) FROM usage_reservations r LEFT JOIN usage_events x
 ON x.reservation_id=r.id WHERE r.status='reserved' AND r.needs_reconciliation=1`).Scan(&pending, &recorded); err != nil {
		t.Fatal(err)
	}
	if pending < 1000 || recorded != 60 {
		t.Fatalf("fixture has %d pending and %d recorded reservations", pending, recorded)
	}
	filters := pendingLogFilters(random)
	for _, state := range []struct {
		name     string
		prepare  func()
		recorded int
	}{
		{"folded", func() { foldRollups(t, s, 700) }, 60},
		{"many recorded", func() { recordPendingLogEvents(t, s, pendingLogRecordedLimit) }, 60 + pendingLogRecordedLimit},
	} {
		state.prepare()
		reader := newPendingLogReader(s.analyticsDB)
		if err := reader.load(context.Background()); err != nil {
			t.Fatal(err)
		}
		if reader.probe != (state.recorded > pendingLogRecordedLimit) || !reader.probe && len(reader.recorded) != state.recorded {
			t.Fatalf("%s: recorded probe %v with %d ids", state.name, reader.probe, len(reader.recorded))
		}
		for _, filter := range filters {
			assertPendingLogReadsMatch(t, s, state.name, filter)
		}
	}
	// Without recorded reservations no query probes usage_events.
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE request_id LIKE 'recorded-%'`); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, s, 700)
	for _, filter := range filters[:12] {
		assertPendingLogReadsMatch(t, s, "none recorded", filter)
	}
}

// The pending reads use the covering pending index and never read
// reservation rows, except the few rows a page shows.
func TestRequestLogPendingQueriesReadTheIndexAlone(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	explain := func(query string, args ...any) string {
		t.Helper()
		rows, err := s.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
		defer rows.Close()
		plan := ""
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		return plan
	}
	since := fixedTime
	recorded := &pendingLogReader{q: s.readDB, loaded: true, recorded: []any{"recorded-a", "recorded-b"}}
	for _, filter := range []domain.RequestLogFilter{{}, {Since: &since}, {AccountIDs: []string{"acct"}, Search: "x"}, {APIKeyIDs: []string{"key"}}} {
		plan := planPendingLog(filter)
		query, args := plan.countSQL(pendingLogRows, nil)
		for _, step := range strings.Split(explain(query, args...), "\n") {
			if strings.Contains(step, " r ") && !strings.Contains(step, "COVERING INDEX idx_usage_reservations_pending") {
				t.Fatalf("count of %+v reads reservation rows: %s", filter, step)
			}
		}
		query, args = plan.countSQL(recorded.recordedSource(), recorded.recorded)
		if plan := explain(query, args...); !strings.Contains(plan, "SEARCH r USING ") || !strings.Contains(plan, "id=?)") || strings.Contains(plan, "SCAN r") {
			t.Fatalf("recorded count of %+v scans:\n%s", filter, plan)
		}
		for _, reader := range []*pendingLogReader{newPendingLogReader(s.readDB), recorded} {
			cte, args, err := reader.pageCTE(ctx, filter, 26)
			if err != nil {
				t.Fatal(err)
			}
			pagePlan := explain("WITH "+strings.TrimSuffix(cte, ", ")+" SELECT * FROM pending_page", args...)
			if !strings.Contains(pagePlan, "COVERING INDEX idx_usage_reservations_pending") || strings.Contains(pagePlan, "SCAN r\n") {
				t.Fatalf("pending page of %+v:\n%s", filter, pagePlan)
			}
		}
	}
	if plan := explain(`SELECT r.id FROM usage_reservations r INDEXED BY idx_usage_reservations_open
 CROSS JOIN usage_events x ON x.reservation_id=r.id WHERE r.status='reserved' AND r.needs_reconciliation=1`); !strings.Contains(plan,
		"COVERING INDEX idx_usage_reservations_open") || !strings.Contains(plan, "COVERING INDEX sqlite_autoindex_usage_events") {
		t.Fatalf("recorded reservations plan:\n%s", plan)
	}
}

// Migration 43 builds the pending index while a schema 42 worker keeps
// settling and flagging reservations through its own connection; the index
// then holds exactly the reservations flagged for reconciliation.
func TestPendingLogIndexMigrationIsAdditiveBesideAnOlderWriter(t *testing.T) {
	ctx := context.Background()
	reservations := 400000
	if raceDetector {
		reservations = 20000
	}
	path := filepath.Join(t.TempDir(), "v42.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:42] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertPendingLogReservations(ctx, db, reservations, 28, fixedTime.Add(-30*24*time.Hour), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=42",
		"INSERT INTO schema_compatibility(id,reader_floor) VALUES(1,40)", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	peer, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate&_busy_timeout=10000"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var written atomic.Int64
	stop := make(chan struct{})
	var peerErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Schema 42 settles a flagged reservation, opens one and flags it.
			id := fmt.Sprintf("peer-%d", i)
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{`UPDATE usage_reservations SET status='finalized',needs_reconciliation=0,updated_at=? WHERE id=(SELECT id
 FROM usage_reservations WHERE status='reserved' AND needs_reconciliation=1 ORDER BY id LIMIT 1)`, []any{millis(fixedTime)}},
				{`INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 VALUES(?,'key-0','acct-0','model-0','reserved',?,?)`, []any{id, millis(fixedTime), millis(fixedTime)}},
				{`UPDATE usage_reservations SET needs_reconciliation=1 WHERE id=? AND status='reserved'`, []any{id}},
			} {
				if _, err := peer.ExecContext(ctx, statement.query, statement.args...); err != nil {
					peerErr = err
					return
				}
			}
			written.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()
	for written.Load() < 20 {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	store := openPeerStore(t, path, Options{})
	opened := time.Since(started)
	for before := written.Load(); written.Load() < before+20 && peerErr == nil; {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if peerErr != nil {
		t.Fatalf("schema 42 writer failed beside the migration: %v", peerErr)
	}
	t.Logf("migration 42->43 with %d reservations opened in %s", reservations, opened)
	if opened > 3*time.Second {
		t.Fatalf("migration of %d reservations took %s", reservations, opened)
	}
	if state := readSchemaFingerprint(t, path); state.version != 43 || state.floor != 40 {
		t.Fatalf("migrated database records %+v", state)
	}
	var indexed, flagged int
	var check string
	if err := store.readDB.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM usage_reservations INDEXED BY idx_usage_reservations_pending WHERE status='reserved' AND needs_reconciliation=1),
 (SELECT count(*) FROM usage_reservations NOT INDEXED WHERE status='reserved' AND needs_reconciliation=1)`).Scan(&indexed, &flagged); err != nil {
		t.Fatal(err)
	}
	if err := store.readDB.QueryRowContext(ctx, `PRAGMA integrity_check(usage_reservations)`).Scan(&check); err != nil {
		t.Fatal(err)
	}
	if indexed != flagged || flagged < reservations*pendingLogShare/10000/2 || check != "ok" {
		t.Fatalf("pending index holds %d of %d flagged reservations; integrity %s", indexed, flagged, check)
	}
}
