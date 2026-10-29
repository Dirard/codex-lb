package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

// notDeletedAccount is the account predicate of every dashboard list.
const notDeletedAccount = "NOT(a.status='deactivated' AND a.deactivation_reason='deleted')"

// OverviewEntities reads, in one read snapshot and a fixed number of
// statements, the non-deleted accounts with their current quotas, credit
// status, quota refusal time and group memberships, the groups, and the
// non-deleted, non-internal keys with their active limits. Expired limit
// windows are projected to zero with the advanced reset, without writes.
func (s *Store) OverviewEntities(ctx context.Context, now time.Time) (domain.OverviewEntities, error) {
	var result domain.OverviewEntities
	if now.IsZero() {
		return result, ErrInvalid
	}
	err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
		accounts, err := listAccountHealth(ctx, tx)
		if err != nil {
			return err
		}
		groups, err := listGroupRefs(ctx, tx)
		if err != nil {
			return err
		}
		keys, err := listKeyRows(ctx, tx, now)
		if err != nil {
			return err
		}
		result = domain.OverviewEntities{Accounts: accounts, Groups: groups, Keys: keys}
		return nil
	})
	return result, err
}

// listAccountHealth is the batch read of accounts, quotas, credits, quota
// refusals and memberships: five statements for any number of accounts.
func listAccountHealth(ctx context.Context, tx *sql.Tx) ([]domain.OverviewAccount, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts a WHERE "+notDeletedAccount+" ORDER BY a.created_at,a.id")
	if err != nil {
		return nil, err
	}
	var accounts []domain.OverviewAccount
	index := map[string]int{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[account.ID] = len(accounts)
		accounts = append(accounts, domain.OverviewAccount{Account: account, Quotas: []domain.AccountQuota{}, GroupIDs: []string{}})
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT q.account_id,q.window,q.used_percent,q.reset_at,q.window_minutes,q.observed_at
 FROM account_quotas q JOIN accounts a ON a.id=q.account_id WHERE `+notDeletedAccount+` ORDER BY q.account_id,q.window`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q domain.AccountQuota
		var reset, minutes sql.NullInt64
		var observed int64
		if err := rows.Scan(&q.AccountID, &q.Window, &q.UsedPercent, &reset, &minutes, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		q.ResetAt, q.WindowMinutes, q.ObservedAt = optionalTime(reset), optionalInt(minutes), fromMillis(observed)
		if i, ok := index[q.AccountID]; ok {
			accounts[i].Quotas = append(accounts[i].Quotas, q)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT account_id,credits_has,credits_unlimited,credits_balance,observed_at FROM account_usage_credits`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var has, unlimited sql.NullBool
		var balance sql.NullFloat64
		var observed int64
		if err := rows.Scan(&id, &has, &unlimited, &balance, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		credits := &domain.AccountCreditStatus{AccountID: id, ObservedAt: fromMillis(observed)}
		if has.Valid {
			credits.Has = &has.Bool
		}
		if unlimited.Valid {
			credits.Unlimited = &unlimited.Bool
		}
		if balance.Valid {
			credits.Balance = &balance.Float64
		}
		accounts[i].Credits = credits
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	// Refusal times matter only for quota-exceeded accounts (see
	// EffectiveAccountQuotaStatus).
	rows, err = tx.QueryContext(ctx, `SELECT account_id,max(blocked_at) FROM (
 SELECT o.account_id,r.updated_at AS blocked_at FROM account_outcomes o
 JOIN usage_reservations r ON r.rowid=o.reservation_rowid
 WHERE o.account_id IN (SELECT id FROM accounts WHERE status='quota_exceeded') AND r.status='failed'
 UNION ALL
 SELECT b.account_id,b.blocked_at FROM account_quota_block_evidence b
 WHERE b.account_id IN (SELECT id FROM accounts WHERE status='quota_exceeded')
 ) GROUP BY account_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var blocked sql.NullInt64
		if err := rows.Scan(&id, &blocked); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			accounts[i].RefusalAt = optionalTime(blocked)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT account_id,group_id FROM account_group_accounts ORDER BY account_id,group_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var account, group string
		if err := rows.Scan(&account, &group); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[account]; ok {
			accounts[i].GroupIDs = append(accounts[i].GroupIDs, group)
		}
	}
	return accounts, closeRows(rows)
}

func listGroupRefs(ctx context.Context, tx *sql.Tx) ([]domain.OverviewGroupRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM account_groups ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	var groups []domain.OverviewGroupRef
	for rows.Next() {
		var group domain.OverviewGroupRef
		if err := rows.Scan(&group.ID, &group.Name); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, closeRows(rows)
}

// listKeyRows reads the non-deleted, non-internal keys and their active
// limits in two statements.
func listKeyRows(ctx context.Context, tx *sql.Tx, now time.Time) ([]domain.OverviewKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,key_prefix,group_id,is_active,expires_at,last_used_at,created_at
 FROM api_keys WHERE deleted_at IS NULL AND id NOT IN (?,?) ORDER BY created_at,id`, domain.LocalProxyKeyID, domain.WarmupKeyID)
	if err != nil {
		return nil, err
	}
	var keys []domain.OverviewKey
	index := map[string]int{}
	for rows.Next() {
		var key domain.OverviewKey
		var group sql.NullString
		var expires, lastUsed sql.NullInt64
		var created int64
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &group, &key.IsActive, &expires, &lastUsed, &created); err != nil {
			rows.Close()
			return nil, err
		}
		key.GroupID, key.ExpiresAt, key.LastUsedAt, key.CreatedAt = optionalString(group), optionalTime(expires), optionalTime(lastUsed), fromMillis(created)
		key.Limits = []domain.LimitRule{}
		index[key.ID] = len(keys)
		keys = append(keys, key)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT l.api_key_id,l.id,l.limit_type,l.limit_window,l.model_filter,l.max_value,l.current_value,l.reset_at
 FROM api_key_limits l WHERE l.is_active=1 ORDER BY l.api_key_id,l.limit_type,l.limit_window,l.model_filter`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var keyID, model string
		var rule domain.LimitRule
		var reset int64
		if err := rows.Scan(&keyID, &rule.ID, &rule.Type, &rule.Window, &model, &rule.MaxValue, &rule.CurrentValue, &reset); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := index[keyID]
		if !ok {
			continue
		}
		rule.ModelFilter = optionalModel(model)
		rule.ResetAt = fromMillis(reset)
		if !rule.ResetAt.After(now) {
			duration, err := rule.Window.Duration()
			if err != nil {
				rows.Close()
				return nil, err
			}
			rule.CurrentValue = 0
			rule.ResetAt = fromMillis(advanceReset(reset, now, duration))
		}
		keys[i].Limits = append(keys[i].Limits, rule)
	}
	return keys, closeRows(rows)
}

// ShellSnapshot reads the stored part of the dashboard shell status in one
// snapshot: the non-deleted accounts, the newest quota observation of
// non-deleted ChatGPT accounts and the usage rollup state.
func (s *Store) ShellSnapshot(ctx context.Context) (domain.ShellSnapshot, error) {
	var result domain.ShellSnapshot
	err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts a WHERE "+notDeletedAccount+" ORDER BY a.created_at,a.id")
		if err != nil {
			return err
		}
		for rows.Next() {
			account, err := scanAccount(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result.Accounts = append(result.Accounts, account)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT max(q.observed_at) FROM account_quotas q JOIN accounts a ON a.id=q.account_id
 WHERE a.kind='chatgpt' AND `+notDeletedAccount).Scan(&last); err != nil {
			return err
		}
		result.LastSyncAt = optionalTime(last)
		status, err := usageRollupStatus(ctx, tx)
		result.UsageFoldedAt, result.RollupsUsable = status.FoldedAt, status.Usable
		return err
	})
	return result, err
}

