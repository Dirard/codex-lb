package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"codex-lb/internal/application"
)

func TestImportPreservesFirewallWithoutMutatingSnapshot(t *testing.T) {
	source, vault, _ := legacyFixture(t)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE api_firewall_allowlist(ip_address TEXT,created_at TEXT);
 INSERT INTO api_firewall_allowlist VALUES('::ffff:192.0.2.9','2026-09-26 12:00:00');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := hashFile(source)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := testStore(t)
	if _, err := store.ImportLegacySnapshot(context.Background(), source, vault); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListFirewallEntries(context.Background())
	if err != nil || len(entries) != 1 || entries[0].IPAddress != "192.0.2.9" {
		t.Fatalf("firewall dropped: %+v %v", entries, err)
	}
	if _, err := application.NewFirewall(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	after, err := hashFile(source)
	if err != nil || before != after {
		t.Fatal("legacy source mutated")
	}
}
