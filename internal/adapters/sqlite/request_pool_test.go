package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRequestPoolRoundTripsAndClears(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	key := domain.APIKey{ID: "pooled", Name: "Pooled", KeyHash: fmt.Sprintf("%064x", 7), KeyPrefix: "sk-clb-pooled", IsActive: true,
		RequestPool: &domain.RequestPool{RequestsPerMinute: 120, MaxWaitSeconds: 30}}
	if err := s.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAPIKey(ctx, "pooled")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestPool == nil || *got.RequestPool != (domain.RequestPool{RequestsPerMinute: 120, MaxWaitSeconds: 30}) {
		t.Fatalf("pool %+v", got.RequestPool)
	}
	got.RequestPool = &domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 0}
	if err := s.SaveAPIKey(ctx, got, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetAPIKey(ctx, "pooled"); err != nil || *got.RequestPool != (domain.RequestPool{RequestsPerMinute: 1}) {
		t.Fatalf("pool %+v %v", got.RequestPool, err)
	}
	got.RequestPool = nil
	if err := s.SaveAPIKey(ctx, got, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetAPIKey(ctx, "pooled"); err != nil || got.RequestPool != nil {
		t.Fatalf("pool %+v %v", got.RequestPool, err)
	}
	for _, pool := range []domain.RequestPool{{RequestsPerMinute: 0, MaxWaitSeconds: 30}, {RequestsPerMinute: 10001}, {RequestsPerMinute: 5, MaxWaitSeconds: 61}, {RequestsPerMinute: 5, MaxWaitSeconds: -1}} {
		got.RequestPool = &pool
		if err := s.SaveAPIKey(ctx, got, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("pool %+v saved: %v", pool, err)
		}
	}
	// The columns refuse what validation refuses, whoever writes them.
	if _, err := s.db.ExecContext(ctx, "UPDATE api_keys SET request_pool_rpm=0 WHERE id='pooled'"); err == nil {
		t.Fatal("database accepted a zero pool")
	}
}

// Schema 49 keys gain no pool, and code that predates pools upserts a key
// without touching its pool.
func TestMigration50LeavesKeysUnpooledAndBesideOlderWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v49.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:49] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		fmt.Sprintf("INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES('old','Old','%064x','sk-clb-old',0)", 9),
		fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=49",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.readDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion() {
		t.Fatalf("version %d %v", version, err)
	}
	key, err := store.GetAPIKey(ctx, "old")
	if err != nil || key.RequestPool != nil {
		t.Fatalf("migrated key %+v %v", key.RequestPool, err)
	}
	key.RequestPool = &domain.RequestPool{RequestsPerMinute: 120, MaxWaitSeconds: 30}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The upsert of schema 49 code names only the columns it knows.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES('old','Renamed',?, 'sk-clb-old',0)
 ON CONFLICT(id) DO UPDATE SET name=excluded.name WHERE api_keys.deleted_at IS NULL`, key.KeyHash); err != nil {
		t.Fatal(err)
	}
	if key, err = store.GetAPIKey(ctx, "old"); err != nil || key.Name != "Renamed" || key.RequestPool == nil || key.RequestPool.RequestsPerMinute != 120 {
		t.Fatalf("older upsert lost the pool: %+v %v", key, err)
	}
}
