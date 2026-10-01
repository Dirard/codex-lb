package sqlite

import "context"

// MarkInterruptedReservations is called only after the installation's exclusive
// database lock is held, before admitting requests. An interrupted upstream call
// may have consumed tokens: preserve its budget until an explicit reconciliation.
func (s *Store) MarkInterruptedReservations(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE usage_reservations SET needs_reconciliation=1
 WHERE status='reserved' AND needs_reconciliation=0`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
