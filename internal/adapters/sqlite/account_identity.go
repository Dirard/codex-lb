package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"codex-lb/internal/domain"
)

// SaveAccountIdentity replaces authentication metadata and credentials together.
// Login is not authorization to unpause an account or remove an egress policy.
func (s *Store) SaveAccountIdentity(ctx context.Context, account domain.Account, credential domain.AccountCredential) error {
	if account.ID != credential.AccountID {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		existing, err := scanAccount(tx.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=?", account.ID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			account.Generation = existing.Generation
			account.RouteRevision = existing.RouteRevision
			account.RequiresEgressDecision = existing.RequiresEgressDecision
			if existing.Status == domain.AccountDeactivated && existing.DeactivationReason == "deleted" {
				var cleaned bool
				if err := tx.QueryRowContext(ctx, `SELECT cleanup_done FROM account_deletions
 WHERE account_id=? AND generation=?`, existing.ID, existing.Generation).Scan(&cleaned); err != nil || !cleaned || existing.Generation == math.MaxInt64 {
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return err
					}
					return ErrConflict
				}
				account.Generation++
				account.CreatedAt = time.Now().UTC()
				account.Status, account.DeactivationReason = domain.AccountActive, ""
				account.SecurityWorkAuthorized, account.LimitWarmupEnabled = false, false
				account.RoutingPolicy = "normal"
				if _, err := tx.ExecContext(ctx, `UPDATE accounts SET generation=?,created_at=?,status='active',deactivation_reason=''
 WHERE id=?`, account.Generation, millis(account.CreatedAt), account.ID); err != nil {
					return err
				}
			} else {
				account.Alias = existing.Alias
				account.RoutingPolicy = existing.RoutingPolicy
				account.SecurityWorkAuthorized = existing.SecurityWorkAuthorized
				account.LimitWarmupEnabled = existing.LimitWarmupEnabled
				account.CodexInstallationID = existing.CodexInstallationID
				if existing.Status == domain.AccountPaused || existing.Status == domain.AccountDeactivated {
					account.Status = existing.Status
					account.DeactivationReason = existing.DeactivationReason
				}
			}
		}
		credential.Generation = account.Generation
		credential.RouteRevision = account.RouteRevision
		if err := saveAccount(ctx, tx, account); err != nil {
			return err
		}
		if err := saveAccountCredential(ctx, tx, credential); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM account_usage_observation WHERE account_id=?", account.ID)
		return err
	})
}
