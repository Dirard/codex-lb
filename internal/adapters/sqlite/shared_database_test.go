package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
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

// One worker committing short jobs back to back (a retention cycle, a reprice
// resumption) leaves the write lock free only between its commits. SQLite's
// busy handler sleeps up to 100 ms between attempts and rarely sees that gap,
// so the other worker's writes, settlements included, must keep retrying
// instead of failing after one busy timeout.
func TestBackToBackBatchesOfOneStoreDoNotStarveTheOther(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	if _, err := first.db.ExecContext(ctx, `CREATE TABLE bulk(id INTEGER PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE peer_writes(id INTEGER PRIMARY KEY, value INTEGER NOT NULL); INSERT INTO peer_writes VALUES(1,0);`); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var batches int
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := first.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
				for range 500 {
					if _, err := tx.ExecContext(ctx, `INSERT INTO bulk(payload) VALUES(hex(randomblob(64)))`); err != nil {
						return err
					}
				}
				_, err := tx.ExecContext(ctx, `DELETE FROM bulk WHERE id IN (SELECT id FROM bulk ORDER BY id LIMIT 500)`)
				return err
			}); err != nil {
				t.Errorf("busy store batch: %v", err)
				return
			}
			batches++
		}
	}()
	const writes = 6
	var failed []string
	var worst time.Duration
	for range writes {
		time.Sleep(20 * time.Millisecond)
		started := time.Now()
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := second.write(writeCtx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE peer_writes SET value=value+1 WHERE id=1`)
			return err
		})
		cancel()
		worst = max(worst, time.Since(started))
		if err != nil {
			failed = append(failed, fmt.Sprintf("after %s: %v", time.Since(started).Round(time.Millisecond), err))
		}
	}
	close(stop)
	<-done
	if len(failed) != 0 || batches == 0 {
		t.Fatalf("%d of %d writes failed beside %d back-to-back batches: %v", len(failed), writes, batches, failed)
	}
	var value int
	if err := second.readDB.QueryRowContext(ctx, `SELECT value FROM peer_writes WHERE id=1`).Scan(&value); err != nil || value != writes {
		t.Fatalf("peer writes committed %d, want %d: %v", value, writes, err)
	}
	t.Logf("longest write %s beside %d batches", worst.Round(time.Millisecond), batches)
}

// While a batch waits for the other worker's write lock, a job whose context
// ends gets its error at once (it never ran) and the others keep waiting; a
// lock that is never released fails the batch after lockWait.
func TestWriteBatchWaitingForAnotherStoreAnswersEndedJobsAndIsBounded(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{})
	if _, err := first.db.ExecContext(ctx, "CREATE TABLE lock_wait_probe(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	insert := func(id int) func(context.Context, *sql.Tx) error {
		return func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO lock_wait_probe VALUES(?)", id)
			return err
		}
	}
	holder, err := second.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	jobs := []*writeJob{batchJob(short, insert(1)), batchJob(ctx, insert(2))}
	committed := make(chan struct{})
	go func() {
		defer close(committed)
		first.writer.commit(jobs)
	}()
	select {
	case err := <-jobs[0].done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ended job while waiting for the lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a job whose context ended kept waiting for the write lock")
	}
	select {
	case err := <-jobs[1].done:
		t.Fatalf("waiting job finished while the other store held the lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-jobs[1].done; err != nil {
		t.Fatalf("waiting job after the lock was released: %v", err)
	}
	<-committed
	var ids string
	if err := first.readDB.QueryRowContext(ctx, "SELECT group_concat(id) FROM lock_wait_probe").Scan(&ids); err != nil || ids != "2" {
		t.Fatalf("committed ids %q: %v", ids, err)
	}

	first.writer.lockWait = 100 * time.Millisecond
	holder, err = second.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	started := time.Now()
	if err := first.write(ctx, insert(3)); !isBusy(err) || time.Since(started) > 2*time.Second {
		t.Fatalf("write behind a held lock returned %v after %s", err, time.Since(started))
	}
}

// Writes outside the batch writer (migrations, dashboard sessions) also wait
// for another worker's write lock instead of failing at once: the writer
// connection has no SQLite busy timeout.
func TestWritesOutsideTheBatchWriterWaitForAnotherWorker(t *testing.T) {
	ctx := context.Background()
	lockFor := func(t *testing.T, path string, hold time.Duration) {
		t.Helper()
		peer, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate"}).String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		tx, err := peer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(hold, func() { _ = tx.Rollback() })
	}

	path := filepath.Join(t.TempDir(), "v40.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:40] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=40", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	lockFor(t, path, 300*time.Millisecond)
	started := time.Now()
	store := openPeerStore(t, path, Options{})
	if waited := time.Since(started); waited < 200*time.Millisecond {
		t.Fatalf("migration did not wait for the held write lock (%s)", waited)
	}
	if state := readSchemaFingerprint(t, path); state.version != SchemaVersion() {
		t.Fatalf("migration under another worker's lock left %+v", state)
	}

	lockFor(t, path, 200*time.Millisecond)
	digest := strings.Repeat("ab", 32)
	if err := store.ConsumeBootstrapToken(ctx, digest, fixedTime); err != nil {
		t.Fatalf("bootstrap token behind another worker's lock: %v", err)
	}
	lockFor(t, path, 200*time.Millisecond)
	if err := store.RevokeDashboardSession(ctx, digest, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("session revocation behind another worker's lock: %v", err)
	}
	lockFor(t, path, 200*time.Millisecond)
	if err := store.AdvanceDashboardSessionEpoch(ctx); err != nil {
		t.Fatalf("session epoch behind another worker's lock: %v", err)
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
	state := func() map[string]string { return reservationOwners(t, first) }
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

// reservationOwners maps each reservation id to "owner/needs_reconciliation".
func reservationOwners(t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows, err := store.readDB.QueryContext(context.Background(), `SELECT id,owner_instance,needs_reconciliation FROM usage_reservations`)
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

// Marking named exited workers flags only their open reservations: a worker
// that is not named (one started after the exit, the caller, or a release that
// recorded no owner) and settled or already flagged rows stay as they are.
func TestMarkInstanceReservationsInterruptedFlagsOnlyNamedWorkers(t *testing.T) {
	ctx := context.Background()
	first, path := testStore(t)
	second := openPeerStore(t, path, Options{InstanceID: "worker-b"})
	exited := openPeerStore(t, path, Options{InstanceID: "worker-c"})
	saveTestAccount(t, first, "acct")
	if err := first.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		store *Store
		id    string
	}{{first, "first"}, {second, "second"}, {exited, "exited"}, {exited, "exited-settled"}} {
		if _, err := item.store.ReserveUsage(ctx, domain.ReservationRequest{ID: item.id, APIKeyID: "key",
			AccountID: "acct", Model: "gpt-test", Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	if err := exited.ReserveWarmupUsage(ctx, "exited-warmup", "acct", "gpt-test", 0, false, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := exited.SettleUsage(ctx, "exited-settled", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
		RequestID: "event-settled", APIKeyID: "key", AccountID: "acct", Model: "gpt-test", RequestKind: "normal",
		Status: "success", RequestedAt: fixedTime}}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 VALUES('unowned','key','acct','gpt-test','reserved',0,0)`); err != nil {
		t.Fatal(err)
	}
	before := reservationOwners(t, first)
	for _, instances := range [][]string{nil, {}, {""}, {"worker-gone"}} {
		if count, err := first.MarkInstanceReservationsInterrupted(ctx, instances); err != nil || count != 0 {
			t.Fatalf("mark %q = %d, %v", instances, count, err)
		}
	}
	if after := reservationOwners(t, first); !maps.Equal(after, before) {
		t.Fatalf("marking no known worker changed %v to %v", before, after)
	}
	if count, err := first.MarkInstanceReservationsInterrupted(ctx, []string{"", "worker-c"}); err != nil || count != 2 {
		t.Fatalf("mark exited worker = %d, %v", count, err)
	}
	want := map[string]string{"first": first.InstanceID() + "/0", "second": "worker-b/0", "exited": "worker-c/1",
		"exited-warmup": "worker-c/1", "exited-settled": "worker-c/0", "unowned": "/0"}
	if got := reservationOwners(t, first); !maps.Equal(got, want) {
		t.Fatalf("after marking the exited worker: %v, want %v", got, want)
	}
	if count, err := first.MarkInstanceReservationsInterrupted(ctx, []string{"worker-c", "worker-b"}); err != nil || count != 1 {
		t.Fatalf("mark again = %d, %v", count, err)
	}
	want["second"] = "worker-b/1"
	if got := reservationOwners(t, first); !maps.Equal(got, want) {
		t.Fatalf("after marking both peers: %v, want %v", got, want)
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
	events := 2*repriceRawChunk + 7
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
	if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != int64(events)*expected {
		t.Fatalf("cost limit %+v, want %d: %v", loaded.Limits, int64(events)*expected, err)
	}
}
