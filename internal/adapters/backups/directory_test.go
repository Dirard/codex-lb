package backups

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestDirectoryPublishesWithoutReplacingAndListsNewestFirst(t *testing.T) {
	dir, err := OpenDirectory(filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir.Path())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	at := time.Date(2026, 10, 28, 3, 15, 0, 0, time.UTC)
	publish := func(at time.Time, content string) (domain.BackupFile, error) {
		temp, name, err := dir.TempPath(at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(temp, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := dir.Publish(temp, name)
		if err != nil {
			dir.Discard(temp)
		}
		return file, err
	}
	first, err := publish(at, "first")
	if err != nil || first.Name != "codex-lb-20261028T031500Z.sqlite3" || first.SizeBytes != 5 || !first.CreatedAt.Equal(at) {
		t.Fatalf("first = %+v %v", first, err)
	}
	if _, err := publish(at, "second"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("existing backup replaced: %v", err)
	}
	if _, err := publish(at.Add(time.Hour), "newer"); err != nil {
		t.Fatal(err)
	}
	// Leftovers and foreign names are never listed or served.
	for _, name := range []string{".codex-lb-x.sqlite3.tmp-1", "notes.txt", "codex-lb-2026.sqlite3"} {
		if err := os.WriteFile(filepath.Join(dir.Path(), name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := dir.List()
	if err != nil || len(files) != 2 || files[0].Name != "codex-lb-20261028T041500Z.sqlite3" {
		t.Fatalf("list = %+v %v", files, err)
	}
	reader, file, err := dir.Open(first.Name)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(content) != "first" || file.SizeBytes != 5 {
		t.Fatalf("open = %q %+v", content, file)
	}
	for _, name := range []string{"../codex-lb-20261028T031500Z.sqlite3", "notes.txt", "", "codex-lb-20261028T031500Z.sqlite3/x"} {
		if _, _, err := dir.Open(name); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("open %q: %v", name, err)
		}
	}
	link := "codex-lb-20261028T051500Z.sqlite3"
	if err := os.Symlink("/etc/hostname", filepath.Join(dir.Path(), link)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dir.Open(link); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("symlink served: %v", err)
	}
	dir.RemoveStaleTemps()
	if _, err := os.Stat(filepath.Join(dir.Path(), ".codex-lb-x.sqlite3.tmp-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp kept: %v", err)
	}
	if err := dir.Remove(first.Name); err != nil {
		t.Fatal(err)
	}
	if err := dir.Remove("../escape"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("remove escaped: %v", err)
	}
	free, total, err := dir.Space()
	if err != nil || total == 0 || free > total {
		t.Fatalf("space free=%d total=%d err=%v", free, total, err)
	}
}

func TestOpenDirectoryRefusesSharedOrSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirectory(shared); err == nil {
		t.Fatal("world-readable backup directory accepted")
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirectory(link); err == nil {
		t.Fatal("symlinked backup directory accepted")
	}
	if _, err := OpenDirectory("relative"); err == nil {
		t.Fatal("relative backup directory accepted")
	}
}
