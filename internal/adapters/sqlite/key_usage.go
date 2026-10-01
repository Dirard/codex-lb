package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

// RefreshExpiredKeyLimits applies the same whole-window lazy reset used by
// admission, without clearing still-active limits on the same key.
func (s *Store) RefreshExpiredKeyLimits(ctx context.Context, keyID string, now time.Time) error {
	if keyID == "" || domain.IsInternalKey(keyID) || now.IsZero() {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,limit_window,reset_at FROM api_key_limits
 WHERE api_key_id=? AND is_active=1 AND reset_at<=?`, keyID, millis(now))
		if err != nil {
			return err
		}
		type expired struct {
			id, reset int64
			window    domain.LimitWindow
		}
		limits := []expired{}
		for rows.Next() {
			var limit expired
			if err := rows.Scan(&limit.id, &limit.window, &limit.reset); err != nil {
				rows.Close()
				return err
			}
			limits = append(limits, limit)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, limit := range limits {
			duration, err := limit.window.Duration()
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE api_key_limits SET current_value=0,reset_at=?,
 backfill_from=NULL,backfill_until=NULL
 WHERE id=? AND reset_at=?`, advanceReset(limit.reset, now, duration), limit.id, limit.reset)
			if err != nil {
				return err
			}
		}
		return nil
	})
}
