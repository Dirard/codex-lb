package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"codex-lb/internal/domain"
)

// SaveAccountUsageSnapshot commits one provider observation as short SQLite
// work. An absent credit/additional field leaves prior explicit evidence alone.
func (s *Store) SaveAccountUsageSnapshot(ctx context.Context, snapshot domain.AccountUsageSnapshot) error {
	if snapshot.AccountID == "" || snapshot.ObservedAt.IsZero() {
		return ErrInvalid
	}
	if snapshot.ReplaceQuotaWindows {
		if len(snapshot.Quotas) == 0 {
			return ErrInvalid
		}
		seen := make(map[string]bool, 3)
		for _, quota := range snapshot.Quotas {
			if quota.AccountID != snapshot.AccountID || !quota.ObservedAt.Equal(snapshot.ObservedAt) ||
				(quota.Window != "primary" && quota.Window != "secondary" && quota.Window != "monthly") || seen[quota.Window] {
				return ErrInvalid
			}
			seen[quota.Window] = true
		}
	}
	if snapshot.Credits != nil {
		c := snapshot.Credits
		if c.AccountID != snapshot.AccountID || !c.ObservedAt.Equal(snapshot.ObservedAt) ||
			c.Has == nil && c.Unlimited == nil && c.Balance == nil ||
			c.Balance != nil && (math.IsNaN(*c.Balance) || math.IsInf(*c.Balance, 0)) {
			return ErrInvalid
		}
	}
	if !snapshot.AdditionalReported && len(snapshot.AdditionalQuotas) != 0 {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(snapshot.AdditionalQuotas))
	for _, q := range snapshot.AdditionalQuotas {
		key := q.QuotaKey + "\x00" + q.Window
		if q.AccountID != snapshot.AccountID || q.QuotaKey == "" || (q.Window != "primary" && q.Window != "secondary") ||
			math.IsNaN(q.UsedPercent) || math.IsInf(q.UsedPercent, 0) || q.UsedPercent < 0 ||
			!q.ObservedAt.Equal(snapshot.ObservedAt) || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	pendingPlan := false
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		pendingPlan, err = prepareUsageIdentityTx(ctx, tx, snapshot)
		if err != nil || pendingPlan {
			return err
		}
		if snapshot.ReplaceQuotaWindows {
			if err := replaceAccountQuotaWindowsTx(ctx, tx, snapshot); err != nil {
				return err
			}
		}
		for _, quota := range snapshot.Quotas {
			if quota.AccountID != snapshot.AccountID {
				return ErrInvalid
			}
			if err := saveAccountQuotaTx(ctx, tx, quota); err != nil {
				return err
			}
		}
		if snapshot.Credits != nil {
			c := snapshot.Credits
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_usage_credits
 (account_id,credits_has,credits_unlimited,credits_balance,observed_at) VALUES(?,?,?,?,?)
 ON CONFLICT(account_id) DO UPDATE SET credits_has=excluded.credits_has,
 credits_unlimited=excluded.credits_unlimited,credits_balance=excluded.credits_balance,
 observed_at=excluded.observed_at WHERE excluded.observed_at>=account_usage_credits.observed_at`,
				snapshot.AccountID, nullableBool(c.Has), nullableBool(c.Unlimited), c.Balance, millis(c.ObservedAt)); err != nil {
				return err
			}
		}
		if !snapshot.AdditionalReported {
			return nil
		}
		var previous int64
		err = tx.QueryRowContext(ctx, "SELECT observed_at FROM account_additional_quota_sync WHERE account_id=?", snapshot.AccountID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && previous > millis(snapshot.ObservedAt) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM account_additional_quotas WHERE account_id=?", snapshot.AccountID); err != nil {
			return err
		}
		for _, q := range snapshot.AdditionalQuotas {
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_additional_quotas
 (account_id,quota_key,window,limit_name,metered_feature,used_percent,reset_at,window_minutes,observed_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, q.AccountID, q.QuotaKey, q.Window, q.LimitName, q.MeteredFeature,
				q.UsedPercent, optionalMillis(q.ResetAt), nullableIntValue(q.WindowMinutes), millis(q.ObservedAt)); err != nil {
				return fmt.Errorf("save additional quota: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_additional_quota_sync(account_id,observed_at) VALUES(?,?)
 ON CONFLICT(account_id) DO UPDATE SET observed_at=excluded.observed_at`, snapshot.AccountID, millis(snapshot.ObservedAt))
		return err
	})
	if err != nil {
		return err
	}
	if pendingPlan {
		return domain.ErrPlanConfirmationPending
	}
	return nil
}

