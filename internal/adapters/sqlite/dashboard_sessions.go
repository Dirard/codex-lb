package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && len(value) == 64
}

// RevokeDashboardSession records a logged-out session until its absolute
// expiry and prunes revocations whose sessions can no longer be presented.
func (s *Store) RevokeDashboardSession(ctx context.Context, sessionDigest string, expiresAt time.Time) error {
	if !validDigest(sessionDigest) {
		return fmt.Errorf("dashboard session digest: %w", ErrInvalid)
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dashboard_session_revocations WHERE expires_at<=?`, time.Now().Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO dashboard_session_revocations(session_hash,expires_at) VALUES(?,?)
 ON CONFLICT(session_hash) DO UPDATE SET expires_at=max(expires_at,excluded.expires_at)`, sessionDigest, expiresAt.Unix())
		return err
	})
}

func (s *Store) DashboardSessionRevoked(ctx context.Context, sessionDigest string) (bool, error) {
	var present int
	err := s.readDB.QueryRowContext(ctx, `SELECT 1 FROM dashboard_session_revocations WHERE session_hash=?`, sessionDigest).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AdvanceDashboardSessionEpoch rejects every session issued before the call.
func (s *Store) AdvanceDashboardSessionEpoch(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_secret SET session_epoch=session_epoch+1 WHERE id=1`)
	return err
}

func (s *Store) BootstrapTokenConsumed(ctx context.Context, tokenDigest string) (bool, error) {
	var present int
	err := s.readDB.QueryRowContext(ctx, `SELECT 1 FROM dashboard_bootstrap_consumed WHERE token_hash=?`, tokenDigest).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) ConsumeBootstrapToken(ctx context.Context, tokenDigest string, at time.Time) error {
	if !validDigest(tokenDigest) {
		return fmt.Errorf("bootstrap token digest: %w", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dashboard_bootstrap_consumed(token_hash,consumed_at) VALUES(?,?)
 ON CONFLICT(token_hash) DO NOTHING`, tokenDigest, at.Unix())
	return err
}
