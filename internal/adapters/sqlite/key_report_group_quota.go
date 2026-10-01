package sqlite

import (
	"context"
	"database/sql"
	"time"

	"codex-lb/internal/domain"
)

// keyReportGroupQuotaTx aggregates only the caller's group account quota in the
// authorized read snapshot. Account identity never leaves this adapter.
func keyReportGroupQuotaTx(ctx context.Context, tx *sql.Tx, keyID string, now time.Time) (*domain.KeyReportGroupQuota, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.plan_type,q.window,q.used_percent,q.reset_at,q.window_minutes,q.observed_at,
 credits.credits_balance,credits.credits_unlimited
 FROM api_keys caller
 JOIN account_group_accounts members ON members.group_id=caller.group_id
 JOIN accounts a ON a.id=members.account_id
 LEFT JOIN account_quotas q ON q.account_id=a.id
 LEFT JOIN account_usage_credits credits ON credits.account_id=a.id
 WHERE caller.id=? AND a.kind='chatgpt' AND NOT(a.status='deactivated' AND a.deactivation_reason='deleted')
 ORDER BY a.id,q.window`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &domain.KeyReportGroupQuota{Windows: []domain.KeyReportGroupQuotaWindow{}}
	type total struct {
		used, capacity float64
		accounts       int
	}
	totals := map[string]total{}
	var lastID, plan string
	var quotas []domain.AccountQuota
	add := func(window string, quota *domain.AccountQuota) {
		if quota == nil || quota.ResetAt != nil && !quota.ResetAt.After(now) {
			return
		}
		capacity := domain.SubscriptionCreditCapacity(plan, window)
		if capacity == nil || *capacity <= 0 {
			return
		}
		summary := totals[window]
		summary.capacity += *capacity
		summary.used += *capacity * max(0, min(100, quota.UsedPercent)) / 100
		summary.accounts++
		totals[window] = summary
	}
	flush := func() {
		if lastID == "" {
			return
		}
		result.AccountCount++
		primary, long := domain.SubscriptionQuotaWindows(plan, quotas)
		add("primary", primary)
		if long != nil && long.Window == "monthly" {
			add("monthly", long)
		} else {
			add("secondary", long)
		}
	}
	for rows.Next() {
		var id, rowPlan string
		var window sql.NullString
		var used sql.NullFloat64
		var balance sql.NullFloat64
		var unlimited sql.NullBool
		var reset, minutes, observed sql.NullInt64
		if err := rows.Scan(&id, &rowPlan, &window, &used, &reset, &minutes, &observed, &balance, &unlimited); err != nil {
			return nil, err
		}
		if id != lastID {
			flush()
			lastID, plan, quotas = id, rowPlan, quotas[:0]
			if unlimited.Valid && unlimited.Bool {
				result.CreditsUnlimited = true
				result.CreditsKnownAccountCount++
			} else if balance.Valid {
				if result.PurchasedCredits == nil {
					result.PurchasedCredits = new(float64)
				}
				*result.PurchasedCredits += max(0, balance.Float64)
				result.CreditsKnownAccountCount++
			}
		}
		if window.Valid {
			quota := domain.AccountQuota{Window: window.String, UsedPercent: used.Float64,
				ResetAt: optionalTime(reset), ObservedAt: fromMillis(observed.Int64)}
			if minutes.Valid {
				value := int(minutes.Int64)
				quota.WindowMinutes = &value
			}
			quotas = append(quotas, quota)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	for _, window := range []string{"primary", "secondary", "monthly"} {
		summary := totals[window]
		if summary.capacity > 0 {
			result.Windows = append(result.Windows, domain.KeyReportGroupQuotaWindow{
				Window: window, UsedPercent: summary.used / summary.capacity * 100, AccountCount: summary.accounts})
		}
	}
	return result, nil
}
