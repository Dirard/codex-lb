package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// syntheticUsageEvents is the number of events of the production copy the
// design was measured on; the race detector gets a smaller fixture.
func syntheticUsageEvents() int {
	if raceDetector {
		return 20000
	}
	return 742000
}

// insertSyntheticUsageEvents adds n events from first on, about 3.5 s apart
// with jitter, shaped like production: keys pinned mostly to one account,
// model and user agent, about 60 concurrent conversations, warmups, errors,
// NULL dimensions and negative queue latencies.
func insertSyntheticUsageEvents(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, first, n int, start time.Time) error {
	_, err := db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT ? UNION ALL SELECT i+1 FROM n WHERE i<?),
 g AS (SELECT i,(i*2654435761)%4294967296 AS h FROM n)
INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,error_code,request_kind,requested_at,
 conversation_id,useragent_group,reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,
 reasoning_tokens_known,cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
SELECT printf('synthetic-%07d',i),'key-'||(h%28),
 CASE WHEN h%200=7 THEN NULL WHEN h%10=0 THEN 'acct-'||((h/10)%9) ELSE 'acct-'||((h%28)%9) END,
 CASE WHEN (h/7)%5=0 THEN 'model-'||((h/35)%5) ELSE 'model-'||((h%28)%5) END,
 CASE h%50 WHEN 0 THEN 'error' WHEN 1 THEN 'cancelled' WHEN 2 THEN 'quota' ELSE 'success' END,
 CASE h%50 WHEN 0 THEN 'upstream_error' WHEN 2 THEN 'rate_limited' ELSE '' END,
 CASE WHEN h%300=0 THEN 'warmup' ELSE 'normal' END,
 ?+i*3500+h%3000,
 CASE WHEN h%13=0 THEN NULL ELSE 'conv-'||((i/1500)*60+h%60) END,
 CASE WHEN h%97=0 THEN NULL ELSE 'ua-'||((h%28)%3) END,
 CASE (h/3)%4 WHEN 0 THEN NULL WHEN 1 THEN 'low' WHEN 2 THEN 'medium' ELSE 'high' END,
 500+h%20000,50+(h/11)%2000,(h/13)%15000,(h/17)%500,(h/19)%2,100+(h/23)%200000,
 (h/29)%80-2,500+(h/31)%60000,CASE WHEN (h/37)%6=0 THEN NULL ELSE 50+(h/41)%4000 END FROM g`,
		first, first+n-1, millis(start))
	return err
}

// v41Database creates a closed schema 41 database holding n synthetic events.
func v41Database(t testing.TB, n int) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v41.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:41] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertSyntheticUsageEvents(ctx, db, 0, n, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=41",
		"INSERT INTO schema_compatibility(id,reader_floor) VALUES(1,40)", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Migration 42 is DDL plus one state row: on a database of production size
// it takes milliseconds, leaves every event unfolded and keeps the floor, while
// a schema 41 worker keeps inserting through its own connection.
func TestUsageRollupMigrationIsAdditiveAndFastBesideAnOlderWriter(t *testing.T) {
	ctx := context.Background()
	events := syntheticUsageEvents()
	path := v41Database(t, events)
	peer, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate&_busy_timeout=10000"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var inserted atomic.Int64
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
			// The statement schema 41 settlement runs, in its own transaction.
			if _, err := peer.ExecContext(ctx, `INSERT INTO usage_events(request_id,model,status,requested_at)
 VALUES(?,'peer','success',?) ON CONFLICT(request_id) DO NOTHING`, fmt.Sprintf("peer-%d", i), millis(fixedTime)); err != nil {
				peerErr = err
				return
			}
			inserted.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()
	for inserted.Load() < 20 {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	store := openPeerStore(t, path, Options{})
	opened := time.Since(started)
	for before := inserted.Load(); inserted.Load() < before+20 && peerErr == nil; {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if peerErr != nil {
		t.Fatalf("schema 41 writer failed beside the migration: %v", peerErr)
	}
	if opened > time.Second {
		t.Fatalf("migration of %d events took %s", events, opened)
	}
	t.Logf("migration 41->42 with %d events opened in %s", events, opened)
	if state := readSchemaFingerprint(t, path); state.version != 42 || state.floor != 40 {
		t.Fatalf("migrated database records %+v", state)
	}
	var layout, folded, foldedRows, journal, rollups, total int64
	var status string
	if err := store.readDB.QueryRowContext(ctx, `SELECT layout,status,folded_rowid,folded_rows,
 (SELECT count(*) FROM usage_rollup_journal),
 (SELECT count(*) FROM usage_rollup_total_hour)+(SELECT count(*) FROM usage_rollup_cube_hour)+
 (SELECT count(*) FROM usage_rollup_slice)+(SELECT count(*) FROM usage_rollup_error_hour)+
 (SELECT count(*) FROM usage_rollup_conversation_hour)+(SELECT count(*) FROM usage_rollup_log_hour)+
 (SELECT count(*) FROM usage_rollup_dim),(SELECT count(*) FROM usage_events) FROM usage_rollup_state`).
		Scan(&layout, &status, &folded, &foldedRows, &journal, &rollups, &total); err != nil {
		t.Fatal(err)
	}
	if layout != 1 || status != "ready" || folded != 0 || foldedRows != 0 || journal != 0 || rollups != 0 ||
		total != int64(events)+inserted.Load() {
		t.Fatalf("state %d %s %d/%d journal %d rollups %d events %d (peer %d)", layout, status, folded, foldedRows,
			journal, rollups, total, inserted.Load())
	}
}

// rollupTestImageColumns are the event image columns journal rows carry.
const rollupTestImageColumns = `request_id,requested_at,api_key_id,account_id,model,request_kind,status,error_code,
 legacy_request_id,legacy_deleted,conversation_id,useragent_group,reasoning_effort,input_tokens,output_tokens,
 cached_input_tokens,reasoning_tokens,reasoning_tokens_known,cost_microdollars,queue_latency_ms,
 total_latency_ms,latency_first_token_ms`

// rollupTestImage is one event image; cost is kept apart so cost-only journal
// rows can be applied.
type rollupTestImage struct {
	dims string
	cost int64
}

func scanRollupTestImages(t *testing.T, rows *sql.Rows, withSign bool) (map[int64]rollupTestImage, []struct {
	sign, rowid int64
	image       rollupTestImage
}) {
	t.Helper()
	defer rows.Close()
	images := map[int64]rollupTestImage{}
	var journal []struct {
		sign, rowid int64
		image       rollupTestImage
	}
	for rows.Next() {
		var rowid, sign, requested, deleted, input, output, cached, reasoning, known, cost, queue, total int64
		var request, model, kind, status, code string
		var key, account, legacy, conversation, agent, effort sql.NullString
		var ttft sql.NullInt64
		dest := []any{&rowid, &request, &requested, &key, &account, &model, &kind, &status, &code, &legacy, &deleted,
			&conversation, &agent, &effort, &input, &output, &cached, &reasoning, &known, &cost, &queue, &total, &ttft}
		if withSign {
			dest = append([]any{&sign}, dest...)
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		image := rollupTestImage{fmt.Sprint(request, requested, key, account, model, kind, status, code, legacy, deleted,
			conversation, agent, effort, input, output, cached, reasoning, known, queue, total, ttft), cost}
		if withSign {
			journal = append(journal, struct {
				sign, rowid int64
				image       rollupTestImage
			}{sign, rowid, image})
		} else {
			images[rowid] = image
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return images, journal
}

// foldedRollupImages returns the images of events with rowid<=cursor.
func foldedRollupImages(t *testing.T, s *Store, cursor int64) map[int64]rollupTestImage {
	t.Helper()
	rows, err := s.readDB.Query(`SELECT rowid,`+rollupTestImageColumns+` FROM usage_events WHERE rowid<=?`, cursor)
	if err != nil {
		t.Fatal(err)
	}
	images, _ := scanRollupTestImages(t, rows, false)
	return images
}

// assertJournalReplays checks that applying the journal to the images folded
// before equals the events of those rowids now, and that the state counts the
// live folded rows.
func assertJournalReplays(t *testing.T, s *Store, before map[int64]rollupTestImage, cursor int64) {
	t.Helper()
	rows, err := s.readDB.Query(`SELECT sign,event_rowid,` + rollupTestImageColumns + ` FROM usage_rollup_journal ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	_, journal := scanRollupTestImages(t, rows, true)
	replayed := map[int64]rollupTestImage{}
	for rowid, image := range before {
		replayed[rowid] = image
	}
	for _, entry := range journal {
		current, ok := replayed[entry.rowid]
		switch entry.sign {
		case -1:
			if !ok || current != entry.image {
				t.Fatalf("journal removes %d %+v, folded %+v", entry.rowid, entry.image, current)
			}
			delete(replayed, entry.rowid)
		case 1:
			if ok {
				t.Fatalf("journal adds %d twice", entry.rowid)
			}
			replayed[entry.rowid] = entry.image
		case 0:
			if !ok || current.dims != entry.image.dims {
				t.Fatalf("journal reprices %d %+v, folded %+v", entry.rowid, entry.image, current)
			}
			current.cost += entry.image.cost
			replayed[entry.rowid] = current
		}
	}
	after := foldedRollupImages(t, s, cursor)
	if len(after) != len(replayed) {
		t.Fatalf("journal replays %d folded events, %d remain", len(replayed), len(after))
	}
	for rowid, image := range after {
		if replayed[rowid] != image {
			t.Fatalf("event %d is %+v, journal replays %+v", rowid, image, replayed[rowid])
		}
	}
	var foldedRows, live int64
	if err := s.readDB.QueryRow(`SELECT folded_rows,(SELECT count(*) FROM usage_events WHERE rowid<=folded_rowid)
 FROM usage_rollup_state`).Scan(&foldedRows, &live); err != nil || foldedRows != live {
		t.Fatalf("state counts %d folded rows, %d live: %v", foldedRows, live, err)
	}
}

