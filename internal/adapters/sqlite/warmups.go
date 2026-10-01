package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// LimitWarmupAttempt is a durable attempt row. Claim writes happen before
// dispatch; an interrupted tuple is never sent again.
type LimitWarmupAttempt = application.LimitWarmupAttempt

// ClaimLimitWarmupAttempt atomically deduplicates one account/window/reset
// tuple. Idle attempts additionally share an account-wide cooldown.
func (s *Store) ClaimLimitWarmupAttempt(ctx context.Context, accountID, window string, resetAt time.Time, model string, cooldown time.Duration, now time.Time) (LimitWarmupAttempt, bool, error) {
	var attempt LimitWarmupAttempt
	if accountID == "" || model == "" || resetAt.IsZero() || now.IsZero() || cooldown < 0 ||
		(window != "primary" && window != "secondary" && window != "monthly" && window != "primary_idle") {
		return attempt, false, ErrInvalid
	}
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		var existing int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_limit_warmup_attempts
		 WHERE account_id=? AND window=? AND reset_at BETWEEN ? AND ? LIMIT 1`,
			accountID, window, millis(resetAt.Add(-5*time.Second)), millis(resetAt.Add(5*time.Second))).Scan(&existing)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var lastStatus string
		var lastAttempted int64
		var lastAttempt int64
		err = tx.QueryRowContext(ctx, `SELECT attempt,status,attempted_at
		 FROM account_limit_warmup_attempts WHERE account_id=?
		 ORDER BY attempt DESC LIMIT 1`, accountID).Scan(&lastAttempt, &lastStatus, &lastAttempted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && cooldown > 0 {
			lastTime := fromMillis(lastAttempted)
			if lastTime.Add(cooldown).After(now) {
				return nil
			}
			if lastStatus == "claimed" {
				// Old claim may have reached upstream; never replay its tuple.
				if _, err := tx.ExecContext(ctx, `UPDATE account_limit_warmup_attempts
				 SET status='abandoned', completed_at=?, error_code='stale_claim_abandoned'
				 WHERE account_id=? AND attempt=? AND status='claimed'`,
					millis(now), accountID, lastAttempt); err != nil {
					return err
				}
			}
		}
		next := lastAttempt + 1
		attempt = LimitWarmupAttempt{
			AccountID: accountID, Attempt: next, Status: "claimed",
			Window: window, ResetAt: resetAt, Model: model, AttemptedAt: now,
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_limit_warmup_attempts
		 (account_id,attempt,status,model,attempted_at,window,reset_at) VALUES(?,?,?,?,?,?,?)`,
			accountID, next, "claimed", model, millis(now), window, millis(resetAt))
		return err
	})
	if err != nil {
		return attempt, false, err
	}
	return attempt, attempt.Attempt != 0, nil
}

// CompleteLimitWarmupAttempt finalizes a claimed attempt.
func (s *Store) CompleteLimitWarmupAttempt(ctx context.Context, accountID string, attempt int64, status string, finishedAt time.Time, errorCode *string) error {
	if status != domain.AutomationSuccess && status != domain.AutomationFailed {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE account_limit_warmup_attempts
	 SET status=?, completed_at=?, error_code=? WHERE account_id=? AND attempt=? AND status='claimed'`,
		status, millis(finishedAt), errorCode, accountID, attempt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}
