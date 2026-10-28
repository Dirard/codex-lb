package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// openPeerStore opens a second store on path, as the other worker process of
// an installation does during an update.
func openPeerStore(t *testing.T, path string, options Options) *Store {
	t.Helper()
	store, err := OpenWithOptions(path, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Two workers write the same database file during an update. Their
// read-then-write jobs must wait for each other's write lock instead of
// failing with SQLITE_BUSY or SQLITE_BUSY_SNAPSHOT.
func TestTwoStoresOnOneFileCompleteConcurrentReadThenWriteJobs(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	saveTestAccount(t, first, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 1 << 40}}
	if err := first.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `CREATE TABLE contention_counter(id INTEGER PRIMARY KEY, value INTEGER NOT NULL);
INSERT INTO contention_counter VALUES(1,0);`); err != nil {
		t.Fatal(err)
	}
	const workers, iterations = 4, 75
	var wg sync.WaitGroup
	errs := make(chan error, 2*workers)
	for index, store := range []*Store{first, second} {
		for worker := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range iterations {
					if err := store.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
						var value int64
						if err := tx.QueryRowContext(ctx, `SELECT value FROM contention_counter WHERE id=1`).Scan(&value); err != nil {
							return err
						}
						_, err := tx.ExecContext(ctx, `UPDATE contention_counter SET value=? WHERE id=1`, value+1)
						return err
					}); err != nil {
						errs <- fmt.Errorf("store %d counter: %w", index, err)
						return
					}
					id := fmt.Sprintf("store%d-worker%d-%03d", index, worker, i)
					if err := reserveAndSettle(ctx, store, id, fixedTime.Add(time.Duration(i)*time.Millisecond)); err != nil {
						errs <- fmt.Errorf("store %d: %w", index, err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	const total = 2 * workers * iterations
	var counter int64
	if err := first.readDB.QueryRowContext(ctx, `SELECT value FROM contention_counter WHERE id=1`).Scan(&counter); err != nil || counter != total {
		t.Fatalf("counter = %d, want %d: %v", counter, total, err)
	}
	totals, err := second.UsageTotals(ctx, "key", "")
	if err != nil || totals.RequestCount != total {
		t.Fatalf("key totals %+v, want %d requests: %v", totals, total, err)
	}
	loaded, err := first.GetAPIKey(ctx, "key")
	if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != total*1100 {
		t.Fatalf("key limit %+v, want %d: %v", loaded.Limits, total*1100, err)
	}
}

// Writer and maintenance transactions take the database write lock at BEGIN;
// read-pool transactions do not.
func TestWriterAndMaintenanceTransactionsBeginImmediate(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	probe, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	writeLocked := func() bool {
		t.Helper()
		conn, err := probe.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			if !isBusy(err) {
				t.Fatal(err)
			}
			return true
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		return false
	}
	for _, pool := range []struct {
		name   string
		db     *sql.DB
		locked bool
	}{{"writer", store.db, true}, {"maintenance", store.maintenance, true}, {"reader", store.readDB, false}} {
		tx, err := pool.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := writeLocked(); got != pool.locked {
			t.Errorf("%s BEGIN holds the write lock = %v, want %v", pool.name, got, pool.locked)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

// Waiting for the other worker's write lock is not batch execution time: a job
// must run once that worker commits.
func TestWriteBatchDeadlineExcludesWaitingForAnotherStoreWriteLock(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	first.writer.timeout = 100 * time.Millisecond
	if _, err := first.db.ExecContext(ctx, "CREATE TABLE writer_wait_probe(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	holder, err := second.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	done := make(chan error, 1)
	go func() {
		done <- first.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO writer_wait_probe VALUES(1)")
			return err
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("job finished while another store held the write lock: %v", err)
	case <-time.After(3 * first.writer.timeout):
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("job failed after waiting for the other store: %v", err)
	}
}

func TestStoreInstanceIDIsGeneratedOrConfigured(t *testing.T) {
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	if pattern := regexp.MustCompile(`^[0-9a-f]{32}$`); !pattern.MatchString(first.InstanceID()) || !pattern.MatchString(second.InstanceID()) || first.InstanceID() == second.InstanceID() {
		t.Fatalf("generated instance ids %q and %q", first.InstanceID(), second.InstanceID())
	}
	if configured := openPeerStore(t, path, Options{InstanceID: "worker-b"}); configured.InstanceID() != "worker-b" {
		t.Fatalf("configured instance id = %q", configured.InstanceID())
	}
}

// Marking at a worker's start leaves the reservations of the other live
// worker alone; reservations of gone workers and of releases that recorded no
// owner are interrupted.
func TestMarkInterruptedReservationsSparesLiveOwners(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{InstanceID: "worker-b"})
	saveTestAccount(t, first, "acct")
	if err := first.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		store *Store
		id    string
	}{{first, "first"}, {second, "second"}} {
		if _, err := item.store.ReserveUsage(ctx, domain.ReservationRequest{ID: item.id, APIKeyID: "key",
			AccountID: "acct", Model: "gpt-test", Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	if err := second.ReserveWarmupUsage(ctx, "second-warmup", "acct", "gpt-test", 0, false, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 VALUES('unowned','key','acct','gpt-test','reserved',0,0)`); err != nil {
		t.Fatal(err)
	}
	state := func() map[string]string {
		t.Helper()
		rows, err := first.readDB.QueryContext(ctx, `SELECT id,owner_instance,needs_reconciliation FROM usage_reservations`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, owner string
			var flagged int
			if err := rows.Scan(&id, &owner, &flagged); err != nil {
				t.Fatal(err)
			}
			out[id] = fmt.Sprintf("%s/%d", owner, flagged)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	owned := state()
	if owned["first"] != first.InstanceID()+"/0" || owned["second"] != "worker-b/0" || owned["second-warmup"] != "worker-b/0" || owned["unowned"] != "/0" {
		t.Fatalf("reservation owners: %v", owned)
	}
	if count, err := first.MarkInterruptedReservations(ctx, []string{first.InstanceID(), ""}); err != nil || count != 3 {
		t.Fatalf("scoped mark = %d, %v", count, err)
	}
	marked := state()
	if marked["first"] != first.InstanceID()+"/0" || marked["second"] != "worker-b/1" || marked["second-warmup"] != "worker-b/1" || marked["unowned"] != "/1" {
		t.Fatalf("scoped mark flagged: %v", marked)
	}
	if count, err := first.MarkInterruptedReservations(ctx, nil); err != nil || count != 1 {
		t.Fatalf("unscoped mark = %d, %v", count, err)
	}
	if all := state(); all["first"] != first.InstanceID()+"/1" {
		t.Fatalf("unscoped mark spared a reservation: %v", all)
	}
}

// Both workers may resume the same interrupted reprice during an overlap;
// event costs, aggregates and current cost limits change exactly once.
func TestConcurrentRepriceResumptionAppliesEachDeltaOnce(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	saveTestAccount(t, first, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1 << 40}}
	if err := first.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	const events = 2*repriceRawChunk + 7
	usage := domain.UsageAmount{InputTokens: 1000, OutputTokens: 100}
	for i := range events {
		id := fmt.Sprintf("cost-%05d", i)
		at := fixedTime.Add(time.Duration(i) * time.Millisecond)
		if _, err := first.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
			Model: "gpt-6-sol", Budget: domain.UsageAmount{CostMicrodollars: 1}, Now: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := first.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
			RequestID: "event-" + id, APIKeyID: "key", AccountID: "acct", Model: "gpt-6-sol", RequestKind: "normal",
			Status: "success", RequestedAt: at, Usage: usage}}); err != nil {
			t.Fatal(err)
		}
	}
	price := pricing.Price{Standard: pricing.Rates{Input: 9000000, Cached: 900000, Output: 27000000}}
	simulateInterruptedReprice(t, first, price)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func() {
			<-start
			results <- store.ResumeModelReprices(ctx)
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertLedgerMatchesEvents(t, first, price)
	expected, err := price.Cost(usage, "")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := second.GetAPIKey(ctx, "key")
	if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != events*expected {
		t.Fatalf("cost limit %+v, want %d: %v", loaded.Limits, events*expected, err)
	}
}
