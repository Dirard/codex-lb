package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"modernc.org/sqlite"
)

var (
	ErrNotFound     = domain.ErrNotFound
	ErrConflict     = domain.ErrConflict
	ErrInvalid      = domain.ErrInvalid
	ErrLimitReached = domain.ErrLimitReached
	ErrNoAccounts   = domain.ErrNoAccounts
)

const applicationID = 0x434c4247 // CLBG
// defaultReadConnections is used when the caller does not size the pool from the host.
const defaultReadConnections = 8

// Options sizes the store for the host it runs on; zero values use defaults.
type Options struct {
	ReadConnections int
	// InstanceID identifies this process among the workers of one installation
	// that share the database; new reservations record it. Empty generates a
	// random id at open.
	InstanceID string
}

// analyticsPoolSize bounds dashboard reports and request-log listing on their
// own read pool, so long analytics cannot take the proxy's read connections.
const analyticsPoolSize = 3

const walSizeLimit = 64 << 20

var memoryDBSequence atomic.Int64

var migrations = []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7, schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14, schemaV15, schemaV16, schemaV17, schemaV18, schemaV19, schemaV20, schemaV21, schemaV22, schemaV23, schemaV24, schemaV25, schemaV26, schemaV27, schemaV28, schemaV29, schemaV30, schemaV31, schemaV32, schemaV33, schemaV34, schemaV35, schemaV36, schemaV37, schemaV38, schemaV39, schemaV40, schemaV41, schemaV42}

// additiveMigrations are the migrations (by resulting version) that code of
// the previous schema keeps working on: they only add defaulted columns and
// tables older code never needs. Migrations 1-40 predate the classification
// and count as non-additive; see SchemaFloor.
// An additive migration runs while the previous version still serves, whose
// writes wait for the lock only within their own deadlines (settlement 10 s),
// so it must finish in a few seconds: no table rewrites or large backfills.
var additiveMigrations = map[int]bool{41: true, 42: true}

type Store struct {
	db                  *sql.DB
	filePath            string // absolute database file; empty for in-memory stores
	maintenance         *sql.DB
	readDB              *sql.DB
	analyticsDB         *sql.DB
	writer              *batchWriter
	accountAdmissionEnv accountAdmissionEnvironment
	affinityStartupTTL  int
	instanceID          string
}

// Open accepts a new Go database or one previously created by this adapter.
// A legacy/foreign SQLite file is rejected before any schema writes.
func Open(path string) (*Store, error) { return OpenWithOptions(path, Options{}) }

