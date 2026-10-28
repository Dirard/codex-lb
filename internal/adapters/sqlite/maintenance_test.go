package sqlite

import (
	"context"
	"os"
	"testing"
)

func TestWALIsLimitedCheckpointedAndTruncatedWhenReadersFinish(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	var limit int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_size_limit").Scan(&limit); err != nil || limit != walSizeLimit {
		t.Fatalf("journal_size_limit = %d %v", limit, err)
	}
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE wal_probe(payload BLOB)"); err != nil {
		t.Fatal(err)
	}
	// A long reader pins its snapshot, so checkpoints cannot reset the WAL.
	reader, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM wal_probe").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<1999)
 INSERT INTO wal_probe SELECT randomblob(4096) FROM n`); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CheckpointWAL(ctx)
	if err != nil || pinned.Truncated {
		t.Fatalf("checkpoint under a long reader = %+v %v", pinned, err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() < 4<<20 {
		t.Fatalf("WAL did not grow under the reader: %v %v", info, err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	result, err := s.CheckpointWAL(ctx)
	if err != nil || result.Busy || !result.Truncated {
		t.Fatalf("idle checkpoint = %+v %v", result, err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() != 0 {
		t.Fatalf("WAL not truncated: %v %v", info, err)
	}
	if err := s.Optimize(ctx); err != nil {
		t.Fatalf("optimize: %v", err)
	}
	// Writes keep working after maintenance.
	if _, err := s.db.ExecContext(ctx, "INSERT INTO wal_probe VALUES(x'00')"); err != nil {
		t.Fatal(err)
	}
}
