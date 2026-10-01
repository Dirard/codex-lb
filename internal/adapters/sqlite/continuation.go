package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) GetContinuation(ctx context.Context, keyID, responseID string, now time.Time) (domain.Continuation, error) {
	var c domain.Continuation
	if keyID == "" || responseID == "" || now.IsZero() {
		return c, ErrInvalid
	}
	var created, expires int64
	err := s.readDB.QueryRowContext(ctx, `SELECT c.response_id,c.key_id,c.account_id,c.provider_id,c.model,
 c.created_at,c.expires_at,c.context_encrypted,c.file_pinned,c.quota_refused,c.turn_state_forwardable,a.generation,a.route_revision
 FROM continuations c JOIN accounts a ON a.id=c.account_id
 WHERE c.response_id=? AND c.key_id=? AND c.expires_at>?
 AND NOT(a.status='deactivated' AND a.deactivation_reason='deleted')`, responseID, keyID, millis(now)).Scan(
		&c.ResponseID, &c.KeyID, &c.AccountID, &c.ProviderID, &c.Model,
		&created, &expires, &c.ContextEncrypted, &c.FilePinned, &c.QuotaRefused, &c.TurnStateForwardable, &c.AccountGeneration, &c.RouteRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, c.ExpiresAt = fromMillis(created), fromMillis(expires)
	return c, nil
}

func (s *Store) SaveContinuation(ctx context.Context, c domain.Continuation, bounds domain.ContinuationBounds) error {
	if strings.HasPrefix(c.ResponseID, "session:") {
		return ErrInvalid
	}
	return s.saveContinuation(ctx, c, "", bounds)
}

// Response IDs have immutable owners; a logical session points to the latest
// settled turn and may move after application-approved quota failover.
func (s *Store) SaveSessionContinuation(ctx context.Context, c domain.Continuation, reservationID string, bounds domain.ContinuationBounds) error {
	if !strings.HasPrefix(c.ResponseID, "session:") || reservationID == "" {
		return ErrInvalid
	}
	return s.saveContinuation(ctx, c, reservationID, bounds)
}

func (s *Store) saveContinuation(ctx context.Context, c domain.Continuation, reservationID string, bounds domain.ContinuationBounds) error {
	if c.TurnStateForwardable && !strings.HasPrefix(c.ResponseID, "session:turn:") {
		return ErrInvalid
	}
	if c.ResponseID == "" || c.KeyID == "" || c.AccountID == "" || c.ProviderID == "" || c.Model == "" ||
		c.CreatedAt.IsZero() || !c.ExpiresAt.After(c.CreatedAt) || !c.ExpiresAt.After(time.Now()) ||
		bounds.MaxRecords < 1 || bounds.MaxContextBytes < 0 ||
		int64(len(c.ContextEncrypted)) > bounds.MaxContextBytes || !fernetCiphertext(c.ContextEncrypted) {
		return fmt.Errorf("continuation or bounds: %w", ErrInvalid)
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		current, err := accountRouteCurrentTx(ctx, tx, c.AccountID, c.AccountGeneration, c.RouteRevision)
		if err != nil {
			return err
		}
		if !current {
			return ErrConflict
		}
		var sequence int64
		if reservationID != "" {
			err := tx.QueryRowContext(ctx, `SELECT rowid FROM usage_reservations WHERE id=? AND api_key_id=? AND account_id=? AND account_generation=? AND route_revision=? AND status IN ('finalized','failed')`, reservationID, c.KeyID, c.AccountID, c.AccountGeneration, c.RouteRevision).Scan(&sequence)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalid
			}
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM continuations WHERE expires_at<=?", time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
		update := `expires_at=excluded.expires_at,context_encrypted=excluded.context_encrypted,
 file_pinned=excluded.file_pinned
 WHERE continuations.account_id=excluded.account_id AND
 continuations.provider_id=excluded.provider_id AND continuations.model=excluded.model`
		if reservationID != "" {
			update = `account_id=excluded.account_id,provider_id=excluded.provider_id,model=excluded.model,
 created_at=excluded.created_at,expires_at=excluded.expires_at,context_encrypted=excluded.context_encrypted,
 file_pinned=excluded.file_pinned,quota_refused=excluded.quota_refused,reservation_rowid=excluded.reservation_rowid,
 turn_state_forwardable=excluded.turn_state_forwardable
 WHERE continuations.reservation_rowid<excluded.reservation_rowid`
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO continuations
 (response_id,key_id,account_id,provider_id,model,created_at,expires_at,
 context_encrypted,file_pinned,quota_refused,reservation_rowid,turn_state_forwardable) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(response_id,key_id) DO UPDATE SET `+update,
			c.ResponseID, c.KeyID, c.AccountID, c.ProviderID, c.Model, millis(c.CreatedAt),
			millis(c.ExpiresAt), c.ContextEncrypted, boolInt(c.FilePinned), boolInt(c.QuotaRefused), sequence, boolInt(c.TurnStateForwardable))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if reservationID != "" {
				return nil
			} // A newer session turn already won.
			return fmt.Errorf("continuation owner changed: %w", ErrConflict)
		}
		for {
			var count, bytes int64
			if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(context_encrypted)),0)
 FROM continuations`).Scan(&count, &bytes); err != nil {
				return err
			}
			if count <= int64(bounds.MaxRecords) && bytes <= bounds.MaxContextBytes {
				return nil
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM continuations WHERE (response_id,key_id) IN (
 SELECT response_id,key_id FROM continuations WHERE NOT (response_id=? AND key_id=?)
 ORDER BY created_at,response_id,key_id LIMIT 1)`, c.ResponseID, c.KeyID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("continuation bounds cannot retain record: %w", ErrInvalid)
			}
		}
	})
}

func (s *Store) MarkContinuationQuotaRefused(ctx context.Context, keyID, responseID, accountID, reservationID string) error {
	if keyID == "" || responseID == "" || accountID == "" || reservationID == "" {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var ownerAccountID string
		var currentSequence int64
		err := tx.QueryRowContext(ctx, `SELECT account_id,reservation_rowid FROM continuations
 WHERE key_id=? AND response_id=?`, keyID, responseID).Scan(&ownerAccountID, &currentSequence)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ownerAccountID != accountID {
			return ErrNotFound
		}
		var refusalSequence, reservationGeneration, reservationRevision int64
		err = tx.QueryRowContext(ctx, `SELECT rowid,account_generation,route_revision FROM usage_reservations
 WHERE id=? AND api_key_id=? AND account_id=? AND (status='failed' OR (status='reserved' AND needs_reconciliation=1))`,
			reservationID, keyID, accountID).Scan(&refusalSequence, &reservationGeneration, &reservationRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		current, err := accountRouteCurrentTx(ctx, tx, accountID, reservationGeneration, reservationRevision)
		if err != nil {
			return err
		}
		if !current {
			return ErrConflict
		}
		if refusalSequence < currentSequence {
			return nil // A newer logical-session generation already won.
		}
		_, err = tx.ExecContext(ctx, `UPDATE continuations SET quota_refused=1
 WHERE key_id=? AND response_id=? AND account_id=? AND reservation_rowid=?`,
			keyID, responseID, accountID, currentSequence)
		return err
	})
}
