package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MarkReservationUncertain retains the held budget until explicit offline
// reconciliation. It marks only this request, never unrelated live calls.
func (s *Store) MarkReservationUncertain(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, ErrInvalid
	}
	retained := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE usage_reservations SET needs_reconciliation=1,
 updated_at=max(updated_at,?) WHERE id=? AND status='reserved' AND needs_reconciliation=0`,
			time.Now().UTC().UnixMilli(), id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 0 {
			retained = changed != 0
			return err
		}
		var status string
		var flagged bool
		err = tx.QueryRowContext(ctx, `SELECT status,needs_reconciliation FROM usage_reservations WHERE id=?`, id).Scan(&status, &flagged)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		retained = status == "reserved" && flagged
		return err
	})
	return retained, err
}
