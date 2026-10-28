package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"codex-lb/internal/domain"
)

func TestAdoptedBootstrapBacksUpBeforeItsMigration(t *testing.T) {
	t.Parallel()
	installation := func(schema int) domain.RuntimeInstallation {
		return domain.RuntimeInstallation{Descriptor: domain.RuntimeDescriptor{SchemaVersion: schema}}
	}
	older, newer := installation(33), installation(41)
	if !adoptionMigrates(true, domain.RuntimeInstallState{Current: newer, Previous: &older}) {
		t.Fatal("an adopted newer schema must be backed up first")
	}
	for name, state := range map[string]domain.RuntimeInstallState{
		"same schema": {Current: newer, Previous: &newer},
		"downgrade":   {Current: older, Previous: &newer},
		"first boot":  {Current: newer},
	} {
		if adoptionMigrates(true, state) {
			t.Errorf("%s: unexpected pre-migration backup", name)
		}
	}
	if adoptionMigrates(false, domain.RuntimeInstallState{Current: newer, Previous: &older}) {
		t.Fatal("a restart without adoption must not back up")
	}

	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "updates"), 0o700); err != nil {
		t.Fatal(err)
	}
	host := &updateHost{dataDir: dataDir}
	if err := backupExistingDatabase(ctx, host, dataDir); err != nil {
		t.Fatalf("a fresh installation has nothing to back up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "updates", "backup-current")); !os.IsNotExist(err) {
		t.Fatal("a backup was recorded without a database")
	}
	data, err := openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	if err := backupExistingDatabase(ctx, host, dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "updates", "backup-current")); err != nil {
		t.Fatal("the existing database was not backed up")
	}
}
