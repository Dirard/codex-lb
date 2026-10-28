package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"codex-lb/internal/domain"
	"modernc.org/sqlite"
)

// Snapshotter writes verified online snapshots of a store's database file.
// It holds the installation key only to verify snapshots; the key is never
// written next to them.
type Snapshotter struct {
	store *Store
	vault runtimeVault
}

func NewSnapshotter(store *Store, vault runtimeVault) *Snapshotter {
	return &Snapshotter{store: store, vault: vault}
}

// SnapshotDatabase writes a verified snapshot of the live database to target.
func (s *Snapshotter) SnapshotDatabase(ctx context.Context, target string) error {
	if s.store.filePath == "" {
		return fmt.Errorf("in-memory database cannot be backed up: %w", ErrInvalid)
	}
	return WriteSnapshot(ctx, s.store.filePath, target, s.vault)
}

// EstimateSnapshotBytes is the size of the used pages, which bounds the size of
// a VACUUM INTO snapshot.
func (s *Snapshotter) EstimateSnapshotBytes(ctx context.Context) (int64, error) {
	return estimateSnapshotBytes(ctx, s.store.readDB)
}

// StorageUsage reports the live database and WAL file sizes.
func (s *Snapshotter) StorageUsage(context.Context) (domain.StorageUsage, error) {
	var usage domain.StorageUsage
	if s.store.filePath == "" {
		return usage, nil
	}
	for _, item := range []struct {
		suffix string
		size   *int64
	}{{"", &usage.DatabaseBytes}, {"-wal", &usage.WALBytes}} {
		info, err := os.Stat(s.store.filePath + item.suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return usage, err
		}
		*item.size = info.Size()
	}
	return usage, nil
}

type pragmaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func estimateSnapshotBytes(ctx context.Context, db pragmaQuerier) (int64, error) {
	var pages, free, size int64
	if err := db.QueryRowContext(ctx, "SELECT page_count,freelist_count,page_size FROM pragma_page_count(),pragma_freelist_count(),pragma_page_size()").Scan(&pages, &free, &size); err != nil {
		return 0, err
	}
	return max(0, pages-free) * size, nil
}

// EstimateSnapshotFileBytes estimates a snapshot of the database file at path
// through a read-only connection (CLI backups run beside the service).
func EstimateSnapshotFileBytes(ctx context.Context, path string) (int64, error) {
	db, err := openReadOnlySource(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return estimateSnapshotBytes(ctx, db)
}

// WriteSnapshot is the backup workflow step "VACUUM INTO a temporary file
// through a dedicated read-only connection --> fsync and verify". The source is
// opened read-only, so the snapshot cannot alter it, and SQLite's own locking
// gives a consistent WAL snapshot while the service keeps writing. target must
// not exist; on any failure it is removed.
func WriteSnapshot(ctx context.Context, source, target string, vault runtimeVault) (err error) {
	if source == "" || target == "" || vault == nil {
		return ErrInvalid
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("snapshot target exists: %w", ErrConflict)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(target)
			_ = os.Remove(target + "-journal")
		}
	}()
	db, err := openReadOnlySource(source)
	if err != nil {
		return err
	}
	if err := checkSnapshotSchema(ctx, db); err != nil {
		_ = db.Close()
		return err
	}
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", target)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return err
	}
	if err := syncFile(target); err != nil {
		return err
	}
	return VerifySnapshot(ctx, target, vault)
}

// VerifySnapshot proves that a snapshot file is intact, belongs to this
// application at a supported schema version, and is readable with vault.
func VerifySnapshot(ctx context.Context, path string, vault runtimeVault) error {
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	connector, err := sqlite.NewConnector(uri)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("snapshot integrity check: %w", err)
	}
	if check != "ok" {
		return errors.New("snapshot failed integrity check")
	}
	if err := checkSnapshotSchema(ctx, db); err != nil {
		return err
	}
	if _, err := checkRuntimeIdentity(ctx, db, vault); err != nil {
		return fmt.Errorf("snapshot key verification: %w", err)
	}
	return nil
}

func checkSnapshotSchema(ctx context.Context, db *sql.DB) error {
	var appID, version int
	if err := db.QueryRowContext(ctx, "SELECT application_id,user_version FROM pragma_application_id(),pragma_user_version()").Scan(&appID, &version); err != nil {
		return err
	}
	compatible := appID == applicationID && version >= 1 && version <= len(migrations)
	if appID == applicationID && version > len(migrations) {
		// A newer schema this code may operate on is also one it may back up.
		var err error
		if compatible, err = newerSchemaCompatible(ctx, db); err != nil {
			return err
		}
	}
	if !compatible {
		return fmt.Errorf("database is not a supported codex-lb schema (version=%d): %w", version, ErrInvalid)
	}
	return nil
}

func openReadOnlySource(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database file is missing or not a regular file: %w", ErrInvalid)
	}
	values := url.Values{}
	values.Set("mode", "ro")
	values.Set("_busy_timeout", "5000")
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
	connector, err := sqlite.NewConnector(uri)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db, nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

// LoadBackupSchedule returns the persisted backup schedule.
func (s *Store) LoadBackupSchedule(ctx context.Context) (domain.BackupSchedule, error) {
	var schedule domain.BackupSchedule
	err := s.readDB.QueryRowContext(ctx, "SELECT interval_hours,keep_count FROM backup_settings WHERE id=1").Scan(&schedule.IntervalHours, &schedule.Keep)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupSchedule{IntervalHours: domain.DefaultBackupIntervalHours, Keep: domain.DefaultBackupKeep}, nil
	}
	return schedule, err
}

// SaveBackupSchedule persists a validated schedule; it applies without restart.
func (s *Store) SaveBackupSchedule(ctx context.Context, schedule domain.BackupSchedule) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO backup_settings(id,interval_hours,keep_count) VALUES(1,?,?)
 ON CONFLICT(id) DO UPDATE SET interval_hours=excluded.interval_hours,keep_count=excluded.keep_count`,
			schedule.IntervalHours, schedule.Keep)
		return err
	})
}
