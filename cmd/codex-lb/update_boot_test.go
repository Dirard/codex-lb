package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/domain"
)

func TestBootMigratesReadsTheDatabaseVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	if migrating, err := bootMigrates(ctx, dataDir, 41); err != nil || migrating {
		t.Fatalf("a missing database is created, not migrated: %v %v", migrating, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "codex-lb.sqlite3"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if migrating, err := bootMigrates(ctx, dataDir, 41); err != nil || migrating {
		t.Fatalf("an empty database is created, not migrated: %v %v", migrating, err)
	}
	if err := os.Remove(filepath.Join(dataDir, "codex-lb.sqlite3")); err != nil {
		t.Fatal(err)
	}
	data, err := openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	current := runtimeDescriptor().SchemaVersion
	for schema, want := range map[int]bool{current - 1: false, current: false, current + 1: true} {
		if migrating, err := bootMigrates(ctx, dataDir, schema); err != nil || migrating != want {
			t.Errorf("schema %d: migrating = %v, %v; want %v", schema, migrating, err, want)
		}
	}
}

// installScript stages a shell script as a managed version that reports
// descriptor and never becomes ready.
func installScript(t *testing.T, ctx context.Context, store *updatefiles.Store, descriptor domain.RuntimeDescriptor) domain.RuntimeInstallation {
	t.Helper()
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nif [ \"$1\" = update-info ]; then\n  printf '%s\\n' '" + string(encoded) + "'\n  exit 0\nfi\nexit 37\n")
	stage, err := store.Stage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DiscardStage(stage)
	path := filepath.Join(stage, "codex-lb")
	if err := os.WriteFile(path, script, 0o500); err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, domain.RuntimeBinary{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(script))}, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

// A version that migrates in this boot and then fails before serving is
// replaced by the previous version on the backup taken in the same boot.
func TestFailedMigratingBootRestoresThisBootsBackup(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	previousBinary := buildManagedTestBinary(t, "go-v9.0.0")
	dataDir, address := filepath.Join(t.TempDir(), "data"), managedTestAddress(t)
	data, err := openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{ID: "boot-recovery-key", Name: "kept", KeyHash: strings.Repeat("e", 64), KeyPrefix: "synthetic", IsActive: true, CreatedAt: time.Now()}
	if err := data.store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	store, err := updatefiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host := &updateHost{dataDir: dataDir, stdout: io.Discard, stderr: io.Discard, fatal: make(chan struct{}, 1), listen: address,
		args: []string{"--data-dir", dataDir, "--listen", address, "--dashboard-auth-mode", "disabled", "--shutdown-grace", "1s"}}
	host.privateDir, err = os.MkdirTemp("", "lb-boot-test-")
	if err != nil {
		t.Fatal(err)
	}
	host.parentSocket, host.token = filepath.Join(host.privateDir, "parent.sock"), strings.Repeat("c", 64)
	t.Cleanup(func() { _ = host.Shutdown(); _ = os.RemoveAll(host.privateDir) })
	previousDescriptor, err := host.Inspect(ctx, previousBinary)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Initialize(ctx, previousBinary, previousDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	failing := previousDescriptor
	failing.Version, failing.SchemaVersion, failing.SchemaFloor = "go-v9.1.0", previousDescriptor.SchemaVersion+1, previousDescriptor.SchemaVersion+1
	previous := state.Current
	state.Current, state.Previous = installScript(t, ctx, store, failing), &previous

	migrating, err := bootMigrates(ctx, dataDir, state.Current.Descriptor.SchemaVersion)
	if err != nil || !migrating {
		t.Fatalf("the newer selected version must count as migrating: %v %v", migrating, err)
	}
	if err := host.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := prepareManagedBoot(ctx, store, host, &state, false); err == nil || !strings.Contains(err.Error(), "pre-update database backup") {
		t.Fatalf("without a backup of this boot recovery must refuse: %v", err)
	}
	if err := prepareManagedBoot(ctx, store, host, &state, true); err != nil {
		t.Fatalf("recovery with this boot's backup failed: %v", err)
	}
	if state.Current.Descriptor.Version != "go-v9.0.0" || !strings.Contains(state.LastError, "pre-update backup") {
		t.Fatalf("unexpected recovered state: %+v", state)
	}
	if err := host.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	response, err := managedHTTPClient().Get("http://" + address + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("recovered previous version is not ready: %d", response.StatusCode)
	}
	if err := host.Shutdown(); err != nil {
		t.Fatal(err)
	}
	data, err = openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer data.close()
	if _, err := data.store.GetAPIKey(ctx, key.ID); err != nil {
		t.Fatal("the restored database lost data written before the update")
	}
}
