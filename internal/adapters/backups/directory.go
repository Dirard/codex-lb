// Package backups stores verified database snapshots in a private directory:
// temporary files, atomic no-replace publication, listing, rotation and
// download access. It never stores the installation encryption key.
package backups

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const nameLayout = "20060102T150405Z"

var namePattern = regexp.MustCompile(`^codex-lb-[0-9]{8}T[0-9]{6}Z\.sqlite3$`)

// Name is the published file name of a snapshot taken at the given time.
func Name(at time.Time) string { return "codex-lb-" + at.UTC().Format(nameLayout) + ".sqlite3" }

// ValidName reports whether name is a backup file name; it never contains a path.
func ValidName(name string) bool { return namePattern.MatchString(name) }

func createdAt(name string) (time.Time, bool) {
	if !ValidName(name) {
		return time.Time{}, false
	}
	at, err := time.Parse(nameLayout, strings.TrimSuffix(strings.TrimPrefix(name, "codex-lb-"), ".sqlite3"))
	return at, err == nil
}

// Directory is one private backups directory.
type Directory struct{ path string }

// OpenDirectory creates the directory with mode 0700 if needed and refuses a
// symlink or a directory readable by other users.
func OpenDirectory(path string) (*Directory, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("backup directory must be absolute")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, errors.New("cannot create backup directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("backup directory must be a private directory (mode 0700), not a symlink")
	}
	return &Directory{path: filepath.Clean(path)}, nil
}

func (d *Directory) Path() string { return d.path }

// List returns published backups, newest first. Temporary files are ignored.
func (d *Directory) List() ([]domain.BackupFile, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, err
	}
	files := make([]domain.BackupFile, 0, len(entries))
	for _, entry := range entries {
		at, ok := createdAt(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		files = append(files, domain.BackupFile{Name: entry.Name(), SizeBytes: info.Size(), CreatedAt: at})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })
	return files, nil
}

// TempPath returns a new temporary path in the directory and the name under
// which a snapshot taken at the given time will be published.
func (d *Directory) TempPath(at time.Time) (temp, name string, err error) {
	name = Name(at)
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", "", err
	}
	return filepath.Join(d.path, "."+name+".tmp-"+hex.EncodeToString(suffix[:])), name, nil
}

// Publish atomically links the verified temporary file under name without
// replacing an existing backup, removes the temporary name and flushes the
// directory entry.
func (d *Directory) Publish(temp, name string) (domain.BackupFile, error) {
	if !ValidName(name) || filepath.Dir(temp) != d.path {
		return domain.BackupFile{}, domain.ErrInvalid
	}
	return publish(temp, filepath.Join(d.path, name))
}

// Discard removes an unpublished temporary snapshot.
func (d *Directory) Discard(temp string) {
	if filepath.Dir(temp) == d.path && strings.HasPrefix(filepath.Base(temp), ".codex-lb-") {
		_ = os.Remove(temp)
		_ = os.Remove(temp + "-journal")
	}
}

// RemoveStaleTemps deletes temporary snapshots left by an interrupted process.
func (d *Directory) RemoveStaleTemps() {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if name := entry.Name(); strings.HasPrefix(name, ".codex-lb-") && strings.Contains(name, ".tmp-") {
			_ = os.Remove(filepath.Join(d.path, name))
		}
	}
}

// Remove deletes one published backup.
func (d *Directory) Remove(name string) error {
	if !ValidName(name) {
		return domain.ErrInvalid
	}
	err := os.Remove(filepath.Join(d.path, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Open opens a published backup for download; only regular files matching the
// backup name pattern inside this directory are served.
func (d *Directory) Open(name string) (io.ReadSeekCloser, domain.BackupFile, error) {
	at, ok := createdAt(name)
	if !ok {
		return nil, domain.BackupFile{}, domain.ErrNotFound
	}
	path := filepath.Join(d.path, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.BackupFile{}, domain.ErrNotFound
	}
	if err != nil {
		return nil, domain.BackupFile{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.BackupFile{}, domain.ErrNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, domain.BackupFile{}, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, domain.BackupFile{}, domain.ErrNotFound
	}
	return file, domain.BackupFile{Name: name, SizeBytes: opened.Size(), CreatedAt: at}, nil
}

// Space reports free and total bytes of the directory's filesystem.
func (d *Directory) Space() (free, total uint64, err error) { return filesystemSpace(d.path) }

// PublishFile links temp to an exact target path without replacing an existing
// file and flushes the target directory. CLI backups use it for --output.
func PublishFile(temp, target string) (domain.BackupFile, error) {
	if filepath.Dir(temp) != filepath.Dir(target) {
		return domain.BackupFile{}, domain.ErrInvalid
	}
	return publish(temp, target)
}

// TempPathFor returns a temporary path beside target.
func TempPathFor(target string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(target), ".codex-lb-"+filepath.Base(target)+".tmp-"+hex.EncodeToString(suffix[:])), nil
}

// FilesystemSpace reports free and total bytes of the filesystem holding path.
func FilesystemSpace(path string) (free, total uint64, err error) { return filesystemSpace(path) }

func publish(temp, target string) (domain.BackupFile, error) {
	// link(2) fails with EEXIST instead of replacing, unlike rename(2).
	if err := os.Link(temp, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return domain.BackupFile{}, fmt.Errorf("backup %s already exists: %w", filepath.Base(target), domain.ErrConflict)
		}
		return domain.BackupFile{}, err
	}
	if err := os.Remove(temp); err != nil {
		return domain.BackupFile{}, err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return domain.BackupFile{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return domain.BackupFile{}, err
	}
	at, ok := createdAt(filepath.Base(target))
	if !ok {
		at = info.ModTime().UTC()
	}
	return domain.BackupFile{Name: filepath.Base(target), SizeBytes: info.Size(), CreatedAt: at}, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
