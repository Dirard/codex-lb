package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// LegacySnapshotSummary describes source rows that a future explicit importer
// must preserve. This inspection never writes to the source or destination.
type LegacySnapshotSummary struct {
	Accounts              int64
	ModelSources          int64
	Groups                int64
	Keys                  int64
	Reservations          int64
	UnsettledReservations int64
	RequestLogs           int64
	AccountUsageRollups   int64
	APIKeyUsageRollups    int64
	ActiveProxyBindings   int64
	GlobalProxyRouting    bool
}

// InspectLegacySnapshot reads a consistent SQLite snapshot in read-only mode.
// verifyCiphertext should decrypt with the legacy key and return an error on
// failure; no plaintext is retained or returned.
func InspectLegacySnapshot(ctx context.Context, path string, verifyCiphertext func([]byte) error) (LegacySnapshotSummary, error) {
	var summary LegacySnapshotSummary
	if path == "" || verifyCiphertext == nil {
		return summary, ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return summary, err
	}
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return summary, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return summary, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, err
	}
	defer tx.Rollback()
	tables := []struct {
		name string
		dest *int64
	}{
		{"accounts", &summary.Accounts}, {"model_sources", &summary.ModelSources},
		{"account_groups", &summary.Groups}, {"api_keys", &summary.Keys},
		{"api_key_usage_reservations", &summary.Reservations},
		{"request_logs", &summary.RequestLogs},
		{"account_usage_rollups", &summary.AccountUsageRollups},
		{"api_key_usage_rollups", &summary.APIKeyUsageRollups},
	}
	for _, table := range tables {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table.name).Scan(&exists); err != nil {
			return summary, err
		}
		if exists == 0 {
			return summary, fmt.Errorf("legacy snapshot missing %s: %w", table.name, ErrInvalid)
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table.name).Scan(table.dest); err != nil {
			return summary, err
		}
	}
	var password sql.NullString
	var totp []byte
	if err := tx.QueryRowContext(ctx, `SELECT password_hash,totp_secret_encrypted
 FROM dashboard_settings WHERE id=1`).Scan(&password, &totp); err != nil {
		return summary, fmt.Errorf("legacy dashboard settings: %w", err)
	}
	if password.Valid && password.String != "" {
		if _, err := bcrypt.Cost([]byte(password.String)); err != nil {
			return summary, fmt.Errorf("invalid legacy admin password hash: %w", ErrInvalid)
		}
	}
	if len(totp) != 0 && (len(totp) > 2<<20 || verifyCiphertext(totp) != nil) {
		return summary, fmt.Errorf("cannot verify legacy TOTP credential: %w", ErrInvalid)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_key_usage_reservations
 WHERE status IN ('reserved','settling')`).Scan(&summary.UnsettledReservations); err != nil {
		return summary, err
	}
	var bindingsTable int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
 WHERE type='table' AND name='account_proxy_bindings'`).Scan(&bindingsTable); err != nil {
		return summary, err
	}
	if bindingsTable != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_proxy_bindings WHERE is_active=1`).
			Scan(&summary.ActiveProxyBindings); err != nil {
			return summary, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT upstream_proxy_routing_enabled
 FROM dashboard_settings WHERE id=1`).Scan(&summary.GlobalProxyRouting); err != nil {
		return summary, fmt.Errorf("legacy proxy routing settings: %w", err)
	}
	var hasDeletion, hasStatus, hasReason bool
	if err := tx.QueryRowContext(ctx, `SELECT max(name='delete_requested_at'),max(name='status'),max(name='deactivation_reason')
 FROM pragma_table_info('accounts')`).Scan(&hasDeletion, &hasStatus, &hasReason); err != nil {
		return summary, err
	}
	liveAccounts := "1=1"
	if hasDeletion {
		liveAccounts += " AND delete_requested_at IS NULL"
	}
	if hasStatus && hasReason {
		liveAccounts += " AND NOT(status='deactivated' AND coalesce(deactivation_reason,'')='deleted')"
	}
	// Pending deletions have intentionally erased tokens; they are not logins.
	rows, err := tx.QueryContext(ctx, `SELECT id,access_token_encrypted,refresh_token_encrypted,
 id_token_encrypted FROM accounts WHERE `+liveAccounts+` ORDER BY id`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var id string
		var access, refresh, identity []byte
		if err := rows.Scan(&id, &access, &refresh, &identity); err != nil {
			rows.Close()
			return summary, err
		}
		for _, token := range [][]byte{access, refresh, identity} {
			if len(token) == 0 || len(token) > 2<<20 || verifyCiphertext(token) != nil {
				rows.Close()
				return summary, fmt.Errorf("cannot verify legacy account %s credential: %w", id, ErrInvalid)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return summary, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,api_key_encrypted FROM model_sources ORDER BY id`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var id string
		var token []byte
		if err := rows.Scan(&id, &token); err != nil {
			rows.Close()
			return summary, err
		}
		if len(token) != 0 && (len(token) > 2<<20 || verifyCiphertext(token) != nil) {
			rows.Close()
			return summary, fmt.Errorf("cannot verify legacy model source %s credential: %w", id, ErrInvalid)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return summary, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,key_hash FROM api_keys ORDER BY id`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return summary, err
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			rows.Close()
			return summary, fmt.Errorf("invalid legacy API key hash for %s: %w", id, ErrInvalid)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return summary, err
	}
	return summary, tx.Commit()
}
