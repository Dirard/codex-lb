package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func batchJob(ctx context.Context, fn func(context.Context, *sql.Tx) error) *writeJob {
	return &writeJob{ctx: ctx, run: fn, done: make(chan error, 1)}
}

func TestWriteBatchIsolatesFailureAndCallerCancellation(t *testing.T) {
	for _, cancelMiddle := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelMiddle), func(t *testing.T) {
			s, _ := testStore(t)
			if _, err := s.db.Exec("CREATE TABLE batch_probe(id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			jobs := make([]*writeJob, 3)
			for i := range jobs {
				caller := context.Background()
				if i == 1 {
					caller = ctx
				}
				jobs[i] = batchJob(caller, func(sqlCtx context.Context, tx *sql.Tx) error {
					if _, err := tx.ExecContext(sqlCtx, "INSERT INTO batch_probe VALUES(?)", i); err != nil {
						return err
					}
					var visible int
					if err := s.readDB.QueryRow("SELECT count(*) FROM batch_probe").Scan(&visible); err != nil || visible != 0 {
						t.Errorf("uncommitted batch visible: %d %v", visible, err)
					}
					if i == 1 {
						if cancelMiddle {
							cancel()
							if sqlCtx.Err() != nil {
								t.Error("caller cancellation reached shared SQL context")
							}
							return nil
						}
						return ErrInvalid
					}
					return nil
				})
			}
			s.writer.commit(jobs)
			for i, job := range jobs {
				err := <-job.done
				if i != 1 && err != nil || i == 1 && err == nil {
					t.Fatalf("job %d result: %v", i, err)
				}
			}
			var count, sum int
			if err := s.readDB.QueryRow("SELECT count(*),sum(id) FROM batch_probe").Scan(&count, &sum); err != nil || count != 2 || sum != 2 {
				t.Fatalf("isolated rollback: count=%d sum=%d err=%v", count, sum, err)
			}
		})
	}
}

func TestWriteBatchCancellationAfterReleaseStillWaitsForCommit(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := batchJob(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE runtime_settings SET version=version+1 WHERE id=1")
		return err
	})
	second := batchJob(context.Background(), func(context.Context, *sql.Tx) error {
		cancel() // The first job has already RELEASEd its savepoint, not committed.
		select {
		case <-first.done:
			t.Error("job acknowledged before durable commit")
		default:
		}
		return nil
	})
	s.writer.commit([]*writeJob{first, second})
	if err := <-first.done; err != nil {
		t.Fatalf("committed job reported cancelled: %v", err)
	}
	if err := <-second.done; err != nil {
		t.Fatal(err)
	}
}

func TestWriteBatchSkipsQueuedCancelledJob(t *testing.T) {
	s, _ := testStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	first := make(chan error, 1)
	go func() {
		first <- s.write(context.Background(), func(context.Context, *sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := make(chan error, 1)
	go func() {
		second <- s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
			t.Error("cancelled queued callback executed")
			_, err := tx.ExecContext(ctx, "UPDATE runtime_settings SET version=999 WHERE id=1")
			return err
		})
	}()
	for until := time.Now().Add(time.Second); len(s.writer.jobs) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(until) {
			t.Fatal("job was not enqueued")
		}
	}
	cancel()
	unblock()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
	settings, err := s.LoadSettings(context.Background())
	if err != nil || settings.Version != 1 {
		t.Fatalf("cancelled job mutated settings: version=%d err=%v", settings.Version, err)
	}
}

func TestWriteBatchFailsAllOnCommitLossOrPanic(t *testing.T) {
	for _, failure := range []string{"commit", "transaction lost", "panic"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := testStore(t)
			if _, err := s.db.Exec(`CREATE TABLE batch_parent(id INTEGER PRIMARY KEY);
CREATE TABLE batch_child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES batch_parent(id) DEFERRABLE INITIALLY DEFERRED);
INSERT INTO batch_parent VALUES(1);`); err != nil {
				t.Fatal(err)
			}
			jobs := []*writeJob{
				batchJob(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "INSERT INTO batch_child VALUES(1,1)")
					return err
				}),
				batchJob(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
					switch failure {
					case "panic":
						panic("synthetic-private-panic-marker")
					case "transaction lost":
						_, err := tx.ExecContext(ctx, "ROLLBACK")
						return err
					default:
						_, err := tx.ExecContext(ctx, "INSERT INTO batch_child VALUES(2,999)")
						return err // Deferred foreign-key violation fails the outer commit.
					}
				}),
			}
			s.writer.commit(jobs)
			for _, job := range jobs {
				if err := <-job.done; err == nil || strings.Contains(err.Error(), "synthetic-private-panic-marker") {
					t.Fatalf("false success or leaked panic: %v", err)
				}
			}
			var rows int
			if err := s.readDB.QueryRow("SELECT count(*) FROM batch_child").Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("failed batch retained rows: %d %v", rows, err)
			}
			if err := s.write(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO batch_child VALUES(3,1)")
				return err
			}); err != nil {
				t.Fatalf("writer did not recover: %v", err)
			}
		})
	}
}

func TestWriteBatchCloseDrainsAcceptedJobs(t *testing.T) {
	s, path := testStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unlock()
	results := make(chan error, 21)
	go func() {
		results <- s.write(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			close(entered)
			<-release
			_, err := tx.ExecContext(ctx, "UPDATE runtime_settings SET version=version+1 WHERE id=1")
			return err
		})
	}()
	<-entered
	for range 20 {
		go func() {
			results <- s.write(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE runtime_settings SET version=version+1 WHERE id=1")
				return err
			})
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.writer.jobs) != 20 {
		if time.Now().After(deadline) {
			t.Fatal("writes not queued")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	for {
		s.writer.mu.Lock()
		closing := s.writer.closed
		s.writer.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not stop admission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.write(context.Background(), func(context.Context, *sql.Tx) error {
		t.Error("new job ran during Close")
		return nil
	}); err == nil {
		t.Fatal("Close accepted new work")
	}
	select {
	case <-closed:
		t.Fatal("Close abandoned pending work")
	default:
	}
	unlock()
	for range 21 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.LoadSettings(context.Background())
	if err != nil || settings.Version != 22 { // Initial version is one.
		t.Fatalf("accepted work lost at close: version=%d err=%v", settings.Version, err)
	}
}

func TestBatchedReservationsCannotOverdrawSharedKey(t *testing.T) {
	s, _ := testStore(t)
	saveTestAccount(t, s, "batch-account")
	now := time.Now().UTC()
	key := testKey("batch-key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100, ResetAt: now.Add(time.Hour)}}
	if err := s.SaveAPIKey(context.Background(), key, now); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 32)
	for i := range 32 {
		go func() {
			_, err := s.ReserveUsage(context.Background(), domain.ReservationRequest{ID: fmt.Sprintf("batch-reserve-%d", i),
				APIKeyID: key.ID, AccountID: "batch-account", Model: "model", Now: now, Budget: domain.UsageAmount{InputTokens: 10}})
			results <- err
		}()
	}
	accepted := 0
	for range 32 {
		if err := <-results; err == nil {
			accepted++
		} else if !errors.Is(err, ErrLimitReached) {
			t.Fatal(err)
		}
	}
	current, err := s.GetAPIKey(context.Background(), key.ID)
	if err != nil || accepted != 10 || current.Limits[0].CurrentValue != 100 {
		t.Fatalf("budget overdraw: accepted=%d limits=%+v err=%v", accepted, current.Limits, err)
	}
}
