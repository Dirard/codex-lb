package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DeleteAccount revokes access atomically; history cleanup can resume after a
// restart. Financial receipts outlive the account and are never revoked here.
func (s *Store) DeleteAccount(ctx context.Context, id string, deleteHistory bool) error {
	if id == "" {
		return ErrInvalid
	}
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		return markAccountDeletedTx(ctx, tx, id, deleteHistory)
	})
	if err != nil {
		return err
	}
	_, err = s.CleanupDeletedAccounts(ctx, 1000)
	return err
}

func markAccountDeletedTx(ctx context.Context, tx *sql.Tx, id string, deleteHistory bool) error {
	var generation int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM accounts WHERE id=?", id).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_deletions(account_id,generation,delete_history)
 VALUES(?,?,?) ON CONFLICT(account_id,generation) DO NOTHING`, id, generation, boolInt(deleteHistory)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET status='deactivated',deactivation_reason='deleted',
 security_work_authorized=0,limit_warmup_enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	for _, table := range []string{
		"account_credentials", "account_group_accounts", "api_key_accounts", "api_key_sources",
		"account_quotas", "account_usage_credits", "account_quota_block_evidence",
		"account_additional_quota_sync", "account_additional_quotas", "account_usage_observation",
		"account_outcomes", "continuations", "codex_resource_owners", "affinity_bindings",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE account_id=?", id); err != nil {
			return err
		}
	}
	return removeAutomationAccountTx(ctx, tx, id)
}

// V30 deletion left credentials and access rows behind. Upgrade revokes them
// through the same boundary as a new deletion; the existing history is retained.
func migrateDeletedAccountsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM accounts WHERE status='deactivated' AND deactivation_reason='deleted'")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := markAccountDeletedTx(ctx, tx, id, false); err != nil {
			return err
		}
	}
	return nil
}

func removeAutomationAccountTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,account_ids FROM automation_jobs WHERE account_scope_all=0")
	if err != nil {
		return err
	}
	type changedJob struct{ id, accounts string }
	var changed []changedJob
	for rows.Next() {
		var id, encoded string
		if err := rows.Scan(&id, &encoded); err != nil {
			rows.Close()
			return err
		}
		ids := splitCSV(encoded)
		kept := ids[:0]
		for _, id := range ids {
			if id != accountID {
				kept = append(kept, id)
			}
		}
		if len(kept) != len(ids) {
			changed = append(changed, changedJob{id, strings.Join(kept, ",")})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, job := range changed {
		// An empty explicit scope remains explicit; it never becomes all-accounts.
		if _, err := tx.ExecContext(ctx, "UPDATE automation_jobs SET account_ids=?,updated_at=? WHERE id=?",
			job.accounts, time.Now().UTC().UnixMilli(), job.id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE automation_runs SET status='failed',finished_at=?,
 error_code='account_deleted',error_message='Account was deleted' WHERE account_id=? AND status='pending'`,
		time.Now().UTC().UnixMilli(), accountID)
	return err
}
