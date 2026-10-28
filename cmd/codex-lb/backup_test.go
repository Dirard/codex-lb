package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestBackupCommandSnapshotsRunningRuntimeWithoutItsLock(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	r, err := openRuntime(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	now := time.Now().UTC()
	account := domain.Account{ID: "synthetic-account", Kind: domain.AccountChatGPT, Status: domain.AccountActive, Email: "synthetic@example.invalid", CreatedAt: now}
	if err := r.data.store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	cipher, err := r.data.vault.Encrypt([]byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.data.store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(t.TempDir(), "offsite")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"backup", "--data-dir", cfg.dataDir, "--output", outputDir}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Path                  string `json:"path"`
		SizeBytes             int64  `json:"sizeBytes"`
		EncryptionKeyIncluded bool   `json:"encryptionKeyIncluded"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || filepath.Dir(result.Path) != outputDir || result.SizeBytes == 0 || result.EncryptionKeyIncluded {
		t.Fatalf("backup output %q: %v", output.String(), err)
	}
	// The running runtime still owns its database after the backup.
	if err := r.data.store.SaveAccount(ctx, domain.Account{ID: "after", Kind: domain.AccountChatGPT, Status: domain.AccountActive, Email: "after@example.invalid", CreatedAt: now}); err != nil {
		t.Fatalf("runtime lost its database: %v", err)
	}
	// Restore runbook: snapshot plus the separately kept key open as a runtime.
	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.Mkdir(restored, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(t, result.Path, filepath.Join(restored, "codex-lb.sqlite3"))
	copyFile(t, filepath.Join(cfg.dataDir, "encryption.key"), filepath.Join(restored, "encryption.key"))
	data, err := openData(ctx, restored)
	if err != nil {
		t.Fatalf("restored backup does not open: %v", err)
	}
	if got, err := data.store.GetAccount(ctx, account.ID); err != nil || got.Email != account.Email {
		t.Fatalf("restored account = %+v %v", got, err)
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	exact := filepath.Join(outputDir, "manual.sqlite3")
	output.Reset()
	if err := run(ctx, []string{"backup", "--data-dir", cfg.dataDir, "--output", exact}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"backup", "--data-dir", cfg.dataDir, "--output", exact}, &output, io.Discard); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output replaced: %v", err)
	}
	if err := run(ctx, []string{"backup", "--data-dir", cfg.dataDir, "--output", "relative.sqlite3"}, &output, io.Discard); err == nil {
		t.Fatal("relative output accepted")
	}
	if err := run(ctx, []string{"backup", "--data-dir", filepath.Join(t.TempDir(), "missing"), "--output", outputDir}, &output, io.Discard); err == nil {
		t.Fatal("missing database accepted")
	}
}

func TestScheduledBackupRunsInRuntimeDirectory(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	r, err := openRuntime(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	attempted, file, err := r.backups.CreateIfDue(ctx)
	if err != nil || !attempted {
		t.Fatalf("scheduled backup: %v %v", attempted, err)
	}
	info, err := os.Stat(filepath.Join(cfg.dataDir, "backups", file.Name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, "backups", "encryption.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("encryption key copied into backups")
	}
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
