package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
}

// analyticsPoolSize bounds dashboard reports and request-log listing on their
// own read pool, so long analytics cannot take the proxy's read connections.
const analyticsPoolSize = 3

var memoryDBSequence atomic.Int64

var migrations = []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7, schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14, schemaV15, schemaV16, schemaV17, schemaV18, schemaV19, schemaV20, schemaV21, schemaV22, schemaV23, schemaV24, schemaV25, schemaV26, schemaV27, schemaV28, schemaV29, schemaV30, schemaV31, schemaV32, schemaV33, schemaV34, schemaV35}

type Store struct {
	db                  *sql.DB
	readDB              *sql.DB
	analyticsDB         *sql.DB
	writer              *batchWriter
	accountAdmissionEnv accountAdmissionEnvironment
	affinityStartupTTL  int
	// background owns store-internal recovery work (interrupted repricing);
	// Close cancels and joins it before the writer stops.
	background     sync.WaitGroup
	stopBackground context.CancelFunc
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
		path = (&url.URL{Scheme: "file", Path: path}).String()
	}
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
	store := &Store{db: db, accountAdmissionEnv: accountEnv, affinityStartupTTL: affinityTTL}
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
	store.writer = newBatchWriter(db)
	background, stop := context.WithCancel(context.Background())
	store.stopBackground = stop
	if err := store.resumeModelReprices(background); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	s.stopBackground()
	s.background.Wait()
	s.writer.close()
	return errors.Join(s.analyticsDB.Close(), s.readDB.Close(), s.db.Close())
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
	values.Set("_synchronous", "2")
	if readonly {
		values.Set("_query_only", "1")
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
	if version < 0 || (version == 0 && tableCount != 0) || (version != 0 && appID != applicationID) || version > len(migrations) {
		return fmt.Errorf("refusing to modify foreign or unsupported SQLite schema (version=%d, application_id=%d): %w", version, appID, ErrInvalid)
	}
	if version == len(migrations) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
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
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

func transact(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
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