// markRollupsFolded moves the cursor over every event as a folder would,
// without writing rollups: only the triggers are under test.
func markRollupsFolded(t *testing.T, s *Store) int64 {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE usage_rollup_state SET folded_rowid=(SELECT coalesce(max(rowid),0) FROM usage_events),
 folded_rows=(SELECT count(*) FROM usage_events),
 anchor_request_id=(SELECT request_id FROM usage_events ORDER BY rowid DESC LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := s.db.QueryRow(`SELECT folded_rowid FROM usage_rollup_state`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	return cursor
}

// A second store that never folds stands in for a schema 41 worker: its
// settlement inserts journal nothing, while its repricing, retention and
// account history deletion journal exactly the changes of folded events.
func TestUsageRollupTriggersJournalChangesOfAStoreThatNeverFolds(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	old := openPeerStore(t, path, Options{})
	seedPricedHistory(t, old, 12)
	saveTestAccount(t, old, "gone")
	for i, account := range []string{"gone", "gone", "acct"} {
		id := fmt.Sprintf("gone-%d", i)
		if _, err := old.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: account,
			Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 10}, Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
		if _, err := old.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
			RequestID: "event-" + id, APIKeyID: "key", AccountID: account, Model: "gpt-6-sol", RequestKind: "normal",
			Status: "success", RequestedAt: fixedTime.Add(time.Duration(i) * time.Hour), ConversationID: " conv ",
			Usage: domain.UsageAmount{InputTokens: 100, OutputTokens: 10}}}); err != nil {
			t.Fatal(err)
		}
	}
	cursor := markRollupsFolded(t, first)
	before := foldedRollupImages(t, first, cursor)

	// Inserts after the cursor fire nothing.
	if err := reserveAndSettle(ctx, old, "unfolded", fixedTime.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertJournalReplays(t, first, before, cursor)
	if _, err := old.ApplyModelPriceChange(ctx, "gpt-6-sol", &pricing.Price{Standard: pricing.Rates{Input: 3, Output: 7}}, fixedTime); err != nil {
		t.Fatal(err)
	}
	var reprices int
	if err := first.readDB.QueryRow(`SELECT count(*) FROM usage_rollup_journal WHERE sign=0`).Scan(&reprices); err != nil || reprices != len(before) {
		t.Fatalf("%d cost deltas journaled for %d folded events: %v", reprices, len(before), err)
	}
	assertJournalReplays(t, first, before, cursor)
	if err := old.DeleteAccount(ctx, "gone", false); err != nil {
		t.Fatal(err)
	}
	assertJournalReplays(t, first, before, cursor)
	if n, err := old.FoldAndPruneRequestLogs(ctx, fixedTime.Add(5*time.Millisecond), 3); err != nil || n != 3 {
		t.Fatalf("retention %d %v", n, err)
	}
	assertJournalReplays(t, first, before, cursor)
	var anonymized int
	if err := first.readDB.QueryRow(`SELECT count(*) FROM usage_rollup_journal WHERE sign=1 AND account_id IS NULL AND legacy_deleted=1`).
		Scan(&anonymized); err != nil || anonymized != 2 {
		t.Fatalf("anonymized images %d: %v", anonymized, err)
	}
}

