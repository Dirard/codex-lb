package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"codex-lb/internal/domain"
)

func TestImportLegacySnapshotRetainsCapabilityLineage(t *testing.T) {
	sourcePath, vault, _ := legacyFixture(t)
	alias := domain.CapabilityLineageAlias{Kind: "session_header", Value: "synthetic-session"}
	hash, err := domain.CapabilityLineageMarkerHash(domain.TrustedCyberCapability, "legacy-key", alias)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`CREATE TABLE capability_lineage_markers (
 marker_hash TEXT PRIMARY KEY, created_at DATETIME NOT NULL, last_seen_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO capability_lineage_markers VALUES(?, '2026-09-01 10:00:00', '2026-09-01 10:01:00')`, hash); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "imported.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.ImportLegacySnapshot(ctx, sourcePath, vault); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key  string
		want bool
	}{{"legacy-key", true}, {"other-key", false}} {
		required, err := store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, test.key, []domain.CapabilityLineageAlias{alias})
		if err != nil || required != test.want {
			t.Fatalf("imported marker under %q = %v, %v", test.key, required, err)
		}
	}
}
