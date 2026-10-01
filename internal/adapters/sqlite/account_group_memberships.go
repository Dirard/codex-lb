package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const maxAccountGroupMemberships = 1000

// SetAccountGroups atomically replaces one account's memberships after the
// account and every target group are validated in the same transaction.
func (s *Store) SetAccountGroups(ctx context.Context, accountID string, groupIDs []string) error {
	if accountID == "" || groupIDs == nil || len(groupIDs) > maxAccountGroupMemberships {
		return fmt.Errorf("account groups: %w", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID == "" {
			return fmt.Errorf("account group id: %w", ErrInvalid)
		}
		if _, exists := seen[groupID]; exists {
			return fmt.Errorf("duplicate account group id: %w", ErrInvalid)
		}
		seen[groupID] = struct{}{}
	}

	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM accounts
		 WHERE id=? AND NOT(status='deactivated' AND deactivation_reason='deleted')`, accountID).
			Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if len(groupIDs) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(groupIDs)), ",")
			args := make([]any, len(groupIDs))
			for i, groupID := range groupIDs {
				args[i] = groupID
			}
			if err := tx.QueryRowContext(ctx,
				fmt.Sprintf("SELECT count(*) FROM account_groups WHERE id IN (%s)", placeholders), args...).
				Scan(&exists); err != nil {
				return err
			}
			if exists != len(groupIDs) {
				return ErrNotFound
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM account_group_accounts WHERE account_id=?", accountID); err != nil {
			return err
		}
		for _, groupID := range groupIDs {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO account_group_accounts(group_id,account_id) VALUES(?,?)", groupID, accountID); err != nil {
				return err
			}
		}
		return nil
	})
}
