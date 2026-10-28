package sqlite

import (
	"context"
	"strings"
)

// MarkInterruptedReservations preserves the budget of open reservations whose
// owning process is gone: an interrupted upstream call may have consumed
// tokens, so they stay reserved until an explicit reconciliation. live lists
// the instance ids of the workers that are still running, the caller's own
// included; their reservations are left alone, and reservations without an
// owner never count as live. Nil or empty live marks every open reservation,
// which is correct only while no other worker uses the database, such as at
// startup under the installation's exclusive lock before admitting requests.
func (s *Store) MarkInterruptedReservations(ctx context.Context, live []string) (int64, error) {
	query := `UPDATE usage_reservations SET needs_reconciliation=1
 WHERE status='reserved' AND needs_reconciliation=0`
	owners := make([]any, 0, len(live))
	for _, id := range live {
		if id != "" {
			owners = append(owners, id)
		}
	}
	if len(owners) != 0 {
		query += ` AND owner_instance NOT IN (?` + strings.Repeat(",?", len(owners)-1) + `)`
	}
	result, err := s.exec(ctx, query, owners...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// MarkInstanceReservationsInterrupted marks the open reservations owned by
// workers known to be gone, such as one that exited during an update while
// others keep serving. Unlike a live list for MarkInterruptedReservations,
// naming the exited workers cannot flag a worker that started after the list
// was taken. Empty ids are ignored and an empty list marks nothing.
func (s *Store) MarkInstanceReservationsInterrupted(ctx context.Context, instances []string) (int64, error) {
	owners := make([]any, 0, len(instances))
	for _, id := range instances {
		if id != "" {
			owners = append(owners, id)
		}
	}
	if len(owners) == 0 {
		return 0, nil
	}
	result, err := s.exec(ctx, `UPDATE usage_reservations SET needs_reconciliation=1
 WHERE status='reserved' AND needs_reconciliation=0
 AND owner_instance IN (?`+strings.Repeat(",?", len(owners)-1)+`)`, owners...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