// usageRollupStatus reports the last fold time and whether reads in this
// snapshot would use the rollups.
func usageRollupStatus(ctx context.Context, tx *sql.Tx) (domain.UsageRollupStatus, error) {
	var status domain.UsageRollupStatus
	var updated int64
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM usage_rollup_state WHERE id=1`).Scan(&updated); err != nil {
		return status, err
	}
	if updated > 0 {
		status.FoldedAt = optionalTime(sql.NullInt64{Int64: updated, Valid: true})
	}
	reader, err := openRollupRead(ctx, tx)
	status.Usable = reader != nil
	return status, err
}

// Directory lists every non-deleted, non-internal key, every group with its
// non-deleted member accounts and keys, and every non-deleted account, in
// three statements.
func (s *Store) Directory(ctx context.Context, now time.Time) (domain.Directory, error) {
	result := domain.Directory{Keys: []domain.DirectoryKey{}, Groups: []domain.DirectoryGroup{}, Accounts: []domain.DirectoryAccount{}}
	err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,key_prefix,group_id,is_active,expires_at FROM api_keys
 WHERE deleted_at IS NULL AND id NOT IN (?,?) ORDER BY name,id`, domain.LocalProxyKeyID, domain.WarmupKeyID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key domain.OverviewKey
			var group sql.NullString
			var expires sql.NullInt64
			if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &group, &key.IsActive, &expires); err != nil {
				rows.Close()
				return err
			}
			key.GroupID, key.ExpiresAt = optionalString(group), optionalTime(expires)
			result.Keys = append(result.Keys, domain.DirectoryKey{ID: key.ID, Name: key.Name, Prefix: key.Prefix,
				GroupID: key.GroupID, Status: key.Status(now)})
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT g.id,g.name,
 (SELECT count(*) FROM account_group_accounts m JOIN accounts a ON a.id=m.account_id WHERE m.group_id=g.id AND `+notDeletedAccount+`),
 (SELECT count(*) FROM api_keys k WHERE k.group_id=g.id AND k.deleted_at IS NULL AND k.id NOT IN (?,?))
 FROM account_groups g ORDER BY g.name,g.id`, domain.LocalProxyKeyID, domain.WarmupKeyID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var group domain.DirectoryGroup
			if err := rows.Scan(&group.ID, &group.Name, &group.AccountCount, &group.KeyCount); err != nil {
				rows.Close()
				return err
			}
			result.Groups = append(result.Groups, group)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT a.id,a.alias,a.email,a.status,a.kind FROM accounts a WHERE `+notDeletedAccount+`
 ORDER BY CASE WHEN a.alias!='' THEN a.alias ELSE a.email END,a.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var account domain.DirectoryAccount
			var alias string
			if err := rows.Scan(&account.ID, &alias, &account.Email, &account.Status, &account.Kind); err != nil {
				rows.Close()
				return err
			}
			account.Label = alias
			if account.Label == "" {
				account.Label = account.Email
			}
			if account.Label == "" {
				account.Label = account.ID
			}
			result.Accounts = append(result.Accounts, account)
		}
		return closeRows(rows)
	})
	return result, err
}
