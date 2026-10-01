package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"codex-lb/internal/adapters/credentials"
)

func TestImportDryRunLeavesDestinationUntouchedOnFailure(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "synthetic.key")
	if _, err := credentials.Open(keyPath, true); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "do-not-create")
	err := importLegacy(context.Background(), config{sourceKey: keyPath, source: filepath.Join(directory, "missing.sqlite"), dataDir: destination, dryRun: true}, io.Discard)
	if err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dry-run modified destination")
	}
	if _, err := os.Stat(filepath.Join(directory, "missing.sqlite")); !os.IsNotExist(err) {
		t.Fatal("import created the missing source")
	}
}

func TestImportWillNotOverwriteAnExistingInstallation(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "source.key")
	vault, err := credentials.Open(keyPath, true)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "copy.key")
	if err := copyImportKey(keyPath, target, vault.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if err := copyImportKey(keyPath, target, vault.Fingerprint()); err == nil {
		t.Fatal("key overwrite permitted")
	}
	copyVault, err := credentials.Open(target, false)
	if err != nil || copyVault.Fingerprint() != vault.Fingerprint() {
		t.Fatal("key bytes changed")
	}
	err = importLegacy(context.Background(), config{sourceKey: keyPath, source: filepath.Join(directory, "source.sqlite"), dataDir: directory}, io.Discard)
	if err == nil {
		t.Fatal("existing destination accepted")
	}
	if _, err := os.Stat(filepath.Join(directory, "codex-lb.sqlite3")); !os.IsNotExist(err) {
		t.Fatal("existing installation modified")
	}
}
