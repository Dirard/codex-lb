package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Backup snapshots the database under the installation's lock: online through
// a read-only connection while the host lends the lock to its workers, or
// after the old worker has stopped. VACUUM INTO includes a remaining WAL
// without copying a live database file.
func (h *updateHost) Backup(ctx context.Context) error {
	dbPath := filepath.Join(h.dataDir, "codex-lb.sqlite3")
	if info, err := os.Lstat(dbPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("runtime database is missing or unsafe")
	}
	if !h.ownsLock() {
		if h.current() != nil {
			return errors.New("cannot back up an active runtime")
		}
		lock, created, err := lockDatabase(dbPath)
		if err != nil {
			return err
		}
		defer lock.Close()
		if created {
			return errors.New("runtime database is missing")
		}
	}
	root := filepath.Join(h.dataDir, "updates")
	backup, err := os.MkdirTemp(root, "backup-")
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(backup)
		}
	}()
	database, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String())
	if err != nil {
		return err
	}
	defer database.Close()
	target := filepath.Join(backup, "codex-lb.sqlite3")
	if _, err = database.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return errors.New("cannot snapshot runtime database")
	}
	if err = os.Chmod(target, 0600); err != nil {
		return err
	}
	if err = syncUpdatePath(target); err != nil {
		return err
	}
	if err = copyPrivateKey(filepath.Join(h.dataDir, "encryption.key"), filepath.Join(backup, "encryption.key")); err != nil {
		return err
	}
	if err = syncUpdatePath(backup); err != nil {
		return err
	}
	marker := filepath.Join(root, "backup-current")
	old, _ := os.ReadFile(marker)
	file, err := os.CreateTemp(root, ".backup-pointer-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(filepath.Base(backup)); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), marker); err != nil {
		return err
	}
	if err = syncUpdatePath(root); err != nil {
		return err
	}
	success = true
	name := string(old)
	if filepath.Base(name) == name && strings.HasPrefix(name, "backup-") && name != filepath.Base(backup) {
		_ = os.RemoveAll(filepath.Join(root, name))
	}
	return nil
}

func syncUpdatePath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func copyPrivateKey(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return errors.New("unsafe runtime encryption key")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("runtime encryption key changed")
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, io.LimitReader(input, 4096))
	if err == nil {
		err = output.Sync()
	}
	return errors.Join(err, output.Close())
}

// RestoreBackup puts the snapshot recorded by the last Backup back in place of
// the database while no worker runs. A newer version that migrated the
// database and then failed leaves its WAL behind; it is removed first so it
// can never be replayed onto the restored file.
func (h *updateHost) RestoreBackup(ctx context.Context) error {
	if h.anyWorker() {
		return errors.New("cannot restore beneath an active runtime")
	}
	root := filepath.Join(h.dataDir, "updates")
	pointer, err := os.ReadFile(filepath.Join(root, "backup-current"))
	name := string(pointer)
	if err != nil || filepath.Base(name) != name || !strings.HasPrefix(name, "backup-") {
		return errors.New("pre-update backup is missing")
	}
	source := filepath.Join(root, name, "codex-lb.sqlite3")
	if info, err := os.Lstat(source); err != nil || !info.Mode().IsRegular() {
		return errors.New("pre-update backup is missing or unsafe")
	}
	dbPath := filepath.Join(h.dataDir, "codex-lb.sqlite3")
	if !h.ownsLock() {
		lock, created, err := lockDatabase(dbPath)
		if err != nil {
			return err
		}
		defer lock.Close()
		if created {
			return errors.New("runtime database is missing")
		}
	}
	staged, err := os.CreateTemp(h.dataDir, ".restore-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	input, err := os.Open(source)
	if err != nil {
		staged.Close()
		return err
	}
	_, err = io.Copy(staged, input)
	input.Close()
	if err == nil {
		err = staged.Sync()
	}
	if err = errors.Join(err, staged.Close(), ctx.Err()); err != nil {
		return errors.New("cannot stage the pre-update backup")
	}
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot discard the failed version's journal")
		}
	}
	if err := os.Rename(staged.Name(), dbPath); err != nil {
		return errors.New("cannot restore the pre-update backup")
	}
	if err := syncUpdatePath(h.dataDir); err != nil {
		return err
	}
	return h.relock(dbPath)
}

func (h *updateHost) ownsLock() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.lock != nil }

func (h *updateHost) anyWorker() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.child != nil || h.candidate != nil || len(h.retiring) != 0
}

// relock moves an owned lock to the restored inode: the old one was replaced,
// and workers verify that their inherited lock is the database file's.
func (h *updateHost) relock(dbPath string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil {
		return nil
	}
	lock, _, err := lockDatabase(dbPath)
	if err != nil {
		return err
	}
	h.lock.Close()
	h.lock = lock
	return nil
}
