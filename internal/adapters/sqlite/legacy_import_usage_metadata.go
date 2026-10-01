package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func importLegacyQuotaMetadata(ctx context.Context, src, dst *sql.Tx) error {
	if err := importLegacyQuotaBlockEvidence(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyPurchasedCredits(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyAdditionalQuotas(ctx, src, dst); err != nil {
		return err
	}
	return importLegacyAdditionalRoutingPolicies(ctx, src, dst)
}

func importLegacyQuotaBlockEvidence(ctx context.Context, src, dst *sql.Tx) error {
	var present int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('accounts') WHERE name='blocked_at'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	rows, err := src.QueryContext(ctx, `SELECT id,blocked_at FROM accounts WHERE blocked_at IS NOT NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var seconds int64
		if err := rows.Scan(&accountID, &seconds); err != nil {
			return err
		}
		blocked := time.Unix(seconds, 0).UnixMilli()
		if seconds <= 0 || blocked/1000 != seconds {
			return ErrInvalid
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_quota_block_evidence(account_id,blocked_at) VALUES(?,?)`, accountID, blocked); err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacyAdditionalRoutingPolicies(ctx context.Context, src, dst *sql.Tx) error {
	var present int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dashboard_settings')
 WHERE name='additional_quota_routing_policies_json'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	var raw string
	if err := src.QueryRowContext(ctx, `SELECT additional_quota_routing_policies_json
 FROM dashboard_settings WHERE id=1`).Scan(&raw); err != nil {
		return err
	}
	var saved map[string]string
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return ErrInvalid
	}
	policies := make(map[string]string)
	for key, value := range saved {
		definition, known := domain.KnownAdditionalQuota(key)
		value = strings.ToLower(strings.TrimSpace(value))
		if !known {
			continue // Legacy registry also ignores unknown override keys.
		}
		switch value {
		case "inherit", "normal", "burn_first", "preserve":
			if previous, exists := policies[definition.QuotaKey]; exists && previous != value {
				return ErrInvalid
			}
			policies[definition.QuotaKey] = value
		default:
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(policies)
	if err != nil {
		return err
	}
	_, err = dst.ExecContext(ctx, `UPDATE runtime_settings SET additional_quota_routing_policies=? WHERE id=1`, string(encoded))
	return err
}

func importLegacyPurchasedCredits(ctx context.Context, src, dst *sql.Tx) error {
	var columns int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('usage_history')
 WHERE name IN ('credits_has','credits_unlimited','credits_balance')`).Scan(&columns); err != nil {
		return err
	}
	if columns == 0 {
		return nil
	}
	if columns != 3 {
		return fmt.Errorf("partial legacy credit metadata schema: %w", ErrInvalid)
	}
	rows, err := src.QueryContext(ctx, `SELECT account_id,credits_has,credits_unlimited,credits_balance,recorded_at
 FROM usage_history WHERE credits_has IS NOT NULL OR credits_unlimited IS NOT NULL OR credits_balance IS NOT NULL
 ORDER BY account_id,recorded_at DESC,id DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	previous := ""
	for rows.Next() {
		var accountID string
		var has, unlimited sql.NullBool
		var balance sql.NullFloat64
		var recorded any
		if err := rows.Scan(&accountID, &has, &unlimited, &balance, &recorded); err != nil {
			return err
		}
		if accountID == previous {
			continue
		}
		if balance.Valid && (math.IsNaN(balance.Float64) || math.IsInf(balance.Float64, 0)) {
			return ErrInvalid
		}
		at, err := parseLegacyTime(recorded)
		if err != nil {
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_usage_credits
 (account_id,credits_has,credits_unlimited,credits_balance,observed_at) VALUES(?,?,?,?,?)`,
			accountID, nullableIntBool(has), nullableIntBool(unlimited), nullableFloat(balance), millis(at)); err != nil {
			return err
		}
		previous = accountID
	}
	return rows.Err()
}

func nullableIntBool(v sql.NullBool) any {
	if !v.Valid {
		return nil
	}
	return boolInt(v.Bool)
}

func importLegacyAdditionalQuotas(ctx context.Context, src, dst *sql.Tx) error {
	var present int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='additional_usage_history'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	rows, err := src.QueryContext(ctx, `SELECT account_id,quota_key,limit_name,metered_feature,window,
 used_percent,reset_at,window_minutes,recorded_at FROM additional_usage_history
 ORDER BY account_id,recorded_at DESC,id DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	latest := make(map[string]domain.AccountAdditionalQuota)
	accountLatest := make(map[string]time.Time)
	for rows.Next() {
		var q domain.AccountAdditionalQuota
		var reset, minutes sql.NullInt64
		var recorded any
		if err := rows.Scan(&q.AccountID, &q.QuotaKey, &q.LimitName, &q.MeteredFeature,
			&q.Window, &q.UsedPercent, &reset, &minutes, &recorded); err != nil {
			return err
		}
		q.QuotaKey = domain.CanonicalAdditionalQuotaKey(q.QuotaKey, q.LimitName, q.MeteredFeature)
		if q.AccountID == "" || q.QuotaKey == "" || (q.Window != "primary" && q.Window != "secondary") ||
			math.IsNaN(q.UsedPercent) || math.IsInf(q.UsedPercent, 0) || q.UsedPercent < 0 {
			return ErrInvalid
		}
		at, err := parseLegacyTime(recorded)
		if err != nil {
			return err
		}
		q.ObservedAt = at
		if current, ok := accountLatest[q.AccountID]; !ok || at.After(current) {
			accountLatest[q.AccountID] = at
		}
		if reset.Valid {
			value := time.Unix(reset.Int64, 0).UTC()
			q.ResetAt = &value
		}
		if minutes.Valid {
			value := int(minutes.Int64)
			q.WindowMinutes = &value
		}
		identity := q.AccountID + "\x00" + q.QuotaKey + "\x00" + q.Window
		if previous, ok := latest[identity]; !ok || at.After(previous.ObservedAt) {
			latest[identity] = q
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		q := latest[key]
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_additional_quotas
 (account_id,quota_key,window,limit_name,metered_feature,used_percent,reset_at,window_minutes,observed_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, q.AccountID, q.QuotaKey, q.Window, q.LimitName, q.MeteredFeature,
			q.UsedPercent, optionalMillis(q.ResetAt), nullableIntValue(q.WindowMinutes), millis(q.ObservedAt)); err != nil {
			return err
		}
	}
	accounts := make([]string, 0, len(accountLatest))
	for id := range accountLatest {
		accounts = append(accounts, id)
	}
	sort.Strings(accounts)
	for _, id := range accounts {
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_additional_quota_sync(account_id,observed_at)
 VALUES(?,?)`, id, millis(accountLatest[id])); err != nil {
			return err
		}
	}
	return nil
}
