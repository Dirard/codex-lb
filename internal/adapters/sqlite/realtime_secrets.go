package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"codex-lb/internal/domain"
)

// realtimeSecretPruneBatch bounds how many expired secrets one save deletes.
const realtimeSecretPruneBatch = 1000

// SaveRealtimeClientSecret stores a new client secret by its hash and deletes
// a bounded batch of expired ones in the same transaction.
func (s *Store) SaveRealtimeClientSecret(ctx context.Context, secret domain.RealtimeClientSecret) error {
	if len(secret.Hash) != 64 || secret.APIKeyID == "" || !secret.ExpiresAt.After(secret.CreatedAt) ||
		secret.SessionEncrypted != nil && !fernetCiphertext(secret.SessionEncrypted) {
		return fmt.Errorf("realtime client secret: %w", ErrInvalid)
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM realtime_client_secrets WHERE secret_hash IN
 (SELECT secret_hash FROM realtime_client_secrets WHERE expires_at<=? LIMIT ?)`, millis(secret.CreatedAt), realtimeSecretPruneBatch); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO realtime_client_secrets(secret_hash,api_key_id,session_encrypted,expires_at,created_at)
 VALUES(?,?,?,?,?)`, secret.Hash, secret.APIKeyID, nullBytes(secret.SessionEncrypted), millis(secret.ExpiresAt), millis(secret.CreatedAt))
		return err
	})
}

// RealtimeClientSecret returns the secret with this hash while it is valid.
func (s *Store) RealtimeClientSecret(ctx context.Context, hash string, now time.Time) (domain.RealtimeClientSecret, error) {
	secret := domain.RealtimeClientSecret{Hash: hash}
	var expires, created int64
	err := s.readDB.QueryRowContext(ctx, `SELECT api_key_id,session_encrypted,expires_at,created_at FROM realtime_client_secrets
 WHERE secret_hash=? AND expires_at>?`, hash, millis(now)).Scan(&secret.APIKeyID, &secret.SessionEncrypted, &expires, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return secret, ErrNotFound
	}
	if err != nil {
		return secret, err
	}
	secret.ExpiresAt, secret.CreatedAt = fromMillis(expires), fromMillis(created)
	return secret, nil
}
