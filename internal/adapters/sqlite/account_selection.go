package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"codex-lb/internal/domain"
)

// AccountSelectionSnapshot reads the quota windows, credit status, latest
// quota refusal and additional quotas of the given accounts from one read
// transaction with a fixed number of queries. Every requested account has an
// entry; the values equal the per-account loaders.
func (s *Store) AccountSelectionSnapshot(ctx context.Context, accountIDs []string) (map[string]domain.AccountSelectionState, error) {
	states := make(map[string]domain.AccountSelectionState, len(accountIDs))
	for _, id := range accountIDs {
		states[id] = domain.AccountSelectionState{Quotas: []domain.AccountQuota{}, Additional: []domain.AccountAdditionalQuota{}}
	}
	if len(accountIDs) == 0 {
		return states, nil
	}
	ids, err := json.Marshal(accountIDs)
	if err != nil {
		return nil, err
	}
	err = s.readSnapshot(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT account_id,window,used_percent,reset_at,window_minutes,observed_at
 FROM account_quotas WHERE account_id IN (SELECT value FROM json_each(?)) ORDER BY account_id,window`, string(ids))
		if err != nil {
			return err
		}
		for rows.Next() {
			var q domain.AccountQuota
			var reset, minutes sql.NullInt64
			var observed int64
			if err := rows.Scan(&q.AccountID, &q.Window, &q.UsedPercent, &reset, &minutes, &observed); err != nil {
				rows.Close()
				return err
			}
			q.ResetAt, q.WindowMinutes, q.ObservedAt = optionalTime(reset), optionalInt(minutes), fromMillis(observed)
			state := states[q.AccountID]
			state.Quotas = append(state.Quotas, q)
			states[q.AccountID] = state
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT account_id,credits_has,credits_unlimited,credits_balance,observed_at
 FROM account_usage_credits WHERE account_id IN (SELECT value FROM json_each(?))`, string(ids))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var has, unlimited sql.NullBool
			var balance sql.NullFloat64
			var observed int64
			if err := rows.Scan(&id, &has, &unlimited, &balance, &observed); err != nil {
				rows.Close()
				return err
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
			state := states[id]
			state.Credits = credits
			states[id] = state
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT account_id,max(blocked_at) FROM (
 SELECT o.account_id,r.updated_at AS blocked_at FROM account_outcomes o
 JOIN usage_reservations r ON r.rowid=o.reservation_rowid
 WHERE o.account_id IN (SELECT value FROM json_each(?)) AND r.status='failed'
 UNION ALL
 SELECT b.account_id,b.blocked_at FROM account_quota_block_evidence b
 WHERE b.account_id IN (SELECT value FROM json_each(?))
 ) GROUP BY account_id`, string(ids), string(ids))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var blocked sql.NullInt64
			if err := rows.Scan(&id, &blocked); err != nil {
				rows.Close()
				return err
			}
			state := states[id]
			state.RefusalAt = optionalTime(blocked)
			states[id] = state
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT account_id,quota_key,window,limit_name,metered_feature,
 used_percent,reset_at,window_minutes,observed_at FROM account_additional_quotas
 WHERE account_id IN (SELECT value FROM json_each(?)) ORDER BY account_id,quota_key,window`, string(ids))
		if err != nil {
			return err
		}
		for rows.Next() {
			var q domain.AccountAdditionalQuota
			var reset, minutes sql.NullInt64
			var observed int64
			if err := rows.Scan(&q.AccountID, &q.QuotaKey, &q.Window, &q.LimitName, &q.MeteredFeature,
				&q.UsedPercent, &reset, &minutes, &observed); err != nil {
				rows.Close()
				return err
			}
			q.ResetAt, q.WindowMinutes, q.ObservedAt = optionalTime(reset), optionalInt(minutes), fromMillis(observed)
			state := states[q.AccountID]
			state.Additional = append(state.Additional, q)
			states[q.AccountID] = state
		}
		return closeRows(rows)
	})
	if err != nil {
		return nil, err
	}
	return states, nil
}

func optionalInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	value := int(v.Int64)
	return &value
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	return err
}
