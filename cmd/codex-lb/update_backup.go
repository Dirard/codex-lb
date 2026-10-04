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

// Backup holds the existing database lock after the old worker has stopped.
// VACUUM INTO includes a remaining WAL without copying a live database file.
func (h *updateHost) Backup(ctx context.Context) error {
	if h.current() != nil {
		return errors.New("cannot back up an active runtime")
	}
	dbPath := filepath.Join(h.dataDir, "codex-lb.sqlite3")
	if info, err := os.Lstat(dbPath); err != nil || !info.Mode().IsRegular() {
		return errors.New("runtime database is missing or unsafe")
	}
	lock, created, err := lockDatabase(dbPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if created {
		return errors.New("runtime database is missing")
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
