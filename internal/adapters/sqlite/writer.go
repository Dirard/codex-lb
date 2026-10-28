package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// writeBatchSize bounds one grouped commit; savepoints keep jobs independent.
const writeBatchSize = 64

// writeExecutionTimeout bounds executing one batch. It starts only after the
// writer connection is obtained, so waiting behind another transaction never
// fails queued jobs.
const writeExecutionTimeout = 5 * time.Second

type writeJob struct {
	ctx  context.Context
	run  func(context.Context, *sql.Tx) error
	done chan error
}

// batchWriter shares one durable commit among already waiting short mutations.
// A job never returns while its callback or commit is still in progress.
type batchWriter struct {
	db      *sql.DB
	timeout time.Duration
	jobs    chan *writeJob
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	pending sync.WaitGroup
	once    sync.Once
}

func newBatchWriter(db *sql.DB) *batchWriter {
	w := &batchWriter{db: db, timeout: writeExecutionTimeout, jobs: make(chan *writeJob, 128), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case first := <-w.jobs:
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
	// No caller can interrupt another job's SQLite transaction. A bounded batch
	// deadline still prevents a damaged/blocked writer from running indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	tx, fatal = conn.BeginTx(ctx, nil)
	if fatal != nil {
		return
	}
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
