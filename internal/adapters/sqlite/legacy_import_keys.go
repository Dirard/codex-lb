package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"codex-lb/internal/domain"
)

func importLegacyGroups(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	rows, err := src.QueryContext(ctx, "SELECT id,name,created_at FROM account_groups ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		var created any
		if err := rows.Scan(&id, &name, &created); err != nil {
			rows.Close()
			return err
		}
		at, err := parseLegacyTime(created)
		if err != nil {
			rows.Close()
			return err
		}
		if _, err := dst.ExecContext(ctx, "INSERT INTO account_groups(id,name,created_at) VALUES(?,?,?)",
			id, name, millis(at)); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, "SELECT group_id,account_id FROM account_group_accounts ORDER BY group_id,account_id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var groupID, accountID string
		if err := rows.Scan(&groupID, &accountID); err != nil {
			rows.Close()
			return err
		}
		if _, err := dst.ExecContext(ctx, "INSERT INTO account_group_accounts(group_id,account_id) VALUES(?,?)",
			groupID, accountID); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, `SELECT group_id,limit_type,limit_window,max_value,model_filter
 FROM account_group_limits ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID string
		var rule domain.LimitRule
		var model sql.NullString
		if err := rows.Scan(&groupID, &rule.Type, &rule.Window, &rule.MaxValue, &model); err != nil {
			return err
		}
		if model.Valid {
			rule.ModelFilter = &model.String
		}
		if err := rule.Validate(); err != nil {
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO group_limits
 (group_id,limit_type,limit_window,model_filter,max_value) VALUES(?,?,?,?,?)`,
			groupID, rule.Type, rule.Window, modelFilter(rule.ModelFilter), rule.MaxValue); err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacyKeys(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	rows, err := src.QueryContext(ctx, `SELECT id,name,key_hash,key_prefix,group_id,allowed_models,
 apply_to_codex_model,enforced_model,allowed_reasoning_efforts,enforced_reasoning_effort,
 enforced_service_tier,traffic_class,transport_policy_override,usage_sections,
 account_assignment_scope_enabled,source_assignment_scope_enabled,expires_at,is_active,
 created_at,last_used_at FROM api_keys ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, hash, prefix, traffic, sections string
		var group, allowed, enforced, efforts, reasoning, tier, transport sql.NullString
		var applyCodex, accountScope, sourceScope, active bool
		var expires, created, last any
		if err := rows.Scan(&id, &name, &hash, &prefix, &group, &allowed, &applyCodex, &enforced,
			&efforts, &reasoning, &tier, &traffic, &transport, &sections, &accountScope, &sourceScope,
			&expires, &active, &created, &last); err != nil {
			rows.Close()
			return err
		}
		hashBytes, hashErr := hex.DecodeString(hash)
		if hashErr != nil || len(hashBytes) != 32 || strings.ToLower(hash) != hash {
			rows.Close()
			return fmt.Errorf("legacy key %s hash incompatible: %w", id, ErrInvalid)
		}
		if _, err := decodeList(allowed); err != nil {
			rows.Close()
			return fmt.Errorf("legacy key %s models: %w", id, err)
		}
		if _, err := decodeList(efforts); err != nil {
			rows.Close()
			return fmt.Errorf("legacy key %s efforts: %w", id, err)
		}
		if efforts.Valid && reasoning.Valid {
			rows.Close()
			return fmt.Errorf("legacy key %s conflicting reasoning policy: %w", id, ErrInvalid)
		}
		createdAt, err := parseLegacyTime(created)
		if err != nil {
			rows.Close()
			return err
		}
		expiresAt, err := optionalLegacyMillis(expires)
		if err != nil {
			rows.Close()
			return err
		}
		lastAt, err := optionalLegacyMillis(last)
		if err != nil {
			rows.Close()
			return err
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO api_keys
 (id,name,key_hash,key_prefix,group_id,allowed_models,apply_to_codex_model,
 enforced_model,allowed_reasoning_efforts,enforced_reasoning_effort,enforced_service_tier,
 traffic_class,transport_policy_override,usage_sections,account_assignment_scope_enabled,
 source_assignment_scope_enabled,expires_at,is_active,created_at,last_used_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, name, hash, prefix, nullText(group),
			nullText(allowed), boolInt(applyCodex), nullText(enforced), nullText(efforts),
			nullText(reasoning), nullText(tier), traffic, nullText(transport), sections,
			boolInt(accountScope), boolInt(sourceScope), expiresAt, boolInt(active), millis(createdAt), lastAt)
		if err != nil {
			rows.Close()
			return fmt.Errorf("import legacy key %s: %w", id, err)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, assignment := range []struct{ source, target, idColumn string }{
		{"api_key_accounts", "api_key_accounts", "account_id"},
		{"api_key_model_sources", "api_key_sources", "source_id"},
	} {
		rows, err = src.QueryContext(ctx, "SELECT api_key_id,"+assignment.idColumn+" FROM "+assignment.source)
		if err != nil {
			return err
		}
		for rows.Next() {
			var keyID, id string
			if err := rows.Scan(&keyID, &id); err != nil {
				rows.Close()
				return err
			}
			if _, err := dst.ExecContext(ctx, "INSERT INTO "+assignment.target+"(api_key_id,account_id) VALUES(?,?)",
				keyID, id); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func importLegacyLimits(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	rows, err := src.QueryContext(ctx, `SELECT id,api_key_id,limit_type,limit_window,max_value,
 current_value,model_filter,reset_at FROM api_key_limits ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var keyID string
		var rule domain.LimitRule
		var model sql.NullString
		var reset any
		if err := rows.Scan(&id, &keyID, &rule.Type, &rule.Window, &rule.MaxValue,
			&rule.CurrentValue, &model, &reset); err != nil {
			return err
		}
		if model.Valid {
			rule.ModelFilter = &model.String
		}
		if err := rule.Validate(); err != nil || rule.CurrentValue < 0 {
			return fmt.Errorf("legacy limit %d invalid: %w", id, ErrInvalid)
		}
		resetAt, err := parseLegacyTime(reset)
		if err != nil {
			return err
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO api_key_limits
 (id,api_key_id,limit_type,limit_window,model_filter,max_value,current_value,reset_at,is_active)
 VALUES(?,?,?,?,?,?,?,?,1)`, id, keyID, rule.Type, rule.Window, modelFilter(rule.ModelFilter),
			rule.MaxValue, rule.CurrentValue, millis(resetAt))
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacyReservations(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	rows, err := src.QueryContext(ctx, `SELECT id,api_key_id,model,status,input_tokens,
 output_tokens,cached_input_tokens,cost_microdollars,created_at,updated_at
 FROM api_key_usage_reservations ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, keyID, model, status string
		var input, output, cached, cost sql.NullInt64
		var created, updated any
		if err := rows.Scan(&id, &keyID, &model, &status, &input, &output, &cached, &cost,
			&created, &updated); err != nil {
			rows.Close()
			return err
		}
		if status != "reserved" && status != "finalized" && status != "failed" && status != "released" {
			rows.Close()
			return fmt.Errorf("legacy reservation %s needs reconciliation: %w", id, ErrInvalid)
		}
		createdAt, err := parseLegacyTime(created)
		if err != nil {
			rows.Close()
			return err
		}
		updatedAt, err := parseLegacyTime(updated)
		if err != nil {
			rows.Close()
			return err
		}
		needsReconciliation := 0
		if status == "reserved" {
			needsReconciliation = 1
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO usage_reservations
 (id,api_key_id,account_id,model,status,input_tokens,output_tokens,cached_input_tokens,
	 cost_microdollars,created_at,updated_at,needs_reconciliation)
 VALUES(?,?,'',?,?,?,?,?,?,?,?,?)`,
			id, keyID, model, status, nullableInt(input), nullableInt(output), nullableInt(cached),
			nullableInt(cost), millis(createdAt), millis(updatedAt), needsReconciliation)
		if err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, `SELECT reservation_id,limit_id,limit_type,
 reserved_delta,actual_delta,expected_reset_at FROM api_key_usage_reservation_items ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var reservationID, limitType string
		var limitID, reserved int64
		var actual sql.NullInt64
		var reset any
		if err := rows.Scan(&reservationID, &limitID, &limitType, &reserved, &actual, &reset); err != nil {
			return err
		}
		resetAt, err := parseLegacyTime(reset)
		if err != nil {
			return err
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO usage_reservation_items
 (reservation_id,limit_id,limit_type,reserved_delta,actual_delta,expected_reset_at)
 VALUES(?,?,?,?,?,?)`, reservationID, limitID, limitType, reserved, nullableInt(actual), millis(resetAt))
		if err != nil {
			return err
		}
	}
	return rows.Err()
}
