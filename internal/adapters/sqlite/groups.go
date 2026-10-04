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

func validateRules(rules []domain.LimitRule) error {
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		model := ""
		if rule.ModelFilter != nil {
			model = *rule.ModelFilter
		}
		id := string(rule.Type) + "\x00" + string(rule.Window) + "\x00" + model
		if seen[id] {
			return fmt.Errorf("duplicate limit: %w", ErrInvalid)
		}
		seen[id] = true
	}
	return nil
}

func (s *Store) SaveGroup(ctx context.Context, g domain.AccountGroup, now time.Time) error {
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" || g.Name == "" || len(g.Name) > 128 || now.IsZero() {
		return fmt.Errorf("group: %w", ErrInvalid)
	}
	if err := validateRules(g.Limits); err != nil {
		return err
	}
	seenAccounts := make(map[string]bool, len(g.AccountIDs))
	for _, id := range g.AccountIDs {
		if id == "" || seenAccounts[id] {
			return fmt.Errorf("group account ids: %w", ErrInvalid)
		}
		seenAccounts[id] = true
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		for _, id := range g.AccountIDs {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM accounts WHERE id=? AND NOT(status='deactivated' AND deactivation_reason='deleted')", id).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("unknown group account %q: %w", id, ErrInvalid)
				}
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO account_groups(id,name,created_at) VALUES(?,?,?)
 ON CONFLICT(id) DO UPDATE SET name=excluded.name`, g.ID, g.Name, millis(g.CreatedAt))
		if err != nil {
			return fmt.Errorf("save group: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM account_group_accounts WHERE group_id=?", g.ID); err != nil {
			return err
		}
		for _, id := range g.AccountIDs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO account_group_accounts(group_id,account_id) VALUES(?,?)", g.ID, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM group_limits WHERE group_id=?", g.ID); err != nil {
			return err
		}
		for _, rule := range g.Limits {
			if _, err := tx.ExecContext(ctx, `INSERT INTO group_limits(group_id,limit_type,limit_window,model_filter,max_value)
 VALUES(?,?,?,?,?)`, g.ID, rule.Type, rule.Window, modelFilter(rule.ModelFilter), rule.MaxValue); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM api_keys WHERE group_id=? ORDER BY id", g.ID)
		if err != nil {
			return err
		}
		var keyIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			keyIDs = append(keyIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range keyIDs {
			if err := syncKeyLimitsTx(ctx, tx, id, g.Limits, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetGroup(ctx context.Context, id string) (domain.AccountGroup, error) {
	g := domain.AccountGroup{ID: id, AccountIDs: []string{}, Limits: []domain.LimitRule{}}
	var created int64
	err := s.readDB.QueryRowContext(ctx, `SELECT name,created_at,
 (SELECT count(*) FROM api_keys WHERE group_id=account_groups.id) FROM account_groups WHERE id=?`, id).
		Scan(&g.Name, &created, &g.KeyCount)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	g.CreatedAt = fromMillis(created)
	rows, err := s.readDB.QueryContext(ctx, "SELECT account_id FROM account_group_accounts WHERE group_id=? ORDER BY account_id", id)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return g, err
		}
		g.AccountIDs = append(g.AccountIDs, accountID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	rows, err = s.readDB.QueryContext(ctx, `SELECT limit_type,limit_window,model_filter,max_value FROM group_limits
 WHERE group_id=? ORDER BY limit_type,limit_window,model_filter`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var rule domain.LimitRule
		var model string
		if err := rows.Scan(&rule.Type, &rule.Window, &model, &rule.MaxValue); err != nil {
			return g, err
		}
		rule.ModelFilter = optionalModel(model)
		g.Limits = append(g.Limits, rule)
	}
	return g, rows.Err()
}

func (s *Store) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id FROM account_groups ORDER BY name,id")
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
	groups := make([]domain.AccountGroup, 0, len(ids))
	for _, id := range ids {
		g, err := s.GetGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_groups WHERE id=?
 AND NOT EXISTS(SELECT 1 FROM api_keys WHERE group_id=?)`, id, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM account_groups WHERE id=?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return ErrConflict
}

// ResetGroupUsage starts new limit windows for all current, non-deleted member
// keys in one transaction. Historical usage and provider quotas are untouched.
func (s *Store) ResetGroupUsage(ctx context.Context, id string, now time.Time) error {
	if id == "" || now.IsZero() {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM account_groups WHERE id=?", id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM api_keys WHERE group_id=? AND deleted_at IS NULL
 AND id NOT IN (?,?) ORDER BY id`, id, domain.LocalProxyKeyID, domain.WarmupKeyID)
		if err != nil {
			return err
		}
		var keyIDs []string
		for rows.Next() {
			var keyID string
			if err := rows.Scan(&keyID); err != nil {
				rows.Close()
				return err
			}
			keyIDs = append(keyIDs, keyID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, keyID := range keyIDs {
			if err := resetAPIKeyUsageTx(ctx, tx, keyID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func modelFilter(model *string) string {
	if model == nil {
		return ""
	}
	return *model
}
func optionalModel(model string) *string {
	if model == "" {
		return nil
	}
	return &model
}