// OpenWithOptions is Open with an explicitly sized reader pool.
func OpenWithOptions(path string, options Options) (*Store, error) {
	readPoolSize := options.ReadConnections
	if readPoolSize <= 0 {
		readPoolSize = defaultReadConnections
	}
	if path == "" {
		return nil, fmt.Errorf("sqlite path: %w", ErrInvalid)
	}
	instanceID := options.InstanceID
	if instanceID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		instanceID = hex.EncodeToString(raw[:])
	}
	accountEnv, err := loadAccountAdmissionEnvironment()
	if err != nil {
		return nil, err
	}
	affinityTTL, err := loadAffinityEnvironment()
	if err != nil {
		return nil, err
	}
	if path == ":memory:" || path == "file::memory:" {
		path = fmt.Sprintf("file:codex-lb-memory-%d?mode=memory&cache=shared", memoryDBSequence.Add(1))
	}
	if !strings.HasPrefix(path, "file:") {
		// A relative path would become a URI authority ("file://rel"), which
		// SQLite rejects; resolve it against the working directory instead.
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("sqlite path: %w", ErrInvalid)
		}
		path = (&url.URL{Scheme: "file", Path: absolute}).String()
	}
	filePath := databaseFilePath(path)
	writerDSN, err := sqliteDSN(path, false)
	if err != nil {
		return nil, err
	}
	writerConnector, err := sqlite.NewConnector(writerDSN)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(writerConnector)
	// WAL readers use a separate pool; every durable mutation still uses one writer.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	store := &Store{db: db, filePath: filePath, accountAdmissionEnv: accountEnv, affinityStartupTTL: affinityTTL, instanceID: instanceID}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Configure WAL only after schema ownership validation; a foreign database
	// must not be altered even by a journal-mode change. FULL keeps committed
	// accounting durable without recreating the rollback journal on every write.
	var journal string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		_ = db.Close()
		return nil, err
	}
	if journal != "wal" && journal != "memory" {
		_ = db.Close()
		return nil, fmt.Errorf("SQLite WAL is unavailable: %w", ErrInvalid)
	}

	readerDSN, err := sqliteDSN(path, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	readerConnector, err := sqlite.NewConnector(readerDSN)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	readDB := sql.OpenDB(readerConnector)
	readDB.SetMaxOpenConns(readPoolSize)
	readDB.SetMaxIdleConns(readPoolSize)
	readDB.SetConnMaxLifetime(0)
	store.readDB = readDB
	if err := readDB.PingContext(context.Background()); err != nil {
		_ = errors.Join(readDB.Close(), db.Close())
		return nil, err
	}
	analyticsDB := sql.OpenDB(readerConnector)
	analyticsDB.SetMaxOpenConns(analyticsPoolSize)
	analyticsDB.SetMaxIdleConns(analyticsPoolSize)
	analyticsDB.SetConnMaxLifetime(0)
	store.analyticsDB = analyticsDB
	if err := analyticsDB.PingContext(context.Background()); err != nil {
		_ = errors.Join(analyticsDB.Close(), readDB.Close(), db.Close())
		return nil, err
	}
	maintenanceDSN, err := maintenanceDSN(writerDSN)
	if err != nil {
		_ = errors.Join(analyticsDB.Close(), readDB.Close(), db.Close())
		return nil, err
	}
	maintenanceConnector, err := sqlite.NewConnector(maintenanceDSN)
	if err != nil {
		_ = errors.Join(analyticsDB.Close(), readDB.Close(), db.Close())
		return nil, err
	}
	// Checkpoints run outside the writer connection: PASSIVE never blocks it and
	// TRUNCATE gives up after the short busy timeout.
	store.maintenance = sql.OpenDB(maintenanceConnector)
	store.maintenance.SetMaxOpenConns(1)
	store.maintenance.SetConnMaxLifetime(0)
	store.writer = newBatchWriter(db)
	return store, nil
}

// InstanceID is the id this store records as the owner of new reservations.
func (s *Store) InstanceID() string { return s.instanceID }

// Close finishes accepted writes, refreshes planner statistics and closes every
// connection; an optimize failure does not prevent closing. Callers join their
// own store work (such as ResumeModelReprices) first.
func (s *Store) Close() error {
	s.writer.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = retryBusy(writeLockWait, func() bool { return ctx.Err() == nil }, func() error {
		_, err := s.db.ExecContext(ctx, "PRAGMA optimize")
		return err
	})
	cancel()
	return errors.Join(s.maintenance.Close(), s.analyticsDB.Close(), s.readDB.Close(), s.db.Close())
}

func maintenanceDSN(writerDSN string) (string, error) {
	base, query, _ := strings.Cut(writerDSN, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	values.Set("_busy_timeout", "1000")
	return base + "?" + values.Encode(), nil
}

// databaseFilePath returns the filesystem path of a file URI, or "" for an
// in-memory database.
func databaseFilePath(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" || parsed.Query().Get("mode") == "memory" || strings.HasPrefix(parsed.Opaque, ":memory:") {
		return ""
	}
	if parsed.Path != "" {
		return parsed.Path
	}
	return parsed.Opaque
}

// durableSynchronous is FULL: committed accounting must survive power loss.
const durableSynchronous = "2"

// testBinaryNoSync lets test binaries skip fsync. Tests verify logic, and a
// killed process loses nothing the OS page cache still holds; the durability
// tests opt back in. Programs built with go build always use FULL.
var testBinaryNoSync = testing.Testing()

func synchronousLevel() string {
	if testBinaryNoSync {
		return "0"
	}
	return durableSynchronous
}

