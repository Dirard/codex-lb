package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func groupRulesTx(ctx context.Context, tx *sql.Tx, groupID string) ([]domain.LimitRule, error) {
	rows, err := tx.QueryContext(ctx, `SELECT limit_type,limit_window,model_filter,max_value FROM group_limits
 WHERE group_id=? ORDER BY limit_type,limit_window,model_filter`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]domain.LimitRule, 0)
	for rows.Next() {
		var rule domain.LimitRule
		var model string
		if err := rows.Scan(&rule.Type, &rule.Window, &model, &rule.MaxValue); err != nil {
			return nil, err
		}
		rule.ModelFilter = optionalModel(model)
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func syncKeyLimitsTx(ctx context.Context, tx *sql.Tx, keyID string, rules []domain.LimitRule, now time.Time) error {
	if err := validateRules(rules); err != nil {
		return err
	}
	activeIDs := make([]int64, 0, len(rules))
	for _, rule := range rules {
		model := modelFilter(rule.ModelFilter)
		var id, current, reset int64
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT id,current_value,reset_at,is_active FROM api_key_limits
 WHERE api_key_id=? AND limit_type=? AND limit_window=? AND model_filter=?`,
			keyID, rule.Type, rule.Window, model).Scan(&id, &current, &reset, &active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		duration, _ := rule.Window.Duration()
		if errors.Is(err, sql.ErrNoRows) {
			reset = millis(now.Add(duration))
			backfillFrom := now.Add(-duration)
			current, err = backfillLimitTx(ctx, tx, keyID, rule, backfillFrom, now)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO api_key_limits
	 (api_key_id,limit_type,limit_window,model_filter,max_value,current_value,reset_at,is_active,
	 backfill_from,backfill_until) VALUES(?,?,?,?,?,?,?,1,?,?)`, keyID, rule.Type, rule.Window,
				model, rule.MaxValue, current, reset, millis(backfillFrom), millis(now))
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			activeIDs = append(activeIDs, id)
			continue
		}
		resetExpired := reset <= millis(now)
		if resetExpired {
			reset = advanceReset(reset, now, duration)
			current = 0
		}
		var backfillFrom, backfillUntil any
		if !active {
			start := fromMillis(reset).Add(-duration)
			current, err = backfillLimitTx(ctx, tx, keyID, rule, start, now)
			if err != nil {
				return err
			}
			backfillFrom, backfillUntil = millis(start), millis(now)
		}
		query := `UPDATE api_key_limits SET max_value=?,current_value=?,reset_at=?,is_active=1`
		args := []any{rule.MaxValue, current, reset}
		if resetExpired || !active {
			query += `,backfill_from=?,backfill_until=?`
			args = append(args, backfillFrom, backfillUntil)
		}
		query += ` WHERE id=?`
		args = append(args, id)
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		activeIDs = append(activeIDs, id)
	}
	query := "UPDATE api_key_limits SET is_active=0 WHERE api_key_id=?"
	args := []any{keyID}
	if len(activeIDs) != 0 {
		query += " AND id NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(activeIDs)), ",") + ")"
		for _, id := range activeIDs {
			args = append(args, id)
		}
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func advanceReset(reset int64, now time.Time, duration time.Duration) int64 {
	step := duration.Milliseconds()
	return reset + ((millis(now)-reset)/step+1)*step
}

func backfillLimitTx(ctx context.Context, tx *sql.Tx, keyID string, rule domain.LimitRule, start, end time.Time) (int64, error) {
	column := "0"
	switch rule.Type {
	case domain.LimitTotalTokens:
		column = "input_tokens+output_tokens"
	case domain.LimitInputTokens:
		column = "input_tokens"
	case domain.LimitOutputTokens:
		column = "output_tokens"
	case domain.LimitCostUSD:
		column = "cost_microdollars"
	}
	query := "SELECT coalesce(sum(" + column + "),0) FROM usage_events WHERE api_key_id=? AND request_kind='normal' AND requested_at>=? AND requested_at<?"
	args := []any{keyID, millis(start), millis(end)}
	if rule.ModelFilter != nil {
		query += " AND model=?"
		args = append(args, *rule.ModelFilter)
	}
	var amount int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&amount)
	return amount, err
}
