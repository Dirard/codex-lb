package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) RotateAccountCredential(ctx context.Context, expected, next domain.AccountCredential, now time.Time) (bool, error) {
	if expected.AccountID == "" || expected.AccountID != next.AccountID || expected.Generation < 0 ||
		expected.Generation != next.Generation || now.IsZero() {
		return false, ErrInvalid
	}
	for _, ciphertext := range [][]byte{next.AccessTokenEncrypted, next.RefreshTokenEncrypted, next.IDTokenEncrypted} {
		if len(ciphertext) == 0 || !fernetCiphertext(ciphertext) {
			return false, ErrInvalid
		}
	}
	changed := false
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE account_credentials
 SET access_token_encrypted=?,refresh_token_encrypted=?,id_token_encrypted=?
 WHERE account_id=? AND access_token_encrypted=? AND refresh_token_encrypted=? AND id_token_encrypted=?
 AND EXISTS(SELECT 1 FROM accounts a WHERE a.id=account_credentials.account_id AND a.generation=? AND a.deactivation_reason!='deleted')`, next.AccessTokenEncrypted, next.RefreshTokenEncrypted, next.IDTokenEncrypted, expected.AccountID, expected.AccessTokenEncrypted, expected.RefreshTokenEncrypted, expected.IDTokenEncrypted, expected.Generation)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil || rows == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE accounts SET last_refresh=? WHERE id=?", millis(now), expected.AccountID)
		changed = err == nil
		return err
	})
	return changed, err
}

func (s *Store) MarkAccountReauthRequired(ctx context.Context, expected domain.AccountCredential, code string) error {
	if expected.AccountID == "" || expected.Generation < 0 || code == "" || len(code) > 128 || !fernetCiphertext(expected.RefreshTokenEncrypted) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET status='reauth_required',deactivation_reason=?
 WHERE id=? AND generation=? AND status IN ('active','rate_limited','quota_exceeded')
 AND EXISTS(SELECT 1 FROM account_credentials c WHERE c.account_id=accounts.id AND c.refresh_token_encrypted=?)`, code, expected.AccountID, expected.Generation, expected.RefreshTokenEncrypted)
	return err
}
