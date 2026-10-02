package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestStoreUsesDurableWALAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var journal string
		var synchronous, foreignKeys int
		if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
			t.Fatalf("journal=%s error=%v", journal, err)
		}
		if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
			t.Fatalf("durability=%d error=%v", synchronous, err)
		}
		if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("foreign_keys=%d error=%v", foreignKeys, err)
		}
		var queryOnly int
		if err := store.readDB.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
			t.Fatalf("reader query_only=%d error=%v", queryOnly, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadPoolDoesNotWaitForActiveWriterTransaction(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	before, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE runtime_settings SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	during, err := store.LoadSettings(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if during.Version != before.Version {
		t.Fatalf("uncommitted version visible: got %d want %d", during.Version, before.Version)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+1 {
		t.Fatalf("committed version not visible: got %d want %d", after.Version, before.Version+1)
	}
}

func TestUsageTotalsReadCommittedDataWithoutWaitingForWriter(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "reader-account")
	if err := store.SaveAPIKey(ctx, testKey("reader-key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reader-reservation", APIKeyID: "reader-key",
		AccountID: "reader-account", Model: "test-model", Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "reader-reservation", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "reader-request", AccountID: "reader-account",
		APIKeyID: "reader-key", Model: "test-model", RequestKind: "normal", Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 2, CostMicrodollars: 17}}}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE usage_totals SET request_count=request_count+100"); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, scope := range []struct{ key, account string }{
		{}, {key: "reader-key"}, {account: "reader-account"}, {key: "reader-key", account: "reader-account"},
	} {
		totals, err := store.UsageTotals(readCtx, scope.key, scope.account)
		if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 10 || totals.Usage.CostMicrodollars != 17 {
			t.Fatalf("scope=%+v totals=%+v error=%v", scope, totals, err)
		}
	}
}

func TestEveryReadConnectionIsBoundedReadOnlyAndConfigured(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	connections := make([]*sql.Conn, readPoolSize)
	defer func() {
		for _, conn := range connections {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	for i := range connections {
		conn, err := store.readDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = conn
		var busyTimeout, synchronous, foreignKeys, queryOnly int
		err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout)
		if err == nil {
			err = conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous)
		}
		if err == nil {
			err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)
		}
		if err == nil {
			err = conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly)
		}
		if err != nil || busyTimeout != 5000 || synchronous != 2 || foreignKeys != 1 || queryOnly != 1 {
			t.Fatalf("connection %d: busy=%d sync=%d fk=%d readonly=%d error=%v", i, busyTimeout, synchronous, foreignKeys, queryOnly, err)
		}
	}
	if _, err := connections[0].ExecContext(ctx, "CREATE TABLE readonly_violation(id INTEGER)"); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestWriterPragmasSurvivePhysicalReconnect(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	for range 2 {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var busyTimeout, synchronous, foreignKeys int
		err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout)
		if err == nil {
			err = conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous)
		}
		if err == nil {
			err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)
		}
		if err != nil || busyTimeout != 5000 || synchronous != 2 || foreignKeys != 1 {
			t.Fatalf("writer reconnect: busy=%d sync=%d fk=%d error=%v", busyTimeout, synchronous, foreignKeys, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		store.db.SetMaxIdleConns(0)
		if stats := store.db.Stats(); stats.OpenConnections != 0 {
			t.Fatalf("idle writer connection retained: %d", stats.OpenConnections)
		}
	}
}

func TestMemoryStoresAreSharedInternallyAndIsolatedFromEachOther(t *testing.T) {
	ctx := context.Background()
	first, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	key := testKey("memory-key", nil)
	if err := first.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetAPIKey(ctx, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("memory stores share state: %v", err)
	}
}

func TestLiteralAndEscapedURIPathsOpenSameDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "literal 100%25?x=1#odd.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := testKey("uri-path", nil)
	if err := first.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	second, err := Open(uri)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	loaded, err := second.GetAPIKey(ctx, key.ID)
	if err != nil || loaded.ID != key.ID {
		t.Fatalf("URI opened another database: key=%+v error=%v", loaded, err)
	}
}
