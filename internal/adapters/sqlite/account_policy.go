package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"codex-lb/internal/domain"
)

// Policy changes update only their own column, so an OAuth refresh, quota
// outcome, or another administrator cannot be reverted by a stale snapshot.
func (s *Store) UpdateAccountAlias(ctx context.Context, id, alias string) error {
	if len(alias) > 255 {
		return ErrInvalid
	}
	return s.updateAccountPolicy(ctx, id, "alias", alias)
}

func (s *Store) UpdateAccountWarmup(ctx context.Context, id string, enabled bool) error {
	return s.updateAccountPolicy(ctx, id, "limit_warmup_enabled", boolInt(enabled))
}

func (s *Store) UpdateAccountRoutingPolicy(ctx context.Context, id, policy string) error {
	switch policy {
	case "normal", "burn_first", "preserve":
	default:
		return ErrInvalid
	}
	return s.updateAccountPolicy(ctx, id, "routing_policy", policy)
}

func (s *Store) UpdateAccountSecurityAuthorization(ctx context.Context, id string, authorized bool) error {
	return s.updateAccountPolicy(ctx, id, "security_work_authorized", boolInt(authorized))
}

func (s *Store) updateAccountPolicy(ctx context.Context, id, column string, value any) error {
	if id == "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, "UPDATE accounts SET "+column+`=? WHERE id=?
 AND NOT (status='deactivated' AND deactivation_reason='deleted')`, value, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TransitionAccountStatus applies the operator's action only to the state it
// observed. A newer quota, reauth or pause write wins and yields a conflict.
func (s *Store) TransitionAccountStatus(ctx context.Context, id string, expected domain.AccountStatus, reason string, next domain.AccountStatus) error {
	if id == "" || (next != domain.AccountActive && next != domain.AccountPaused) {
		return ErrInvalid
	}
	if expected == domain.AccountReauthRequired || expected == domain.AccountDeactivated &&
		(next == domain.AccountPaused || reason == "deleted") {
		return ErrConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET status=?,deactivation_reason=''
 WHERE id=? AND status=? AND deactivation_reason=?
 AND NOT (status='deactivated' AND deactivation_reason='deleted')`, next, id, expected, reason)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	var current domain.AccountStatus
	var currentReason string
	err = s.db.QueryRowContext(ctx, "SELECT status,deactivation_reason FROM accounts WHERE id=?", id).Scan(&current, &currentReason)
	if errors.Is(err, sql.ErrNoRows) || err == nil && current == domain.AccountDeactivated && currentReason == "deleted" {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("account status changed: %w", ErrConflict)
}
