package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"codex-lb/internal/domain"
)

func validActor(actor domain.KeyActor) bool {
	return actor == domain.KeyActorOwner || actor == domain.KeyActorAdmin
}

// checkForkTx refuses a fork whose parent is missing, is itself a fork or is
// narrower than the fork, and a new fork beyond the parent's bound.
func checkForkTx(ctx context.Context, tx *sql.Tx, fork domain.APIKey, inserting bool) error {
	parentID := fork.Fork.ParentID
	if domain.IsInternalKey(parentID) {
		return fmt.Errorf("internal keys have no forks: %w", ErrInvalid)
	}
	parent, err := scanKey(tx.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE id=? AND deleted_at IS NULL", parentID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("unknown parent key: %w", ErrInvalid)
	}
	if err != nil {
		return err
	}
	if parent.Fork != nil {
		return fmt.Errorf("forks have no forks: %w", ErrInvalid)
	}
	if field := domain.ForkViolation(parent, fork); field != "" {
		return &domain.ForkExceedsParent{Field: field}
	}
	if !inserting {
		return nil
	}
	var forks int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM api_keys WHERE parent_key_id=? AND deleted_at IS NULL", parentID).Scan(&forks); err != nil {
		return err
	}
	if forks >= domain.MaxForksPerKey {
		return domain.ErrForkLimitReached
	}
	return nil
}

// disableExceedingForksTx disables, in the parent's transaction, every active
// fork whose models, efforts or tier the parent's new settings no longer
// allow. The fork keeps its settings; its owner narrows and re-enables it.
func disableExceedingForksTx(ctx context.Context, tx *sql.Tx, parent domain.APIKey, now time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE parent_key_id=? AND deleted_at IS NULL AND is_active=1", parent.ID)
	if err != nil {
		return err
	}
	var exceeding []string
	for rows.Next() {
		fork, err := scanKey(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if domain.ForkSettingsViolation(parent, fork) != "" {
			exceeding = append(exceeding, fork.ID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range exceeding {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET is_active=0,fork_updated_by='admin',fork_updated_at=? WHERE id=?`, millis(now), id); err != nil {
			return err
		}
	}
	return nil
}

// ListKeyForks returns the stored settings of a parent's forks, oldest first.
func (s *Store) ListKeyForks(ctx context.Context, parentID string) ([]domain.APIKey, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id FROM api_keys WHERE parent_key_id=? AND deleted_at IS NULL ORDER BY created_at,id", parentID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	forks := make([]domain.APIKey, 0, len(ids))
	for _, id := range ids {
		fork, err := s.GetStoredAPIKey(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return nil, err
		}
		forks = append(forks, fork)
	}
	return forks, nil
}

// keyServingScope is the account scope and state a key serves with. A fork serves
// with its parent's group and assignments, only while both keys are active,
// until the earlier expiry.
type keyServingScope struct {
	// scopeID is the key whose assignments apply: the parent for a fork.
	scopeID string
	// parentID is set for a fork.
	parentID      sql.NullString
	group         sql.NullString
	accountScoped bool
	sourceScoped  bool
	active        bool
	expires       sql.NullInt64
}

// servingStateColumns are the serving state and expiry of key k with its
// parent p left-joined (p.deleted_at IS NULL in the join).
const servingStateColumns = `k.is_active AND (k.parent_key_id IS NULL OR coalesce(p.is_active,0)),
 CASE WHEN p.expires_at IS NOT NULL AND (k.expires_at IS NULL OR p.expires_at<k.expires_at) THEN p.expires_at ELSE k.expires_at END`

// effectiveGroupColumn is the group of key k with its parent p left-joined:
// a fork belongs to its parent's group and stores none of its own.
const effectiveGroupColumn = `CASE WHEN k.parent_key_id IS NULL THEN k.group_id ELSE p.group_id END`

// keysOfGroupCondition selects the keys k, forks included, whose effective
// group is the bound parameter; it needs no join.
const keysOfGroupCondition = `(k.group_id=? AND k.parent_key_id IS NULL OR k.parent_key_id IN (SELECT id FROM api_keys WHERE group_id=?))`

const keyServingScopeQuery = `SELECT k.parent_key_id,coalesce(p.id,k.id),
 ` + effectiveGroupColumn + `,
 CASE WHEN k.parent_key_id IS NULL THEN k.account_assignment_scope_enabled ELSE coalesce(p.account_assignment_scope_enabled,0) END,
 CASE WHEN k.parent_key_id IS NULL THEN k.source_assignment_scope_enabled ELSE coalesce(p.source_assignment_scope_enabled,0) END,
 ` + servingStateColumns + `
 FROM api_keys k LEFT JOIN api_keys p ON p.id=k.parent_key_id AND p.deleted_at IS NULL
 WHERE k.id=? AND k.deleted_at IS NULL`

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// loadKeyServingScope reads keyID's scope; sql.ErrNoRows means no such key.
func loadKeyServingScope(ctx context.Context, q rowQueryer, keyID string) (keyServingScope, error) {
	var scope keyServingScope
	err := q.QueryRowContext(ctx, keyServingScopeQuery, keyID).Scan(&scope.parentID, &scope.scopeID, &scope.group,
		&scope.accountScoped, &scope.sourceScoped, &scope.active, &scope.expires)
	return scope, err
}

// usable reports whether the key may serve at now.
func (s keyServingScope) usable(now time.Time) bool {
	return s.active && (!s.expires.Valid || s.expires.Int64 > millis(now))
}
