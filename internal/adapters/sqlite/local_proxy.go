package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) EnsureLocalProxyKey(ctx context.Context) error {
	return s.ensureInternalKey(ctx, domain.LocalProxyKeyID, "Local (keyless)")
}

func (s *Store) ensureInternalKey(ctx context.Context, id, name string) error {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rand.Text())))
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at)
 VALUES(?,?,?,'internal',?) ON CONFLICT(id) DO NOTHING`, id, name, hash, time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	key, err := s.GetAPIKey(ctx, id)
	if err != nil {
		return err
	}
	if key.Name != name || key.KeyPrefix != "internal" || !key.IsActive || key.GroupID != nil || key.ExpiresAt != nil || key.AccountAssignmentScopeEnabled || key.SourceAssignmentScopeEnabled || len(key.Limits) != 0 || len(key.AllowedModels) != 0 {
		return fmt.Errorf("reserved internal principal conflicts with imported data: %w", ErrConflict)
	}
	return nil
}
