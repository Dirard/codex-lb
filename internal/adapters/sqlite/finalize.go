package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"codex-lb/internal/domain"
)

// FinalizeResponse commits an attempt's ledger step, account outcome and
// ownership as one batch-writer job, so the client terminal waits for a single
// durable commit. The ledger step fails the whole job; the outcome and the
// ownership steps run under nested savepoints so their failure can never undo
// the settlement that precedes them.
func (s *Store) FinalizeResponse(ctx context.Context, f domain.ResponseFinalization) (domain.ResponseFinalizationResult, error) {
	if f.ReservationID == "" {
		return domain.ResponseFinalizationResult{}, ErrInvalid
	}
	if !f.RetainUncertain {
		if err := validateSettlement(f.ReservationID, f.Settlement); err != nil {
			return domain.ResponseFinalizationResult{}, err
		}
	}
	var result domain.ResponseFinalizationResult
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result = domain.ResponseFinalizationResult{}
		if f.RetainUncertain {
			retained, err := markReservationUncertainTx(ctx, tx, f.ReservationID)
			if err != nil || !retained {
				return err
			}
			result.Retained = true
		} else if _, err := settleUsageTx(ctx, tx, f.ReservationID, f.Settlement); err != nil {
			return err
		}
		if outcome := f.Outcome; outcome != nil {
			stepErr, fatal := isolated(ctx, tx, "finalize_outcome", func() error {
				if outcome.AccountID == "" || outcome.QuotaRefused == outcome.Success {
					return ErrInvalid
				}
				if err := recordAccountOutcomeTx(ctx, tx, outcome.AccountID, f.ReservationID, outcome.QuotaRefused, outcome.Success); err != nil {
					return err
				}
				if mark := outcome.QuotaMark; mark != nil {
					if mark.KeyID == "" || mark.ResponseID == "" {
						return ErrInvalid
					}
					return markContinuationQuotaRefusedTx(ctx, tx, mark.KeyID, mark.ResponseID, outcome.AccountID, f.ReservationID)
				}
				return nil
			})
			if fatal != nil {
				return fatal
			}
			if stepErr != nil {
				result.OutcomeErr = stepErr
				return nil // Ownership is never published without its outcome.
			}
		}
		if owner := f.Continuation; owner != nil {
			stepErr, fatal := isolated(ctx, tx, "finalize_continuation", func() error {
				if err := validContinuation(*owner, "", f.Bounds); err != nil {
					return err
				}
				if err := saveContinuationTx(ctx, tx, *owner, "", f.Bounds); err != nil {
					return err
				}
				for _, alias := range f.Aliases {
					if err := validContinuation(alias, f.ReservationID, f.Bounds); err != nil {
						return err
					}
					if err := saveContinuationTx(ctx, tx, alias, f.ReservationID, f.Bounds); err != nil {
						return err
					}
				}
				return nil
			})
			if fatal != nil {
				return fatal
			}
			result.ContinuationErr = stepErr
		}
		return nil
	})
	if err != nil {
		return domain.ResponseFinalizationResult{}, err
	}
	return result, nil
}

// isolated runs fn under a nested savepoint. A failed step is rolled back and
// returned as stepErr; a failed savepoint control statement is fatal for the
// enclosing job because the transaction state is no longer known.
func isolated(ctx context.Context, tx *sql.Tx, name string, fn func() error) (stepErr, fatal error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return nil, err
	}
	stepErr = fn()
	if stepErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO "+name); err != nil {
			return stepErr, errors.Join(stepErr, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE "+name); err != nil {
		return stepErr, err
	}
	return stepErr, nil
}
