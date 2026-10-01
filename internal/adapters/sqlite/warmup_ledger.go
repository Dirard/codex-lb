package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

// ReserveWarmupUsage gives an admin probe durable accounting ownership without
// consuming any client key's budget. The account gate is rechecked atomically.
func (s *Store) ReserveWarmupUsage(ctx context.Context, id, accountID, model string, accountGeneration int64, automatic bool, now time.Time) error {
	if id == "" || accountID == "" || model == "" || now.IsZero() || accountGeneration < 0 {
		return ErrInvalid
	}
	if err := s.ensureInternalKey(ctx, domain.WarmupKeyID, "Admin warmup"); err != nil {
		return err
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var allowed int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=? AND generation=? AND kind='chatgpt'
 AND requires_egress_decision=0 AND status IN ('active','rate_limited','quota_exceeded','paused')
 AND (?=0 OR (status='active' AND limit_warmup_enabled=1))`, accountID, accountGeneration, boolInt(automatic)).Scan(&allowed)
		if err != nil {
			return err
		}
		if allowed != 1 {
			return ErrNoAccounts
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_reservations
		 (id,api_key_id,account_id,account_generation,model,status,created_at,updated_at) VALUES(?,?,?,?,?,'reserved',?,?)`,
			id, domain.WarmupKeyID, accountID, accountGeneration, model, millis(now), millis(now))
		return err
	})
}
