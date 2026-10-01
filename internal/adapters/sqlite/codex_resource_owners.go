package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/application"
)

const maxCodexResourceOwners = 100_000

func (s *Store) SaveCodexResourceOwner(ctx context.Context, owner application.CodexResourceOwner) error {
	now := time.Now().UTC()
	if owner.ResourceType != application.CodexResourceFile && owner.ResourceType != application.CodexResourceRealtime || len(owner.ResourceID) == 0 ||
		len(owner.ResourceID) > 512 || strings.TrimSpace(owner.ResourceID) == "" ||
		owner.KeyID == "" || owner.AccountID == "" || !owner.ExpiresAt.After(now) ||
		owner.ExpiresAt.After(now.Add(30*24*time.Hour)) {
		return fmt.Errorf("Codex resource owner: %w", ErrInvalid)
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		current, err := accountRouteCurrentTx(ctx, tx, owner.AccountID, owner.AccountGeneration, owner.RouteRevision)
		if err != nil {
			return err
		}
		if !current {
			return ErrConflict
		}
		// Expired rows are operational state; prune them while adding a new owner.
		if _, err := tx.ExecContext(ctx, `DELETE FROM codex_resource_owners
 WHERE expires_at<=? AND NOT (resource_type=? AND resource_id=?)`,
			millis(now), owner.ResourceType, owner.ResourceID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO codex_resource_owners
 (resource_type,resource_id,key_id,account_id,created_at,expires_at)
 VALUES(?,?,?,?,?,?) ON CONFLICT(resource_type,resource_id) DO UPDATE SET
 expires_at=max(codex_resource_owners.expires_at,excluded.expires_at)
 WHERE codex_resource_owners.key_id=excluded.key_id
 AND codex_resource_owners.account_id=excluded.account_id
 AND codex_resource_owners.expires_at>?`, owner.ResourceType, owner.ResourceID,
			owner.KeyID, owner.AccountID, millis(now), millis(owner.ExpiresAt), millis(now))
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrConflict
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM codex_resource_owners").Scan(&count); err != nil {
			return err
		}
		if count > maxCodexResourceOwners {
			_, err = tx.ExecContext(ctx, `DELETE FROM codex_resource_owners WHERE rowid IN (
 SELECT rowid FROM codex_resource_owners WHERE NOT (resource_type=? AND resource_id=?)
 ORDER BY expires_at,created_at,resource_id LIMIT ?)`, owner.ResourceType, owner.ResourceID,
				count-maxCodexResourceOwners)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetCodexResourceOwner(ctx context.Context, resourceType, resourceID, keyID string, now time.Time) (application.CodexResourceOwner, error) {
	var owner application.CodexResourceOwner
	if resourceType != application.CodexResourceFile && resourceType != application.CodexResourceRealtime || resourceID == "" || keyID == "" || now.IsZero() {
		return owner, ErrNotFound
	}
	var expires int64
	err := s.readDB.QueryRowContext(ctx, `SELECT o.resource_type,o.resource_id,o.key_id,o.account_id,o.expires_at,a.generation,a.route_revision
 FROM codex_resource_owners o JOIN accounts a ON a.id=o.account_id
 WHERE o.resource_type=? AND o.resource_id=? AND o.key_id=? AND o.expires_at>?
 AND NOT(a.status='deactivated' AND a.deactivation_reason='deleted')`, resourceType, resourceID, keyID, millis(now)).Scan(&owner.ResourceType,
		&owner.ResourceID, &owner.KeyID, &owner.AccountID, &expires, &owner.AccountGeneration, &owner.RouteRevision)
	if err == sql.ErrNoRows {
		return owner, ErrNotFound
	}
	if err != nil {
		return owner, err
	}
	owner.ExpiresAt = fromMillis(expires)
	return owner, nil
}
