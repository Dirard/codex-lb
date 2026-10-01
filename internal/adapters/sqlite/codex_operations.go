package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"codex-lb/internal/domain"
)

// RecordCodexContentFreeUsage records keyed ancillary statistics without
// creating a reservation or consuming API-key usage limits.
func (s *Store) RecordCodexContentFreeUsage(ctx context.Context, event domain.UsageEvent) error {
	if err := validateUsageEvent(event); err != nil {
		return err
	}
	if event.APIKeyID == "" || event.AccountID == "" || event.ReservationID != "" ||
		event.Usage != (domain.UsageAmount{}) {
		return fmt.Errorf("content-free usage event: %w", ErrInvalid)
	}
	inserted, discarded := false, false
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		deleted, discard, err := usageDeletionPolicyTx(ctx, tx, event.AccountID, event.AccountGeneration)
		if err != nil || discard {
			discarded = discard
			return err
		}
		if deleted {
			event.AccountID = ""
		}
		inserted, err = insertUsageTx(ctx, tx, event, deleted)
		return err
	})
	if err == nil && !inserted && !discarded {
		return ErrConflict
	}
	return err
}
