package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// KeyReportLimits reads the caller's live membership and safe limit snapshot in
// one read-only transaction. Expired limits are projected without ledger writes.
func (s *Store) KeyReportLimits(ctx context.Context, keyID string, now time.Time) (domain.KeyReportLimitSummary, error) {
	result := domain.KeyReportLimitSummary{Limits: []domain.LimitRule{}}
	if keyID == "" || domain.IsInternalKey(keyID) || now.IsZero() {
		return result, ErrNotFound
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT g.name,k.id,k.name,k.is_active,k.expires_at,
 caller.usage_sections,settings.hide_upstream_quota_from_keys,
 l.id,l.limit_type,l.limit_window,l.model_filter,l.max_value,l.current_value,l.reset_at
 FROM api_keys caller
 JOIN runtime_settings settings ON settings.id=1
 JOIN api_keys k ON k.id=caller.id OR k.group_id=caller.group_id
 LEFT JOIN account_groups g ON g.id=caller.group_id
 LEFT JOIN api_key_limits l ON l.api_key_id=k.id AND l.is_active=1
 WHERE caller.id=? AND caller.deleted_at IS NULL AND caller.is_active=1
 AND (caller.expires_at IS NULL OR caller.expires_at>?)
 AND k.deleted_at IS NULL AND k.id NOT IN (?,?)
 ORDER BY k.created_at,k.id,l.limit_type,l.limit_window,l.model_filter`,
		keyID, millis(now), domain.LocalProxyKeyID, domain.WarmupKeyID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	keys := []domain.KeyReportGroupKey{}
	var sections string
	var hidden bool
	for rows.Next() {
		var group, limitType, window, model sql.NullString
		var expiry, limitID, maximum, current, reset sql.NullInt64
		var key domain.KeyReportGroupKey
		if err := rows.Scan(&group, &key.ID, &key.Name, &key.IsActive, &expiry, &sections, &hidden,
			&limitID, &limitType, &window, &model, &maximum, &current, &reset); err != nil {
			return result, err
		}
		if group.Valid && result.Group == nil {
			result.Group = &domain.KeyReportGroup{Name: group.String}
		}
		if len(keys) == 0 || keys[len(keys)-1].ID != key.ID {
			key.ExpiresAt, key.IsCurrent, key.Limits = optionalTime(expiry), key.ID == keyID, []domain.LimitRule{}
			keys = append(keys, key)
		}
		if !limitID.Valid {
			continue
		}
		rule := domain.LimitRule{ID: limitID.Int64, Type: domain.LimitType(limitType.String), Window: domain.LimitWindow(window.String),
			ModelFilter: optionalModel(model.String), MaxValue: maximum.Int64, CurrentValue: current.Int64, ResetAt: fromMillis(reset.Int64)}
		if !rule.ResetAt.After(now) {
			duration, err := rule.Window.Duration()
			if err != nil {
				return result, err
			}
			rule.CurrentValue = 0
			rule.ResetAt = fromMillis(advanceReset(reset.Int64, now, duration))
		}
		last := &keys[len(keys)-1]
		last.Limits = append(last.Limits, rule)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if len(keys) == 0 {
		return result, ErrNotFound
	}
	for _, key := range keys {
		if key.IsCurrent {
			result.Limits = key.Limits
		}
	}
	if result.Group != nil {
		result.Group.Keys = keys
		if !hidden {
			for _, section := range strings.Split(sections, ",") {
				if strings.TrimSpace(section) == "account_pool_usage" {
					result.Group.AccountQuota, err = keyReportGroupQuotaTx(ctx, tx, keyID, now)
					if err != nil {
						return result, err
					}
					break
				}
			}
		}
	}
	return result, tx.Commit()
}