func sqliteDSN(path string, readonly bool) (string, error) {
	base, query := path, ""
	if len(path) > 1 {
		if pos := strings.IndexByte(path[1:], '?'); pos >= 0 {
			base, query = path[:pos+1], path[pos+2:]
		}
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("invalid SQLite URI query: %w", err)
	}
	for key := range values {
		if strings.HasPrefix(key, "_") {
			return "", fmt.Errorf("SQLite driver parameters are not accepted in path: %w", ErrInvalid)
		}
	}
	values.Set("_busy_timeout", "5000")
	values.Set("_foreign_keys", "1")
	values.Set("_synchronous", synchronousLevel())
	if readonly {
		values.Set("_query_only", "1")
	} else {
		// Another worker of the installation may write the same file during an
		// update. A deferred read-then-write transaction would then fail with
		// SQLITE_BUSY_SNAPSHOT; IMMEDIATE takes the write lock at BEGIN instead.
		values.Set("_txlock", "immediate")
		// Writes wait for that lock in retryBusy, not in SQLite's busy handler
		// (see writeLockWait). Inside an IMMEDIATE WAL transaction nothing else
		// waits for a lock (a new file migrates before WAL, with no other
		// connection open), so every write on this pool begins through the
		// batch writer or beginWrite.
		values.Set("_busy_timeout", "0")
		// A checkpoint that resets the WAL truncates it to this size, so one
		// large transaction or a long reader cannot grow the file forever.
		values.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", walSizeLimit))
		// PRAGMA optimize then samples at most this many rows per index.
		values.Add("_pragma", "analysis_limit(400)")
	}
	if base == "file::memory:" {
		base = "file::memory:"
		values.Set("cache", "shared")
	}
	return base + "?" + values.Encode(), nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version, appID, tableCount int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tableCount); err != nil {
		return err
	}
	if version > len(migrations) && appID == applicationID {
		// A newer release migrated this database; operate on it unchanged when
		// every later migration is one this code keeps working on.
		compatible, err := newerSchemaCompatible(ctx, s.db)
		if err != nil {
			return err
		}
		if compatible {
			return nil
		}
	}
	if version < 0 || (version == 0 && tableCount != 0) || (version != 0 && appID != applicationID) || version > len(migrations) {
		return fmt.Errorf("refusing to modify foreign or unsupported SQLite schema (version=%d, application_id=%d): %w", version, appID, ErrInvalid)
	}
	if version == len(migrations) {
		return nil
	}
	// An additive migration runs while the previous worker still writes.
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for index := version; index < len(migrations); index++ {
		if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
			return fmt.Errorf("migrate schema to %d: %w", index+1, err)
		}
	}
	if version < 31 {
		if err := migrateDeletedAccountsTx(ctx, tx); err != nil {
			return fmt.Errorf("migrate deleted account access: %w", err)
		}
	}
	if version == 0 {
		env := s.accountAdmissionEnv
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_settings SET
 proxy_account_response_create_limit=?,proxy_account_stream_limit=?,
 proxy_account_stream_recovery_reserve=?,proxy_api_key_fair_share_congestion_threshold_pct=?,
 openai_cache_affinity_max_age_seconds=? WHERE id=1`,
			env.create, env.stream, env.reserve, env.fairShare, s.affinityStartupTTL); err != nil {
			return err
		}
	}
	// Older code reads the floor to decide whether it may open the result.
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_compatibility(id,reader_floor) VALUES(1,?)
 ON CONFLICT(id) DO UPDATE SET reader_floor=excluded.reader_floor`, SchemaFloor()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

// newerSchemaCompatible reports whether a database of this application at a
// newer schema records a floor this code satisfies. It only reads.
func newerSchemaCompatible(ctx context.Context, db *sql.DB) (bool, error) {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_compatibility'`).Scan(&tables); err != nil || tables == 0 {
		return false, err
	}
	var floor int
	err := db.QueryRowContext(ctx, `SELECT reader_floor FROM schema_compatibility WHERE id=1`).Scan(&floor)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && floor <= len(migrations), err
}

func millis(t time.Time) int64     { return t.UTC().UnixMilli() }
func fromMillis(v int64) time.Time { return time.UnixMilli(v).UTC() }
func optionalMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return millis(*t)
}
func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMillis(v.Int64)
	return &t
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
