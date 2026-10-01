package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
)

type persistentData struct {
	store *sqlite.Store
	vault *credentials.Vault
	lock  *os.File
}

func openData(ctx context.Context, directory string) (*persistentData, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, errors.New("cannot create data directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("data directory must be a private directory (mode 0700), not a symlink")
	}
	path := filepath.Join(directory, "codex-lb.sqlite3")
	file, newStore, err := lockDatabase(path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	vault, err := credentials.Open(filepath.Join(directory, "encryption.key"), newStore)
	if err != nil {
		return nil, errors.New("cannot open private encryption.key; do not replace the key of an existing database")
	}
	store, err := sqlite.Open((&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		return nil, errors.New("cannot open or migrate Go database; foreign/legacy databases must be imported into a separate directory")
	}
	if err := store.ValidateRuntime(ctx, vault); err != nil {
		store.Close()
		return nil, errors.New("database encryption validation failed; restore the matching key and database")
	}
	ok = true
	return &persistentData{store: store, vault: vault, lock: file}, nil
}

func (d *persistentData) close() error {
	// Lock remains held until every SQLite handle is closed.
	return errors.Join(d.store.Close(), d.lock.Close())
}
