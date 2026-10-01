package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type LegacyVault interface {
	Fingerprint() string
	Decrypt([]byte) ([]byte, error)
}

type LegacyImportReport struct {
	LegacySnapshotSummary
	SidecarPath         string
	SourceSHA256        string
	FoldedThrough       time.Time
	HourlyFoldedThrough time.Time
}

// ImportLegacySnapshot imports a checkpointed COPY of a legacy SQLite file.
// The source is opened immutable/read-only; an exact 0600 sidecar beside the
// new database retains historical fields not used by the Go runtime.
func (s *Store) ImportLegacySnapshot(ctx context.Context, sourcePath string, vault LegacyVault) (LegacyImportReport, error) {
	var report LegacyImportReport
	if vault == nil || vault.Fingerprint() == "" {
		return report, ErrInvalid
	}
	sourcePath, destPath, err := s.legacyImportPaths(ctx, sourcePath)
	if err != nil {
		return report, err
	}
	verify := func(cipher []byte) error { _, err := vault.Decrypt(cipher); return err }
	report.LegacySnapshotSummary, err = InspectLegacySnapshot(ctx, sourcePath, verify)
	if err != nil {
		return report, err
	}
	if report.UnsettledReservations > 0 {
		var settling int
		legacyDB, err := openImmutableLegacy(sourcePath)
		if err != nil {
			return report, err
		}
		err = legacyDB.QueryRowContext(ctx, "SELECT count(*) FROM api_key_usage_reservations WHERE status='settling'").Scan(&settling)
		_ = legacyDB.Close()
		if err != nil {
			return report, err
		}
		if settling != 0 {
			return report, fmt.Errorf("legacy settling reservations require reconciliation: %w", ErrInvalid)
		}
	}
	sourceHash, err := hashFile(sourcePath)
	if err != nil {
		return report, err
	}
	report.SourceSHA256 = sourceHash
	report.SidecarPath = destPath + ".legacy-source.sqlite"
	srcDB, err := openImmutableLegacy(sourcePath)
	if err != nil {
		return report, err
	}
	defer srcDB.Close()
	srcTx, err := srcDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer srcTx.Rollback()
	var fingerprint string
	if err := srcTx.QueryRowContext(ctx, `SELECT value FROM runtime_sentinels
 WHERE name='encryption_key_fingerprint'`).Scan(&fingerprint); err != nil {
		return report, fmt.Errorf("legacy encryption fingerprint sentinel: %w", err)
	}
	if fingerprint != vault.Fingerprint() {
		return report, fmt.Errorf("legacy encryption key fingerprint mismatch: %w", ErrInvalid)
	}
	var foldedRaw, hourlyRaw string
	if err := srcTx.QueryRowContext(ctx, `SELECT folded_through,hourly_folded_through
 FROM account_usage_rollup_state WHERE id=1`).Scan(&foldedRaw, &hourlyRaw); err != nil {
		return report, fmt.Errorf("legacy usage fold watermark: %w", err)
	}
	report.FoldedThrough, err = parseLegacyTime(foldedRaw)
	if err != nil {
		return report, err
	}
	report.HourlyFoldedThrough, err = parseLegacyTime(hourlyRaw)
	if err != nil {
		return report, err
	}
	dstTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer dstTx.Rollback()
	if err := ensureEmptyImportDestination(ctx, dstTx); err != nil {
		return report, err
	}
	for _, step := range []func(context.Context, *sql.Tx, *sql.Tx, LegacyVault) error{
		importLegacyAccounts, importLegacyModelSources, importLegacySettings,
		importLegacyGroups, importLegacyKeys, importLegacyLimits, importLegacyReservations, importLegacyFirewall,
		importLegacyAutomations,
	} {
		if err := step(ctx, srcTx, dstTx, vault); err != nil {
			return report, err
		}
	}
	if err := importLegacyQuota(ctx, srcTx, dstTx); err != nil {
		return report, err
	}
	if err := importLegacyQuotaMetadata(ctx, srcTx, dstTx); err != nil {
		return report, err
	}
	if err := importLegacyWarmupAttempts(ctx, srcTx, dstTx); err != nil {
		return report, err
	}
	if err := importLegacyCapabilityLineage(ctx, srcTx, dstTx); err != nil {
		return report, err
	}
	if err := importLegacyUsage(ctx, srcTx, dstTx, foldedRaw, hourlyRaw, report.HourlyFoldedThrough); err != nil {
		return report, err
	}
	if err := migrateDeletedAccountsTx(ctx, dstTx); err != nil {
		return report, err
	}
	if _, err := dstTx.ExecContext(ctx, `INSERT INTO legacy_import_state
 (id,source_sha256,key_fingerprint,folded_through,hourly_folded_through,imported_at,sidecar_path)
 VALUES(1,?,?,?,?,?,?)`, sourceHash, fingerprint, millis(report.FoldedThrough),
		millis(report.HourlyFoldedThrough), time.Now().UTC().UnixMilli(), report.SidecarPath); err != nil {
		return report, err
	}
	if err := srcTx.Commit(); err != nil {
		return report, err
	}
	if err := preserveLegacySidecar(sourcePath, report.SidecarPath, sourceHash); err != nil {
		return report, err
	}
	if err := dstTx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) legacyImportPaths(ctx context.Context, source string) (string, string, error) {
	if source == "" {
		return "", "", ErrInvalid
	}
	var seq int
	var name, dest string
	rows, err := s.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&seq, &name, &dest); err != nil {
			return "", "", err
		}
		if name == "main" {
			break
		}
	}
	if dest == "" {
		return "", "", fmt.Errorf("legacy import requires file-backed destination: %w", ErrInvalid)
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return "", "", err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", "", err
	}
	if source == dest || source == dest+".legacy-source.sqlite" {
		return "", "", fmt.Errorf("source and destination overlap: %w", ErrInvalid)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("source must be a regular snapshot copy: %w", ErrInvalid)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(source + suffix); err == nil {
			return "", "", fmt.Errorf("source has SQLite sidecar %s; checkpoint the snapshot copy: %w", suffix, ErrInvalid)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
	}
	return source, dest, nil
}

func openImmutableLegacy(path string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ensureEmptyImportDestination(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"accounts", "account_groups", "api_keys", "usage_events", "usage_totals", "admin_secret", "legacy_import_state"} {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("destination already contains %s: %w", table, ErrConflict)
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func preserveLegacySidecar(source, target, expectedHash string) error {
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("existing legacy sidecar is not a private regular file: %w", ErrInvalid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existing, err := hashFile(target); err == nil {
		if existing == expectedHash {
			return nil
		}
		return fmt.Errorf("legacy sidecar already exists with different contents: %w", ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".legacy-import-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	var h hash.Hash = sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), in)
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	copyErr = errors.Join(copyErr, tmp.Close())
	if copyErr != nil {
		return copyErr
	}
	if hex.EncodeToString(h.Sum(nil)) != expectedHash {
		return fmt.Errorf("legacy snapshot changed during import: %w", ErrInvalid)
	}
	if err := os.Link(tmp.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, hashErr := hashFile(target)
			if hashErr == nil && existing == expectedHash {
				return nil
			}
		}
		return err
	}
	return nil
}

func parseLegacyTime(value any) (time.Time, error) {
	if t, ok := value.(time.Time); ok {
		return t.UTC(), nil
	}
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return time.Time{}, fmt.Errorf("legacy timestamp type %T: %w", value, ErrInvalid)
	}
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid legacy timestamp: %w", ErrInvalid)
}