// Replace only current standard windows after the observation fences passed.
func replaceAccountQuotaWindowsTx(ctx context.Context, tx *sql.Tx, snapshot domain.AccountUsageSnapshot) error {
	for _, quota := range snapshot.Quotas {
		if quota.Window != "secondary" || quota.WindowMinutes == nil || *quota.WindowMinutes != 7*24*60 {
			continue
		}
		var mislabeled bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_quotas
 WHERE account_id=? AND window='primary' AND window_minutes=10080 AND observed_at<=?)`,
			snapshot.AccountID, millis(snapshot.ObservedAt)).Scan(&mislabeled); err != nil {
			return err
		}
		if mislabeled {
			// Repair the known old label once; retain all values and financial history.
			if _, err := tx.ExecContext(ctx, `UPDATE account_quota_history SET window='secondary'
 WHERE account_id=? AND window='primary' AND window_minutes=10080 AND observed_at<=?`,
				snapshot.AccountID, millis(snapshot.ObservedAt)); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM account_quotas WHERE account_id=?
 AND window IN ('primary','secondary','monthly') AND observed_at<=?`, snapshot.AccountID, millis(snapshot.ObservedAt))
	return err
}

func nullableBool(v *bool) any {
	if v == nil {
		return nil
	}
	return boolInt(*v)
}

func nullableIntValue(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func (s *Store) LoadAccountCreditStatus(ctx context.Context, accountID string) (*domain.AccountCreditStatus, error) {
	return loadAccountCreditStatus(ctx, s.readDB, accountID)
}

func loadAccountCreditStatus(ctx context.Context, query quotaRowQuerier, accountID string) (*domain.AccountCreditStatus, error) {
	var has, unlimited sql.NullBool
	var balance sql.NullFloat64
	var observed int64
	err := query.QueryRowContext(ctx, `SELECT credits_has,credits_unlimited,credits_balance,observed_at
 FROM account_usage_credits WHERE account_id=?`, accountID).Scan(&has, &unlimited, &balance, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := &domain.AccountCreditStatus{AccountID: accountID, ObservedAt: fromMillis(observed)}
	if has.Valid {
		status.Has = &has.Bool
	}
	if unlimited.Valid {
		status.Unlimited = &unlimited.Bool
	}
	if balance.Valid {
		status.Balance = &balance.Float64
	}
	return status, nil
}

// LoadAccountQuotaRefusalAt returns the latest settled Go refusal or imported
// legacy blocked_at marker used by the same cooldown gate.
func (s *Store) LoadAccountQuotaRefusalAt(ctx context.Context, accountID string) (*time.Time, error) {
	return loadAccountQuotaRefusalAt(ctx, s.readDB, accountID)
}

type quotaRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAccountQuotaRefusalAt(ctx context.Context, query quotaRowQuerier, accountID string) (*time.Time, error) {
	var blocked sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT max(blocked_at) FROM (
 SELECT r.updated_at AS blocked_at FROM account_outcomes o
 JOIN usage_reservations r ON r.rowid=o.reservation_rowid
 WHERE o.account_id=? AND r.status='failed'
 UNION ALL
 SELECT b.blocked_at FROM account_quota_block_evidence b WHERE b.account_id=?
 )`, accountID, accountID).Scan(&blocked)
	if err != nil {
		return nil, err
	}
	if !blocked.Valid {
		return nil, nil
	}
	at := fromMillis(blocked.Int64)
	return &at, nil
}

func (s *Store) ListAccountAdditionalQuotas(ctx context.Context, accountID string) ([]domain.AccountAdditionalQuota, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT quota_key,window,limit_name,metered_feature,
 used_percent,reset_at,window_minutes,observed_at FROM account_additional_quotas
 WHERE account_id=? ORDER BY quota_key,window`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.AccountAdditionalQuota, 0)
	for rows.Next() {
		q := domain.AccountAdditionalQuota{AccountID: accountID}
		var reset, minutes sql.NullInt64
		var observed int64
		if err := rows.Scan(&q.QuotaKey, &q.Window, &q.LimitName, &q.MeteredFeature,
			&q.UsedPercent, &reset, &minutes, &observed); err != nil {
			return nil, err
		}
		q.ResetAt = optionalTime(reset)
		if minutes.Valid {
			m := int(minutes.Int64)
			q.WindowMinutes = &m
		}
		q.ObservedAt = fromMillis(observed)
		result = append(result, q)
	}
	return result, rows.Err()
}
