package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

func validAffinityIdentity(key string, kind domain.AffinityKind) bool {
	return key != "" && len(key) <= 256 && kind.Valid()
}

func (s *Store) LookupAffinity(ctx context.Context, keyID string, kind domain.AffinityKind, key string) (domain.AffinityBinding, error) {
	var binding domain.AffinityBinding
	if keyID == "" || !validAffinityIdentity(key, kind) {
		return binding, ErrInvalid
	}
	var created, updated int64
	err := s.readDB.QueryRowContext(ctx, `SELECT key,kind,api_key_id,account_id,created_at,updated_at,version
 FROM affinity_bindings WHERE key=? AND kind=? AND api_key_id=?`, key, kind, keyID).Scan(
		&binding.Key, &binding.Kind, &binding.APIKeyID, &binding.AccountID, &created, &updated, &binding.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, ErrNotFound
	}
	binding.CreatedAt, binding.UpdatedAt = fromMillis(created), fromMillis(updated)
	return binding, err
}

// SaveAffinity compares the observed binding before dispatch. A reserved
// key/account-owned attempt supplies a monotonic generation across delete/recreate.
func (s *Store) SaveAffinity(ctx context.Context, binding domain.AffinityBinding, expectedVersion int64, reservationID string) (bool, error) {
	if !validAffinityIdentity(binding.Key, binding.Kind) || binding.APIKeyID == "" || binding.AccountID == "" ||
		binding.UpdatedAt.IsZero() || expectedVersion < 0 || reservationID == "" {
		return false, ErrInvalid
	}
	changed := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var sequence, generation, revision int64
		var model string
		var group sql.NullString
		var accountScoped, sourceScoped bool
		err := tx.QueryRowContext(ctx, `SELECT r.rowid,r.account_generation,r.route_revision,r.model,k.group_id,k.account_assignment_scope_enabled,k.source_assignment_scope_enabled
	FROM usage_reservations r JOIN api_keys k ON k.id=r.api_key_id
	JOIN accounts a ON a.id=r.account_id AND a.generation=r.account_generation AND a.route_revision=r.route_revision
 WHERE r.id=? AND r.api_key_id=? AND r.account_id=? AND r.status='reserved' AND r.needs_reconciliation=0
	AND NOT(a.status='deactivated' AND a.deactivation_reason='deleted')
 AND k.is_active=1 AND k.deleted_at IS NULL
 AND (k.expires_at IS NULL OR k.expires_at>?)`, reservationID, binding.APIKeyID, binding.AccountID, millis(binding.UpdatedAt)).Scan(&sequence, &generation, &revision, &model, &group, &accountScoped, &sourceScoped)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoAccounts
		}
		if err != nil {
			return err
		}
		if err := checkReservedAccountTx(ctx, tx, domain.ReservationRequest{APIKeyID: binding.APIKeyID,
			AccountID: binding.AccountID, AccountGeneration: generation, RouteRevision: revision, Model: model, Now: binding.UpdatedAt}, group, accountScoped, sourceScoped); err != nil {
			return err
		}
		if sequence <= expectedVersion {
			return nil
		}
		var result sql.Result
		if expectedVersion == 0 {
			result, err = tx.ExecContext(ctx, `INSERT INTO affinity_bindings
 (key,kind,api_key_id,account_id,created_at,updated_at,version) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(kind,key) DO NOTHING`, binding.Key, binding.Kind, binding.APIKeyID, binding.AccountID,
				millis(binding.UpdatedAt), millis(binding.UpdatedAt), sequence)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE affinity_bindings SET account_id=?,updated_at=?,version=?
 WHERE key=? AND kind=? AND api_key_id=? AND version=?`, binding.AccountID, millis(binding.UpdatedAt), sequence,
				binding.Key, binding.Kind, binding.APIKeyID, expectedVersion)
		}
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		changed = count != 0
		return err
	})
	return changed, err
}

func (s *Store) DeleteAffinities(ctx context.Context, targets []domain.AffinityIdentifier) (domain.AffinityDeleteResult, error) {
	result := domain.AffinityDeleteResult{Deleted: []domain.AffinityIdentifier{}, Failed: []domain.AffinityDeleteFailure{}}
	if len(targets) == 0 || len(targets) > 500 {
		return result, ErrInvalid
	}
	seen := make(map[domain.AffinityIdentifier]bool, len(targets))
	for _, target := range targets {
		if !validAffinityIdentity(target.Key, target.Kind) || seen[target] {
			return result, ErrInvalid
		}
		seen[target] = true
	}
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		for _, target := range targets {
			row, err := tx.ExecContext(ctx, "DELETE FROM affinity_bindings WHERE key=? AND kind=?", target.Key, target.Kind)
			if err != nil {
				return err
			}
			count, err := row.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				result.Failed = append(result.Failed, domain.AffinityDeleteFailure{AffinityIdentifier: target, Reason: "not_found"})
			} else {
				result.Deleted = append(result.Deleted, target)
			}
		}
		return nil
	})
	if err != nil {
		return domain.AffinityDeleteResult{}, err
	}
	result.DeletedCount = len(result.Deleted)
	return result, nil
}

func (s *Store) PruneAffinities(ctx context.Context, now time.Time, ttl time.Duration, limit int) (int, error) {
	if now.IsZero() || ttl <= 0 || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM affinity_bindings WHERE (kind,key) IN
 (SELECT kind,key FROM affinity_bindings WHERE kind='prompt_cache' AND updated_at<=?
 ORDER BY updated_at,key LIMIT ?)`, millis(now.Add(-ttl)), limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
