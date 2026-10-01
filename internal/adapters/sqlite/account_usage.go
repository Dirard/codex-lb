package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const schemaV21 = `ALTER TABLE reset_credit_redeem_requests ADD COLUMN outcome_version INTEGER;`

// GetPinnedResetCredit returns the credit pinned to an earlier redemption of
// redeemRequestID for the account, when such a pin exists.
func (s *Store) GetPinnedResetCredit(ctx context.Context, accountID string, accountGeneration int64, redeemRequestID string) (application.ResetRedemption, bool, error) {
	return getPinnedResetCredit(ctx, s.db, accountID, accountGeneration, redeemRequestID)
}

func getPinnedResetCredit(ctx context.Context, db accountWriter, accountID string, accountGeneration int64, redeemRequestID string) (application.ResetRedemption, bool, error) {
	var pin application.ResetRedemption
	var version sql.NullInt64
	if accountGeneration < 0 {
		return pin, false, ErrInvalid
	}
	err := db.QueryRowContext(ctx,
		"SELECT credit_id,outcome_version FROM reset_credit_redeem_requests WHERE account_id=? AND account_generation=? AND redeem_request_id=?",
		accountID, accountGeneration, redeemRequestID).Scan(&pin.CreditID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return pin, false, nil
	}
	if err != nil {
		return pin, false, err
	}
	if version.Valid {
		pin.OutcomeVersion = &version.Int64
	}
	return pin, true, nil
}

// PinResetCredit durably records the redemption target. A racing retry in the
// same incarnation receives the first credit; an older incarnation's request ID
// is a conflict rather than a grant to the reimported account.
func (s *Store) PinResetCredit(ctx context.Context, accountID string, accountGeneration int64, redeemRequestID, creditID string) (application.ResetRedemption, error) {
	if accountID == "" || accountGeneration < 0 || redeemRequestID == "" {
		return application.ResetRedemption{}, domain.ErrInvalid
	}
	var pinned application.ResetRedemption
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		current, err := accountIncarnationCurrentTx(ctx, tx, accountID, accountGeneration)
		if err != nil {
			return err
		}
		if !current {
			return domain.ErrConflict
		}
		_, err = tx.ExecContext(ctx,
			"INSERT INTO reset_credit_redeem_requests(account_id,account_generation,redeem_request_id,credit_id,created_at,outcome_version) VALUES(?,?,?,?,?,coalesce((SELECT reservation_rowid FROM account_outcomes WHERE account_id=?),0)) "+
				"ON CONFLICT(account_id,redeem_request_id) DO NOTHING",
			accountID, accountGeneration, redeemRequestID, creditID, time.Now().UTC().Unix(), accountID)
		if err != nil {
			return err
		}
		var ok bool
		pinned, ok, err = getPinnedResetCredit(ctx, tx, accountID, accountGeneration, redeemRequestID)
		if err == nil && !ok {
			return domain.ErrConflict
		}
		return err
	})
	return pinned, err
}
