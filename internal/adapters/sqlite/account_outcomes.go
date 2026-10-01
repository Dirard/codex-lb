package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"codex-lb/internal/domain"
)

// RecordAccountOutcome requires a durable, account-owned accounting decision.
// Retained unknown usage permits quota refusal, never a healthy-success result.
// Reservation rowid fences a delayed outcome from overwriting a newer one.
func (s *Store) RecordAccountOutcome(ctx context.Context, accountID, reservationID string, quotaRefused, success bool) error {
	if accountID == "" || reservationID == "" || quotaRefused == success {
		return ErrInvalid
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var rowID, generation, revision int64
		var reservedAccountID, status string
		var held bool
		var decidedAt int64
		err := tx.QueryRowContext(ctx, `SELECT rowid,account_id,account_generation,route_revision,status,needs_reconciliation,updated_at FROM usage_reservations WHERE id=?`,
			reservationID).Scan(&rowID, &reservedAccountID, &generation, &revision, &status, &held, &decidedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if reservedAccountID != accountID || (quotaRefused && status != "failed" && !(status == "reserved" && held)) || (success && status != "finalized") {
			return fmt.Errorf("reservation outcome mismatch: %w", ErrInvalid)
		}
		current, err := accountRouteCurrentTx(ctx, tx, accountID, generation, revision)
		if err != nil || !current {
			return err // A deleted or replaced account keeps its settled bill, not its health outcome.
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO account_outcomes(account_id,reservation_rowid)
 VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET reservation_rowid=excluded.reservation_rowid
 WHERE account_outcomes.reservation_rowid<excluded.reservation_rowid`, accountID, rowID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		newStatus := domain.AccountActive
		if quotaRefused {
			newStatus = domain.AccountQuotaExceeded
			// Quota proof must survive later reconciliation of an unknown bill.
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_quota_block_evidence(account_id,blocked_at) VALUES(?,?)
 ON CONFLICT(account_id) DO UPDATE SET blocked_at=max(blocked_at,excluded.blocked_at)`, accountID, decidedAt); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET status=? WHERE id=?
 AND status IN ('active','rate_limited','quota_exceeded')`, newStatus, accountID)
		return err
	})
}

func (s *Store) CaptureQuotaOutcome(ctx context.Context, accountID string) (int64, error) {
	var version int64
	err := s.readDB.QueryRowContext(ctx, `SELECT coalesce((SELECT reservation_rowid
 FROM account_outcomes WHERE account_id=accounts.id),0) FROM accounts WHERE id=?`, accountID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return version, err
}

// RecoverAccountQuota changes only an older quota-derived block. The caller
// proves a fresh reset; the SQL predicate fences in-flight provider outcomes
// and administrator policy changes made while that usage fetch was running.
func (s *Store) RecoverAccountQuota(ctx context.Context, accountID string, version, generation int64) (bool, error) {
	if accountID == "" || version < 0 || generation < 0 {
		return false, ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE accounts SET status='active'
	 WHERE id=? AND generation=? AND kind='chatgpt' AND requires_egress_decision=0
 AND status IN ('rate_limited','quota_exceeded')
	 AND coalesce((SELECT reservation_rowid FROM account_outcomes WHERE account_id=accounts.id),0)=?`, accountID, generation, version)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}
