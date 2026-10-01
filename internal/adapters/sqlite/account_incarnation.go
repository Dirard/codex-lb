package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

// Account identity is checked inside the writer's transaction so DELETE and
// reimport cannot turn a late callback into a write for the next incarnation.
func accountIncarnationCurrentTx(ctx context.Context, tx *sql.Tx, accountID string, generation int64) (bool, error) {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT generation FROM accounts WHERE id=?
 AND NOT(status='deactivated' AND deactivation_reason='deleted')`, accountID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return current == generation, err
}

func accountRouteCurrentTx(ctx context.Context, tx *sql.Tx, accountID string, generation, revision int64) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM accounts WHERE id=? AND generation=? AND route_revision=?
 AND NOT(status='deactivated' AND deactivation_reason='deleted')`, accountID, generation, revision).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
