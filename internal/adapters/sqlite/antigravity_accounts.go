package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"codex-lb/internal/domain"
)

// SetAntigravityAccountStatus applies an account effect of an Antigravity
// source in one transaction: the account's status and reason, guarded by
// the states that effect may replace, and, only when the status changed, the
// source's action link (nil leaves the stored link). No other column is
// written, so a source edit, quota outcome or operator pause made meanwhile
// is never reverted.
//
//   - reauth_required replaces active, rate_limited, quota_exceeded and
//     reauth_required; a pause or a deactivation stays.
//   - deactivated (reason tos_violation) replaces every state but a deletion.
//   - active returns only reauth_required and a tos_violation deactivation.
func (s *Store) SetAntigravityAccountStatus(ctx context.Context, sourceID string, status domain.AccountStatus, reason string, actionURL *string) error {
	var guard string
	switch {
	case status == domain.AccountReauthRequired && reason == "":
		guard = "status IN ('active','rate_limited','quota_exceeded','reauth_required')"
	case status == domain.AccountDeactivated && reason == "tos_violation":
		guard = "NOT (status='deactivated' AND deactivation_reason='deleted')"
	case status == domain.AccountActive && reason == "":
		guard = "(status='reauth_required' OR status='deactivated' AND deactivation_reason='tos_violation')"
	default:
		return ErrInvalid
	}
	if sourceID == "" || actionURL != nil && len(*actionURL) > 2048 {
		return ErrInvalid
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM legacy_model_sources
 WHERE id=? AND kind=? AND deleted_at IS NULL`, sourceID, domain.ModelSourceAntigravity).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET status=?,deactivation_reason=?
 WHERE id=? AND kind='external' AND `+guard, status, reason, sourceID)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil || changed == 0 || actionURL == nil {
			return err
		}
		if *actionURL == "" {
			_, err = tx.ExecContext(ctx, `UPDATE legacy_model_sources
 SET provider_config_json=json_remove(provider_config_json,'$.antigravity.actionUrl') WHERE id=?`, sourceID)
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE legacy_model_sources
 SET provider_config_json=json_set(provider_config_json,'$.antigravity.actionUrl',?) WHERE id=?`, *actionURL, sourceID)
		return err
	})
}

// RotateExternalCredential replaces a source's encrypted credential only
// while the row still holds previous, so a rotated refresh token never
// overwrites one a newer sign-in stored. It reports whether it wrote.
func (s *Store) RotateExternalCredential(ctx context.Context, accountID string, previous, next []byte) (bool, error) {
	if accountID == "" || len(previous) == 0 || len(next) == 0 || !fernetCiphertext(next) || bytes.Equal(previous, next) {
		return false, ErrInvalid
	}
	res, err := s.exec(ctx, `UPDATE account_credentials SET external_key_encrypted=?
 WHERE account_id=? AND external_key_encrypted=?`, next, accountID, previous)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}
