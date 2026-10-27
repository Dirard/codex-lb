package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

func backupTestStore(t *testing.T) (*Store, *credentials.Vault, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-lb.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "encryption.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateRuntime(context.Background(), vault); err != nil {
		t.Fatal(err)
	}
	return store, vault, dir
}

func TestSnapshotDuringWritesIsVerifiedAndPreservesReservationRowids(t *testing.T) {
	ctx := context.Background()
	store, vault, dir := backupTestStore(t)
	saveTestAccount(t, store, "acct")
	cipher, err := vault.Encrypt([]byte("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "acct",
		AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	insert := func(i int) error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 VALUES(?,'key','acct','m','released',?,?)`, fmt.Sprintf("res-%05d", 100000-i), millis(fixedTime), millis(fixedTime))
		return err
	}
	if _, err := store.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<499)
 INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 SELECT printf('res-%05d',100000-i),'key','acct','m','released',?,? FROM n`, millis(fixedTime), millis(fixedTime)); err != nil {
		t.Fatal(err)
	}
	// Gaps in the implicit rowid sequence must survive the snapshot: outcomes,
	// continuations and affinity versions compare these rowids.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM usage_reservations WHERE rowid%7=0`); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var writes sync.WaitGroup
	writes.Add(1)
	go func() {
		defer writes.Done()
		for i := 500; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := insert(i); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	target := filepath.Join(dir, "snapshot.sqlite3")
	started := time.Now()
	err = NewSnapshotter(store, vault).SnapshotDatabase(ctx, target)
	t.Logf("snapshot took %s", time.Since(started))
	close(stop)
	writes.Wait()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode: %v %v", info, err)
	}
	snapshot, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := snapshot.ValidateRuntime(ctx, vault); err != nil {
		t.Fatalf("snapshot does not open with the installation key: %v", err)
	}
	rowids := func(db interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) map[string]int64 {
		rows, err := db.QueryContext(ctx, `SELECT id,rowid FROM usage_reservations`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]int64{}
		for rows.Next() {
			var id string
			var rowid int64
			if err := rows.Scan(&id, &rowid); err != nil {
				t.Fatal(err)
			}
			result[id] = rowid
		}
		return result
	}
	live, copied := rowids(store.readDB), rowids(snapshot.readDB)
	if len(copied) < 400 {
		t.Fatalf("snapshot reservations=%d", len(copied))
	}
	for id, rowid := range copied {
		if live[id] != rowid {
			t.Fatalf("snapshot renumbered reservation %s: %d != %d", id, rowid, live[id])
		}
	}
}

func TestSnapshotVerificationRejectsForeignKeyAndCorruption(t *testing.T) {
	ctx := context.Background()
	store, vault, dir := backupTestStore(t)
	target := filepath.Join(dir, "good.sqlite3")
	if err := NewSnapshotter(store, vault).SnapshotDatabase(ctx, target); err != nil {
		t.Fatal(err)
	}
	other, err := credentials.Open(filepath.Join(dir, "other.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(ctx, target, other); err == nil {
		t.Fatal("a snapshot verified with a foreign key")
	}
	// A foreign key also fails the write path, which removes the temporary file.
	failed := filepath.Join(dir, "failed.sqlite3")
	if err := WriteSnapshot(ctx, store.filePath, failed, other); err == nil {
		t.Fatal("snapshot with a foreign key was accepted")
	}
	if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed snapshot left a file: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.sqlite3")
	if err := os.WriteFile(corrupt, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(ctx, corrupt, vault); err == nil {
		t.Fatal("a truncated snapshot passed verification")
	}
	if err := WriteSnapshot(ctx, store.filePath, target, vault); !errors.Is(err, ErrConflict) {
		t.Fatalf("existing target was not refused: %v", err)
	}
	if err := NewSnapshotter(memoryStore(t), vault).SnapshotDatabase(ctx, filepath.Join(dir, "memory.sqlite3")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-memory snapshot: %v", err)
	}
}

func memoryStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestBackupScheduleDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	schedule, err := store.LoadBackupSchedule(ctx)
	if err != nil || schedule != (domain.BackupSchedule{IntervalHours: 24, Keep: 3}) {
		t.Fatalf("default schedule = %+v %v", schedule, err)
	}
	if err := store.SaveBackupSchedule(ctx, domain.BackupSchedule{IntervalHours: 0, Keep: 5}); err != nil {
		t.Fatal(err)
	}
	if schedule, err = store.LoadBackupSchedule(ctx); err != nil || schedule != (domain.BackupSchedule{IntervalHours: 0, Keep: 5}) {
		t.Fatalf("saved schedule = %+v %v", schedule, err)
	}
	for _, invalid := range []domain.BackupSchedule{{IntervalHours: -1, Keep: 3}, {IntervalHours: 169, Keep: 3}, {IntervalHours: 24, Keep: 0}, {IntervalHours: 24, Keep: 31}} {
		if err := store.SaveBackupSchedule(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("schedule %+v accepted: %v", invalid, err)
		}
	}
	estimate, err := NewSnapshotter(store, nil).EstimateSnapshotBytes(ctx)
	if err != nil || estimate <= 0 {
		t.Fatalf("estimate = %d %v", estimate, err)
	}
	usage, err := NewSnapshotter(store, nil).StorageUsage(ctx)
	if err != nil || usage.DatabaseBytes <= 0 {
		t.Fatalf("usage = %+v %v", usage, err)
	}
}

func TestOpenResolvesRelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	store, err := Open("relative.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.filePath != filepath.Join(dir, "relative.sqlite3") {
		t.Fatalf("file path = %q", store.filePath)
	}
}
