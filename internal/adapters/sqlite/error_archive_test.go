package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

func TestArchiveByteBoundPrunesOnlyDiagnostics(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt([]byte(strings.Repeat("x", 1024)))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute)
	for index, id := range []string{"first", "second", "third"} {
		entry := domain.ErrorArchive{RequestID: id, OccurredAt: started.Add(time.Duration(index) * time.Second), ExpiresAt: time.Now().Add(time.Hour), ContentEncrypted: encrypted}
		if err := store.SaveErrorArchive(ctx, entry, int64(len(encrypted)*2)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.QueryErrorArchives(ctx, domain.ErrorArchiveFilter{Start: &started, Limit: 100}, time.Now())
	if err != nil || page.Total != 2 || page.Items[0].RequestID != "second" || page.Items[1].RequestID != "third" {
		t.Fatalf("byte retention failed: %d %v", page.Total, err)
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM usage_events").Scan(&count); err != nil || count != 0 {
		t.Fatal("archive affected accounting rows")
	}
}
