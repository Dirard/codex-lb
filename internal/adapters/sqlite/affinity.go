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

// validAffinityCAS checks a reservation's soft-binding request before its job.
func validAffinityCAS(req domain.ReservationRequest) bool {
	cas := req.Affinity
	return cas == nil || validAffinityIdentity(cas.Binding.Key, cas.Binding.Kind) &&
		cas.Binding.APIKeyID == req.APIKeyID && cas.Binding.AccountID == req.AccountID && req.AccountID != "" &&
		!cas.Binding.UpdatedAt.IsZero() && cas.ExpectedVersion >= 0
}

// saveAffinityTx compares the binding observed before dispatch inside the
// reservation transaction that already proved the key/account scope. The
// reservation's ledger sequence is a monotonic version across delete/recreate,
// so a lost race or a stale observation leaves the winner untouched.
func saveAffinityTx(ctx context.Context, tx *sql.Tx, cas domain.AffinityCAS, sequence int64) (bool, error) {
	binding := cas.Binding
	if sequence <= cas.ExpectedVersion {
		return false, nil
	}
	var result sql.Result
	var err error
	if cas.ExpectedVersion == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO affinity_bindings
 (key,kind,api_key_id,account_id,created_at,updated_at,version) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(kind,key) DO NOTHING`, binding.Key, binding.Kind, binding.APIKeyID, binding.AccountID,
			millis(binding.UpdatedAt), millis(binding.UpdatedAt), sequence)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE affinity_bindings SET account_id=?,updated_at=?,version=?
 WHERE key=? AND kind=? AND api_key_id=? AND version=?`, binding.AccountID, millis(binding.UpdatedAt), sequence,
			binding.Key, binding.Kind, binding.APIKeyID, cas.ExpectedVersion)
	}
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
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
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
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
	result, err := s.exec(ctx, `DELETE FROM affinity_bindings WHERE (kind,key) IN
 (SELECT kind,key FROM affinity_bindings WHERE kind='prompt_cache' AND updated_at<=?
 ORDER BY updated_at,key LIMIT ?)`, millis(now.Add(-ttl)), limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
