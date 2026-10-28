package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"codex-lb/internal/application"
)

const runtimeProbe = "codex-lb:runtime-key:v1"

type runtimeVault interface {
	application.SecretCipher
	Fingerprint() string
}

// ValidateRuntime fails closed before readiness. In particular, a missing or
// replaced key never silently turns an existing installation into an empty one.
func (s *Store) ValidateRuntime(ctx context.Context, vault runtimeVault) error {
	if vault == nil || vault.Fingerprint() == "" {
		return errors.New("runtime encryption key is required")
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		fingerprint, err := checkRuntimeIdentity(ctx, tx, vault)
		if err != nil || fingerprint != "" {
			return err
		}
		probe, err := vault.Encrypt([]byte(runtimeProbe))
		if err != nil {
			return errors.New("could not validate runtime encryption")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO runtime_identity(id,key_fingerprint,encryption_probe) VALUES(1,?,?)", vault.Fingerprint(), probe)
		return err
	})
}

type identityQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// checkRuntimeIdentity proves without writing that vault is the key of this
// database: the stored probe and every stored credential must decrypt. It
// returns the stored fingerprint, empty when the identity was never recorded.
func checkRuntimeIdentity(ctx context.Context, q identityQuerier, vault runtimeVault) (string, error) {
	if vault == nil || vault.Fingerprint() == "" {
		return "", errors.New("runtime encryption key is required")
	}
	var fingerprint string
	var probe []byte
	err := q.QueryRowContext(ctx, "SELECT key_fingerprint,encryption_probe FROM runtime_identity WHERE id=1").Scan(&fingerprint, &probe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil {
		plain, decryptErr := vault.Decrypt(probe)
		if fingerprint != vault.Fingerprint() || decryptErr != nil || string(plain) != runtimeProbe {
			return "", errors.New("runtime encryption key does not match this database")
		}
	}
	var imported string
	if err := q.QueryRowContext(ctx, "SELECT key_fingerprint FROM legacy_import_state WHERE id=1").Scan(&imported); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if imported != "" && imported != vault.Fingerprint() {
		return "", errors.New("encryption key does not match the imported database")
	}
	rows, err := q.QueryContext(ctx, `SELECT access_token_encrypted FROM account_credentials
 UNION ALL SELECT refresh_token_encrypted FROM account_credentials
 UNION ALL SELECT id_token_encrypted FROM account_credentials
 UNION ALL SELECT external_key_encrypted FROM account_credentials
 UNION ALL SELECT totp_secret_encrypted FROM admin_secret`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var encrypted []byte
		if err := rows.Scan(&encrypted); err != nil {
			return "", err
		}
		if len(encrypted) != 0 {
			if _, err := vault.Decrypt(encrypted); err != nil {
				return "", errors.New("stored credential failed encryption validation")
			}
		}
	}
	return fingerprint, rows.Err()
}

func (s *Store) Health(ctx context.Context) error { return s.db.PingContext(ctx) }
