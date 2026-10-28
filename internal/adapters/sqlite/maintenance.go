package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

// truncateWALAbove skips the truncating checkpoint for a WAL that is already
// small; the writer reuses it from the start after each reset anyway.
const truncateWALAbove = 1 << 20

// WALCheckpoint is the outcome of one maintenance checkpoint.
type WALCheckpoint struct {
	Busy         bool
	LogFrames    int
	Checkpointed int
	Truncated    bool
}

// CheckpointWAL is the workflow step "PASSIVE checkpoint on a maintenance
// connection --> [backlog, idle --TRUNCATE with 1 s busy timeout--> empty WAL]".
// PASSIVE never waits for the writer or readers. TRUNCATE runs only when every
// frame is already in the database file and the WAL is larger than 1 MiB; if
// a reader or writer is active it gives up after the maintenance busy timeout
// and is retried on the next cycle.
func (s *Store) CheckpointWAL(ctx context.Context) (WALCheckpoint, error) {
	var result WALCheckpoint
	conn, err := s.maintenance.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	var busy int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &result.LogFrames, &result.Checkpointed); err != nil {
		return result, err
	}
	result.Busy = busy != 0
	if result.Busy || result.LogFrames < 0 || result.Checkpointed != result.LogFrames || s.filePath == "" {
		return result, nil
	}
	info, err := os.Stat(s.filePath + "-wal")
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Size() <= truncateWALAbove {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var log, checkpointed int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		if isBusy(err) {
			result.Busy = true
			return result, nil
		}
		return result, err
	}
	result.Busy, result.Truncated = busy != 0, busy == 0
	return result, nil
}

// Optimize refreshes planner statistics through the batch writer; the writer
// connection samples at most analysis_limit rows per index, so the job stays
// short.
func (s *Store) Optimize(ctx context.Context) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "PRAGMA optimize=0x10002")
		return err
	})
}

func isBusy(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		// SQLITE_BUSY and SQLITE_LOCKED, including extended codes.
		code := coded.Code() & 0xff
		return code == 5 || code == 6
	}
	return false
}
