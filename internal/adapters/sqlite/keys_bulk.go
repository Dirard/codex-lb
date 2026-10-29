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

// keysActiveJobMax bounds the keys of one SetAPIKeysActive write job.
const keysActiveJobMax = 100

// SetAPIKeysActive enables or disables keys in one atomic write job (dashboard
// redesign use case 6.1) and reports, per requested id, whether it named a
// non-deleted, non-internal key. Only is_active changes.
func (s *Store) SetAPIKeysActive(ctx context.Context, ids []string, active bool) ([]bool, error) {
	if len(ids) == 0 || len(ids) > keysActiveJobMax {
		return nil, fmt.Errorf("bulk key job: %w", ErrInvalid)
	}
	args := make([]any, 0, len(ids)+3)
	args = append(args, boolInt(active), domain.LocalProxyKeyID, domain.WarmupKeyID)
	for _, id := range ids {
		args = append(args, id)
	}
	query := `UPDATE api_keys SET is_active=? WHERE deleted_at IS NULL AND id NOT IN (?,?)
 AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `) RETURNING id`
	changed := map[string]bool{}
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		clear(changed)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			changed[id] = true
		}
		return closeRows(rows)
	})
	if err != nil {
		return nil, err
	}
	found := make([]bool, len(ids))
	for i, id := range ids {
		found[i] = changed[id]
	}
	return found, nil
}

// MoveAPIKeyToGroup moves one key into a group, or out of its group with a nil
// groupID, in one write job, with the effect of SaveAPIKey for that group: a
// group enables the account-assignment scope and its limit templates replace
// the key's limits (backfilling new windows); leaving a group keeps the key's
// last effective limits. Other columns, the last use included, are untouched.
func (s *Store) MoveAPIKeyToGroup(ctx context.Context, id string, groupID *string, now time.Time) error {
	if id == "" || now.IsZero() || (groupID != nil && *groupID == "") {
		return fmt.Errorf("move API key: %w", ErrInvalid)
	}
	if domain.IsInternalKey(id) {
		return ErrNotFound
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := currentKeyGroupTx(ctx, tx, id); err != nil {
			return err
		}
		if groupID == nil {
			_, err := tx.ExecContext(ctx, `UPDATE api_keys SET group_id=NULL WHERE id=? AND deleted_at IS NULL`, id)
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM account_groups WHERE id=?", *groupID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrUnknownGroup
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET group_id=?,account_assignment_scope_enabled=1
 WHERE id=? AND deleted_at IS NULL`, *groupID, id); err != nil {
			return err
		}
		rules, err := groupRulesTx(ctx, tx, *groupID)
		if err != nil {
			return err
		}
		return syncKeyLimitsTx(ctx, tx, id, rules, now)
	})
}

// SetAPIKeyLimits replaces the limits of one ungrouped key in one write job
// with the effect of SaveAPIKey with those limits; a grouped key keeps its
// group limits and fails with domain.ErrKeyInGroup. An empty list removes
// every limit.
func (s *Store) SetAPIKeyLimits(ctx context.Context, id string, limits []domain.LimitRule, now time.Time) error {
	if id == "" || now.IsZero() || limits == nil {
		return fmt.Errorf("API key limits: %w", ErrInvalid)
	}
	if domain.IsInternalKey(id) {
		return ErrNotFound
	}
	if err := validateRules(limits); err != nil {
		return err
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		group, err := currentKeyGroupTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if group.Valid {
			return domain.ErrKeyInGroup
		}
		return syncKeyLimitsTx(ctx, tx, id, limits, now)
	})
}

// currentKeyGroupTx returns the group of a non-deleted key, or ErrNotFound.
func currentKeyGroupTx(ctx context.Context, tx *sql.Tx, id string) (sql.NullString, error) {
	var group sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT group_id FROM api_keys WHERE id=? AND deleted_at IS NULL`, id).Scan(&group)
	if errors.Is(err, sql.ErrNoRows) {
		return group, ErrNotFound
	}
	return group, err
}
