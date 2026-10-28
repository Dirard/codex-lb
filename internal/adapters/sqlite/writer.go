package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// writeBatchSize bounds one grouped commit; savepoints keep jobs independent.
const writeBatchSize = 64

// writeExecutionTimeout bounds executing one batch. It starts only after the
// writer connection and the database write lock are obtained, so waiting
// behind another transaction, in this process or another, never fails queued
// jobs.
const writeExecutionTimeout = 5 * time.Second

// writeLockWait bounds how long a write waits for the database write lock.
// The writer connection has no SQLite busy timeout: SQLite's busy handler
// sleeps up to 100 ms between attempts and rarely catches the lock free while
// another worker of the installation commits short transactions back to back.
// retryBusy polls every few milliseconds instead.
const writeLockWait = 15 * time.Second

// writeLockRetryDelay is the jittered pause between write lock attempts.
func writeLockRetryDelay() time.Duration {
	return time.Millisecond + rand.N(4*time.Millisecond)
}

// Polling alone rarely wins against batches that follow each other within
// microseconds, the longer each batch the less. A writer that kept the lock
// busy for writeLockShare without a gap of writeLockYield therefore leaves
// such a gap, longer than any retry delay, before its next batch. That costs
// at most 4 % of a saturated writer and nothing otherwise.
const (
	writeLockShare = 250 * time.Millisecond
	writeLockYield = 10 * time.Millisecond
)

// retryBusy repeats attempt while it fails because another connection holds
// the write lock, until wait reports that nobody waits any longer or budget
// passed. attempt must leave nothing changed when it fails busy, as BEGIN
// IMMEDIATE and single autocommit statements do.
func retryBusy(budget time.Duration, wait func() bool, attempt func() error) error {
	deadline := time.Now().Add(budget)
	for {
		err := attempt()
		if err == nil || !isBusy(err) || !wait() || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(writeLockRetryDelay())
	}
}

// beginWrite starts a write transaction on the writer pool outside the batch
// writer (migrations, the legacy import), waiting for the write lock like the
// batch writer does while ctx allows.
func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, error) {
	var tx *sql.Tx
	err := retryBusy(writeLockWait, func() bool { return ctx.Err() == nil }, func() (err error) {
		tx, err = s.db.BeginTx(ctx, nil)
		return err
	})
	return tx, err
}

type writeJob struct {
	ctx  context.Context
	run  func(context.Context, *sql.Tx) error
	done chan error
}

// batchWriter shares one durable commit among already waiting short mutations.
// A job never returns while its callback or commit is still in progress.
type batchWriter struct {
	db       *sql.DB
	timeout  time.Duration
	lockWait time.Duration
	jobs     chan *writeJob
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
	pending  sync.WaitGroup
	once     sync.Once
}

func newBatchWriter(db *sql.DB) *batchWriter {
	w := &batchWriter{db: db, timeout: writeExecutionTimeout, lockWait: writeLockWait, jobs: make(chan *writeJob, 128), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		// busySince starts the current run of batches that left the write lock
		// no gap of writeLockYield; ended is when the previous batch finished.
		var busySince, ended time.Time
		for {
			select {
			case first := <-w.jobs:
				if gap := time.Since(ended); gap >= writeLockYield {
					busySince = time.Now()
				} else if time.Since(busySince) >= writeLockShare {
					time.Sleep(writeLockYield - gap)
					busySince = time.Now()
				}
				batch := []*writeJob{first}
			collect:
				for len(batch) < writeBatchSize {
					select {
					case next := <-w.jobs:
						batch = append(batch, next)
					default:
						break collect
					}
				}
				w.commit(batch)
				ended = time.Now()
			case <-w.stop:
				return
			}
		}
	}()
	return w
}

func (s *Store) write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	w := s.writer
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("SQLite writer is closed")
	}
	w.pending.Add(1)
	w.mu.Unlock()
	defer w.pending.Done()
	job := &writeJob{ctx: ctx, run: fn, done: make(chan error, 1)}
	select {
	case w.jobs <- job:
		// Cancellation after enqueue must await the definitive transaction result.
		return <-job.done
	case <-ctx.Done():
		return ctx.Err()
	}
}

type execResult struct{ lastID, affected int64 }

func (r execResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r execResult) RowsAffected() (int64, error) { return r.affected, nil }

// exec runs one statement as its own batch-writer job.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var out execResult
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if out.affected, err = result.RowsAffected(); err != nil {
			return err
		}
		out.lastID, _ = result.LastInsertId()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (w *batchWriter) close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.pending.Wait()
		close(w.stop)
		<-w.done
	})
}

func (w *batchWriter) commit(jobs []*writeJob) {
	results := make([]error, len(jobs))
	var fatal error
	var conn *sql.Conn
	var tx *sql.Tx
	defer func() {
		if recover() != nil {
			fatal = errors.New("SQLite write callback failed")
		}
		if tx != nil {
			_ = tx.Rollback()
		}
		if conn != nil {
			_ = conn.Close()
		}
		for i, job := range jobs {
			if results[i] == nil {
				results[i] = fatal
			}
			job.done <- results[i]
		}
	}()
	// Only migrations and the offline import open direct writer transactions;
	// waiting for them is not part of this batch's execution budget.
	conn, fatal = w.db.Conn(context.Background())
	if fatal != nil {
		return
	}
	// BEGIN IMMEDIATE is retried while another process of the installation
	// holds the write lock; that is not execution time either. A job whose
	// context ends meanwhile never ran, so it gets its error at once and leaves
	// the batch; the others keep waiting up to lockWait.
	txCtx, cancelTx := context.WithCancel(context.Background())
	defer cancelTx()
	fatal = retryBusy(w.lockWait, func() bool {
		jobs = answerCancelled(jobs)
		return len(jobs) != 0
	}, func() (err error) {
		tx, err = conn.BeginTx(txCtx, nil)
		return err
	})
	if fatal != nil {
		return
	}
	// No caller can interrupt another job's SQLite transaction. A bounded batch
	// deadline still prevents a damaged/blocked writer from running indefinitely.
	ctx, cancel := context.WithTimeout(txCtx, w.timeout)
	defer cancel()
	for i, job := range jobs {
		if results[i] = job.ctx.Err(); results[i] != nil {
			continue
		}
		if _, fatal = tx.ExecContext(ctx, "SAVEPOINT write_job"); fatal != nil {
			return
		}
		results[i] = job.run(ctx, tx)
		if results[i] == nil {
			results[i] = job.ctx.Err()
		}
		if results[i] != nil {
			if _, fatal = tx.ExecContext(ctx, "ROLLBACK TO write_job"); fatal != nil {
				return
			}
		}
		if _, fatal = tx.ExecContext(ctx, "RELEASE write_job"); fatal != nil {
			return
		}
	}
	fatal = tx.Commit()
}

// answerCancelled completes the jobs whose context ended before their batch
// began; none of them ran. It returns the jobs that still wait and leaves the
// given slice unchanged.
func answerCancelled(jobs []*writeJob) []*writeJob {
	if !slices.ContainsFunc(jobs, func(job *writeJob) bool { return job.ctx.Err() != nil }) {
		return jobs
	}
	waiting := make([]*writeJob, 0, len(jobs))
	for _, job := range jobs {
		if err := job.ctx.Err(); err != nil {
			job.done <- err
		} else {
			waiting = append(waiting, job)
		}
	}
	return waiting
}
