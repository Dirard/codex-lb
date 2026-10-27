package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"codex-lb/internal/adapters/backups"
	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// backupDatabase is the CLI backup workflow for cron: it reads the database
// through SQLite's own locking (never the service's inode lock), so it works
// while the service runs; it never migrates or writes the source and never
// replaces an existing file. The encryption key is only used for verification.
func backupDatabase(ctx context.Context, cfg config, output io.Writer) error {
	source := filepath.Join(cfg.dataDir, "codex-lb.sqlite3")
	if info, err := os.Lstat(source); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("backup requires an existing private Go database (mode 0600)")
	}
	vault, err := credentials.Open(filepath.Join(cfg.dataDir, "encryption.key"), false)
	if err != nil {
		return errors.New("cannot open private encryption.key of this data directory")
	}
	target := cfg.output
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, backups.Name(time.Now()))
	} else if err == nil {
		return errors.New("backup output already exists; choose a new file or a directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect backup output")
	}
	estimate, err := sqlite.EstimateSnapshotFileBytes(ctx, source)
	if err != nil {
		return errors.New("cannot read the Go database; is it a codex-lb database?")
	}
	if free, total, err := backups.FilesystemSpace(filepath.Dir(target)); err != nil {
		return errors.New("cannot determine free space of the backup output directory")
	} else if !application.BackupSpaceSufficient(estimate, free, total) {
		return errors.New("not enough free disk space for the backup")
	}
	temp, err := backups.TempPathFor(target)
	if err != nil {
		return err
	}
	if err := sqlite.WriteSnapshot(ctx, source, temp, vault); err != nil {
		return errors.New("backup snapshot or verification failed; nothing was written")
	}
	file, err := backups.PublishFile(temp, target)
	if err != nil {
		_ = os.Remove(temp)
		if errors.Is(err, domain.ErrConflict) {
			return errors.New("backup output already exists; nothing was replaced")
		}
		return errors.New("cannot publish the verified backup")
	}
	return json.NewEncoder(output).Encode(struct {
		Path                  string    `json:"path"`
		SizeBytes             int64     `json:"sizeBytes"`
		CreatedAt             time.Time `json:"createdAt"`
		EncryptionKeyIncluded bool      `json:"encryptionKeyIncluded"`
	}{target, file.SizeBytes, file.CreatedAt, false})
}
