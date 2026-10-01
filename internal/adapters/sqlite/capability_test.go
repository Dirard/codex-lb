package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func ensureCapabilitySchema(t *testing.T, store *Store) {
	t.Helper()
	var table string
	err := store.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='capability_lineage_markers'").Scan(&table)
	if err == sql.ErrNoRows {
		if _, err := store.db.Exec(schemaV17); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityLineageIsDurableScopeIsolatedAndOpaque(t *testing.T) {
	store, _ := testStore(t)
	ensureCapabilitySchema(t, store)
	ctx := context.Background()
	aliases := []domain.CapabilityLineageAlias{
		{Kind: "session_header", Value: "visible-session"},
		{Kind: "turn_state", Value: "visible-turn"},
		{Kind: "previous_response", Value: "resp-visible"},
		{Kind: "codex_task", Value: "visible-task"},
		{Kind: "codex_window", Value: "visible-window:12"},
		{Kind: "codex_task", Value: "visible-window"},
	}
	hashes, err := store.RequireCapability(ctx, domain.TrustedCyberCapability, "key-visible", aliases)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != len(aliases) {
		t.Fatalf("marker count = %d, want %d", len(hashes), len(aliases))
	}
	var stored []string
	rows, err := store.db.Query("SELECT marker_hash FROM capability_lineage_markers ORDER BY marker_hash")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(hashes) {
		t.Fatalf("stored marker count = %d, want %d", len(stored), len(hashes))
	}
	for _, hash := range stored {
		if strings.Contains(hash, "visible-") || strings.Contains(hash, "key-visible") {
			t.Fatalf("marker stored lineage or scope material: %s", hash)
		}
	}
	required, err := store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "key-visible", aliases)
	if err != nil || !required {
		t.Fatalf("lineage did not survive repository read: %v %v", required, err)
	}
	required, err = store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "key-other", aliases)
	if err != nil || required {
		t.Fatalf("API-key scope leaked: %v %v", required, err)
	}
}

func TestCapabilityLineageSurvivesRestartAndImport(t *testing.T) {
	store, path := testStore(t)
	ensureCapabilitySchema(t, store)
	ctx := context.Background()
	alias := domain.CapabilityLineageAlias{Kind: "session_header", Value: "session-restart"}
	if _, err := store.RequireCapability(ctx, domain.TrustedCyberCapability, "key-restart", []domain.CapabilityLineageAlias{alias}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	required, err := reopened.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "key-restart", []domain.CapabilityLineageAlias{alias})
	if err != nil || !required {
		t.Fatalf("marker did not survive restart: %v %v", required, err)
	}

	sourcePath := filepath.Join(t.TempDir(), "legacy-capability.sqlite")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`CREATE TABLE capability_lineage_markers
 (marker_hash TEXT PRIMARY KEY,created_at DATETIME NOT NULL,last_seen_at DATETIME NOT NULL);
 INSERT INTO capability_lineage_markers VALUES('` + strings.Repeat("a", 64) + `','2026-09-01 10:00:00','2026-09-01 10:01:00')`); err != nil {
		t.Fatal(err)
	}
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTx.Rollback()
	importStore, err := Open(filepath.Join(t.TempDir(), "capability-import.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer importStore.Close()
	ensureCapabilitySchema(t, importStore)
	destTx, err := importStore.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer destTx.Rollback()
	if err := importLegacyCapabilityLineage(ctx, sourceTx, destTx); err != nil {
		t.Fatal(err)
	}
	if err := destTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var created int64
	if err := importStore.db.QueryRow("SELECT created_at FROM capability_lineage_markers WHERE marker_hash=?", strings.Repeat("a", 64)).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if fromMillis(created).Format(time.RFC3339) != "2026-09-01T10:00:00Z" {
		t.Fatalf("legacy marker timestamp = %v", fromMillis(created))
	}
}
