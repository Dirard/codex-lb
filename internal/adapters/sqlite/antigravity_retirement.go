package sqlite

import (
	"context"
	"database/sql"
)

// Antigravity sources (go-v1.17.0 – go-v1.19.0) were withdrawn in go-v1.20.0.
// Their schema stays, append-only: schemaV53 added the client version
// setting and schemaV54 corrected its seed. Nothing reads that column now.
const schemaV53 = `
ALTER TABLE runtime_settings ADD COLUMN antigravity_client_version TEXT NOT NULL DEFAULT '2.34.0' CHECK(length(antigravity_client_version) BETWEEN 5 AND 20);
`

const schemaV54 = `
UPDATE runtime_settings SET antigravity_client_version='2.33.0' WHERE antigravity_client_version='2.34.0';
`

// schemaV55 retires every Antigravity source: the source is deleted like an
// operator deletion, and retireAntigravitySourcesTx then deletes its account
// with its credential (the Google refresh token), scopes and owners. Usage
// history stays. Additive: a handful of rows, and the previous release reads
// a deleted source as deleted.
const schemaV55 = `
UPDATE legacy_model_sources SET is_enabled=0,
 deleted_at=CAST(strftime('%s','now') AS INTEGER)*1000, updated_at=CAST(strftime('%s','now') AS INTEGER)*1000
 WHERE kind='antigravity' AND deleted_at IS NULL;
`

// retireAntigravitySourcesTx completes schemaV55 through the same boundary as
// an account deletion, for every Antigravity account not deleted yet.
func retireAntigravitySourcesTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM accounts WHERE kind='external' AND provider='antigravity'
 AND NOT (status='deactivated' AND deactivation_reason='deleted')`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := markAccountDeletedTx(ctx, tx, id, false); err != nil {
			return err
		}
	}
	// Sources deleted before this release kept their accounts deleted but may
	// still hold a credential row from an older deletion path.
	_, err = tx.ExecContext(ctx, `DELETE FROM account_credentials WHERE account_id IN
 (SELECT id FROM accounts WHERE kind='external' AND provider='antigravity')`)
	return err
}