// Deleting the highest rowid, folded or not, lowers the cursor to the
// remaining maximum, so the next insert cannot reuse a folded rowid.
func TestUsageRollupDeleteTriggerKeepsInsertsAboveTheCursor(t *testing.T) {
	s, _ := testStore(t)
	insert := func(id string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,model,status,requested_at) VALUES(?,'m','success',0)`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		insert(id)
	}
	cursor := markRollupsFolded(t, s)
	insert("unfolded")
	for _, statement := range []string{`DELETE FROM usage_events WHERE request_id='c'`, `DELETE FROM usage_events WHERE request_id='unfolded'`} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var folded, rows int64
	var anchor string
	if err := s.db.QueryRow(`SELECT folded_rowid,folded_rows,anchor_request_id FROM usage_rollup_state`).Scan(&folded, &rows, &anchor); err != nil {
		t.Fatal(err)
	}
	if folded != cursor-1 || rows != 2 || anchor != "b" {
		t.Fatalf("cursor %d rows %d anchor %q after deleting the folded and the unfolded maximum (was %d)", folded, rows, anchor, cursor)
	}
	insert("next")
	var rowid int64
	if err := s.db.QueryRow(`SELECT rowid FROM usage_events WHERE request_id='next'`).Scan(&rowid); err != nil || rowid <= folded {
		t.Fatalf("new event rowid %d at or below cursor %d: %v", rowid, folded, err)
	}
	var journal []string
	rowsJ, err := s.db.Query(`SELECT request_id FROM usage_rollup_journal ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rowsJ.Close()
	for rowsJ.Next() {
		var id string
		if err := rowsJ.Scan(&id); err != nil {
			t.Fatal(err)
		}
		journal = append(journal, id)
	}
	if strings.Join(journal, ",") != "c" {
		t.Fatalf("journal %v, want only the folded deletion", journal)
	}
}
