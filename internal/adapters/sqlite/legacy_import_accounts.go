package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func importLegacyAccounts(ctx context.Context, src, dst *sql.Tx, vault LegacyVault) error {
	blocked := make(map[string]bool)
	var globalProxy bool
	if err := src.QueryRowContext(ctx, `SELECT upstream_proxy_routing_enabled
 FROM dashboard_settings WHERE id=1`).Scan(&globalProxy); err != nil {
		return err
	}
	var bindingsTable int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table'
 AND name='account_proxy_bindings'`).Scan(&bindingsTable); err != nil {
		return err
	}
	if bindingsTable != 0 {
		rows, err := src.QueryContext(ctx, `SELECT account_id FROM account_proxy_bindings WHERE is_active=1`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			blocked[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	var historyColumn int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('accounts') WHERE name='delete_history_requested'`).Scan(&historyColumn); err != nil {
		return err
	}
	historyChoice := "0"
	if historyColumn != 0 {
		historyChoice = "coalesce(delete_history_requested,0)"
	}
	rows, err := src.QueryContext(ctx, `SELECT id,chatgpt_account_id,chatgpt_user_id,
 codex_installation_id,email,alias,workspace_id,workspace_label,seat_type,plan_type,
 routing_policy,access_token_encrypted,refresh_token_encrypted,id_token_encrypted,
 last_refresh,created_at,status,deactivation_reason,security_work_authorized,
 limit_warmup_enabled,delete_requested_at,`+historyChoice+` FROM accounts ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, installation, email, plan, policy, status string
		var chatID, userID, alias, workspaceID, workspaceLabel, seatType, reason sql.NullString
		var access, refresh, identity []byte
		var last, created, deleted any
		var security, warmup, deleteHistory bool
		if err := rows.Scan(&id, &chatID, &userID, &installation, &email, &alias,
			&workspaceID, &workspaceLabel, &seatType, &plan, &policy,
			&access, &refresh, &identity, &last, &created, &status, &reason,
			&security, &warmup, &deleted, &deleteHistory); err != nil {
			return err
		}
		isDeleted := deleted != nil || status == "deactivated" && textVal(reason) == "deleted"
		if deleted != nil {
			if _, err := parseLegacyTime(deleted); err != nil {
				return fmt.Errorf("legacy account %s delete_requested_at: %w", id, err)
			}
		}
		if !isDeleted {
			for _, cipher := range [][]byte{access, refresh, identity} {
				if len(cipher) == 0 || !fernetCiphertext(cipher) {
					return fmt.Errorf("legacy account %s credential format: %w", id, ErrInvalid)
				}
				if _, err := vault.Decrypt(cipher); err != nil {
					return fmt.Errorf("legacy account %s credential cannot decrypt: %w", id, ErrInvalid)
				}
			}
		}
		createdAt, err := parseLegacyTime(created)
		if err != nil {
			return fmt.Errorf("legacy account %s created_at: %w", id, err)
		}
		lastAt, err := optionalLegacyMillis(last)
		if err != nil {
			return fmt.Errorf("legacy account %s last_refresh: %w", id, err)
		}
		if isDeleted {
			status = "deactivated"
			reason = sql.NullString{String: "deleted", Valid: true}
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO accounts
 (id,kind,provider,base_url,chatgpt_account_id,chatgpt_user_id,codex_installation_id,
 email,alias,workspace_id,workspace_label,seat_type,plan_type,routing_policy,status,
 deactivation_reason,requires_egress_decision,security_work_authorized,limit_warmup_enabled,
 created_at,last_refresh) VALUES(?,'chatgpt','openai','',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, textVal(chatID), textVal(userID), installation, email, textVal(alias),
			textVal(workspaceID), textVal(workspaceLabel), textVal(seatType), plan, policy,
			status, textVal(reason), boolInt(blocked[id] || globalProxy), boolInt(security), boolInt(warmup),
			millis(createdAt), lastAt)
		if err != nil {
			return fmt.Errorf("import account %s: %w", id, err)
		}
		if isDeleted {
			if _, err := dst.ExecContext(ctx, `INSERT INTO account_deletions(account_id,generation,delete_history) VALUES(?,0,?)`, id, boolInt(deleteHistory)); err != nil {
				return err
			}
			continue
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_credentials
 (account_id,access_token_encrypted,refresh_token_encrypted,id_token_encrypted)
 VALUES(?,?,?,?)`, id, access, refresh, identity); err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacyModelSources(ctx context.Context, src, dst *sql.Tx, vault LegacyVault) error {
	var globalProxy bool
	if err := src.QueryRowContext(ctx, `SELECT upstream_proxy_routing_enabled
 FROM dashboard_settings WHERE id=1`).Scan(&globalProxy); err != nil {
		return err
	}
	rows, err := src.QueryContext(ctx, `SELECT id,name,kind,base_url,api_key_encrypted,is_enabled,
 health_status,supports_chat_completions,supports_responses,supports_audio_transcriptions,
 supports_embeddings,timeout_seconds,max_concurrency,created_at,updated_at
 FROM model_sources ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, kind, baseURL, health string
		var cipher []byte
		var enabled, chat, responses, audio, embeddings bool
		var timeout, concurrency sql.NullInt64
		var created, updated any
		if err := rows.Scan(&id, &name, &kind, &baseURL, &cipher, &enabled, &health, &chat, &responses,
			&audio, &embeddings, &timeout, &concurrency, &created, &updated); err != nil {
			rows.Close()
			return err
		}
		if len(cipher) != 0 {
			if !fernetCiphertext(cipher) {
				rows.Close()
				return fmt.Errorf("legacy source %s credential format: %w", id, ErrInvalid)
			}
			if _, err := vault.Decrypt(cipher); err != nil {
				rows.Close()
				return fmt.Errorf("legacy source %s credential cannot decrypt: %w", id, ErrInvalid)
			}
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
		status := "paused"
		if enabled {
			status = "active"
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO accounts
	 (id,kind,provider,base_url,email,alias,plan_type,routing_policy,status,requires_egress_decision,created_at)
	 VALUES(?,'external',?,?,?,?, 'external','normal',?,?,?)`, id, kind, baseURL, id, name, status,
			boolInt(globalProxy), millis(createdAt)); err != nil {
			rows.Close()
			return fmt.Errorf("import external source %s: %w", id, err)
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_credentials(account_id,external_key_encrypted)
 VALUES(?,?)`, id, nullBytes(cipher)); err != nil {
			rows.Close()
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO legacy_model_sources
 (id,name,kind,base_url,is_enabled,health_status,supports_chat_completions,
 supports_responses,supports_audio_transcriptions,supports_embeddings,timeout_seconds,
 max_concurrency,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, name, kind, baseURL, boolInt(enabled), health, boolInt(chat), boolInt(responses),
			boolInt(audio), boolInt(embeddings), nullableInt(timeout), nullableInt(concurrency),
			millis(createdAt), millis(updatedAt)); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, `SELECT id,source_id,model,display_name,context_window,
 max_output_tokens,supports_streaming,supports_tools,supports_vision,input_per_1m,
 cached_input_per_1m,output_per_1m,audio_per_minute,raw_metadata_json,is_enabled,
 created_at,updated_at FROM model_source_models ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sourceID, model string
		var display, rawMeta sql.NullString
		var contextWindow, maxOutput sql.NullInt64
		var streaming, tools, vision, enabled bool
		var input, cached, output, audio sql.NullFloat64
		var created, updated any
		if err := rows.Scan(&id, &sourceID, &model, &display, &contextWindow, &maxOutput,
			&streaming, &tools, &vision, &input, &cached, &output, &audio, &rawMeta, &enabled,
			&created, &updated); err != nil {
			return err
		}
		createdAt, err := parseLegacyTime(created)
		if err != nil {
			return err
		}
		updatedAt, err := parseLegacyTime(updated)
		if err != nil {
			return err
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO legacy_model_source_models
 (id,source_id,model,display_name,context_window,max_output_tokens,supports_streaming,
 supports_tools,supports_vision,input_per_1m,cached_input_per_1m,output_per_1m,
 audio_per_minute,raw_metadata_json,is_enabled,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, sourceID, model, nullText(display),
			nullableInt(contextWindow), nullableInt(maxOutput), boolInt(streaming), boolInt(tools),
			boolInt(vision), nullableFloat(input), nullableFloat(cached), nullableFloat(output),
			nullableFloat(audio), nullText(rawMeta), boolInt(enabled), millis(createdAt), millis(updatedAt))
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacySettings(ctx context.Context, src, dst *sql.Tx, vault LegacyVault) error {
	var sticky, prohibit, prefer, importNoOverwrite, totpRequired, keyAuth, hideQuota, warmup bool
	var routing, resetWindow, streamTransport, httpPolicy, warmupModel string
	var singleAccount, password sql.NullString
	var totp []byte
	var lastStep sql.NullInt64
	var sessionTTL int
	err := src.QueryRowContext(ctx, `SELECT sticky_threads_enabled,prohibit_fast_mode,
 prefer_earlier_reset_accounts,prefer_earlier_reset_window,import_without_overwrite,
 totp_required_on_login,api_key_auth_enabled,hide_upstream_quota_from_api_keys,
 limit_warmup_enabled,routing_strategy,single_account_id,upstream_stream_transport,
 http_downstream_transport_policy,limit_warmup_model,dashboard_session_ttl_seconds,
 password_hash,totp_secret_encrypted,totp_last_verified_step
 FROM dashboard_settings WHERE id=1`).Scan(&sticky, &prohibit, &prefer, &resetWindow,
		&importNoOverwrite, &totpRequired, &keyAuth, &hideQuota, &warmup, &routing,
		&singleAccount, &streamTransport, &httpPolicy, &warmupModel, &sessionTTL,
		&password, &totp, &lastStep)
	if err != nil {
		return err
	}
	// Older snapshots may predate the explicit retention override. Absence is
	// unlimited retention, never permission to infer a destructive new default.
	var retentionColumn int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name='request_log_retention_days'`).Scan(&retentionColumn); err != nil {
		return err
	}
	if retentionColumn != 0 {
		var retention sql.NullInt64
		if err := src.QueryRowContext(ctx, "SELECT request_log_retention_days FROM dashboard_settings WHERE id=1").Scan(&retention); err != nil {
			return err
		}
		if retention.Valid && (retention.Int64 < 0 || retention.Int64 > 3650 || retention.Int64 > 0 && retention.Int64 < 30) {
			return ErrInvalid
		}
		if _, err := dst.ExecContext(ctx, "UPDATE runtime_settings SET request_log_retention_days=? WHERE id=1", nullableInt(retention)); err != nil {
			return err
		}
	}
	var historyRetentionColumn int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name='usage_history_retention_days'`).Scan(&historyRetentionColumn); err != nil {
		return err
	}
	if historyRetentionColumn != 0 {
		var retention sql.NullInt64
		if err := src.QueryRowContext(ctx, "SELECT usage_history_retention_days FROM dashboard_settings WHERE id=1").Scan(&retention); err != nil {
			return err
		}
		if retention.Valid && (retention.Int64 < 0 || retention.Int64 > 3650 || retention.Int64 > 0 && retention.Int64 < 45) {
			return ErrInvalid
		}
		if _, err := dst.ExecContext(ctx, "UPDATE runtime_settings SET usage_history_retention_days=? WHERE id=1", nullableInt(retention)); err != nil {
			return err
		}
	}
	if password.Valid && password.String != "" {
		if _, err := bcrypt.Cost([]byte(password.String)); err != nil {
			return fmt.Errorf("legacy password hash: %w", ErrInvalid)
		}
	}
	if len(totp) != 0 {
		if !fernetCiphertext(totp) {
			return fmt.Errorf("legacy TOTP format: %w", ErrInvalid)
		}
		if _, err := vault.Decrypt(totp); err != nil {
			return fmt.Errorf("legacy TOTP cannot decrypt: %w", ErrInvalid)
		}
	}
	if totpRequired && (!password.Valid || password.String == "" || len(totp) == 0) {
		return fmt.Errorf("legacy TOTP policy without credential: %w", ErrInvalid)
	}
	_, err = dst.ExecContext(ctx, `UPDATE runtime_settings SET sticky_threads_enabled=?,
 prohibit_fast_mode=?,prefer_earlier_reset_accounts=?,prefer_earlier_reset_window=?,
 import_without_overwrite=?,totp_required_on_login=?,api_key_auth_enabled=?,
 hide_upstream_quota_from_keys=?,limit_warmup_enabled=?,routing_strategy=?,
 single_account_id=?,upstream_stream_transport=?,http_transport_policy=?,
 limit_warmup_model=?,dashboard_session_ttl=? WHERE id=1`, boolInt(sticky), boolInt(prohibit),
		boolInt(prefer), resetWindow, boolInt(importNoOverwrite), boolInt(totpRequired),
		boolInt(keyAuth), boolInt(hideQuota), boolInt(warmup), routing, textVal(singleAccount),
		streamTransport, httpPolicy, warmupModel, sessionTTL)
	if err != nil {
		return err
	}
	if err := importLegacyWarmupSettings(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyRelativeAvailabilitySettings(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyResetCreditSettings(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyAccountAdmissionSettings(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyWeeklyPaceSettings(ctx, src, dst); err != nil {
		return err
	}
	if err := importLegacyAffinitySettings(ctx, src, dst); err != nil {
		return err
	}
	if password.Valid && password.String != "" {
		_, err = dst.ExecContext(ctx, `INSERT INTO admin_secret
 (id,password_hash,totp_secret_encrypted,totp_last_verified_step) VALUES(1,?,?,?)`,
			password.String, nullBytes(totp), nullableInt(lastStep))
	}
	return err
}

func importLegacyResetCreditSettings(ctx context.Context, src, dst *sql.Tx) error {
	for _, name := range []string{"show_reset_credit_badges", "show_reset_credit_expiry_badge"} {
		var present int
		if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name=?`, name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			continue // Older snapshots inherit the Go defaults.
		}
		var value sql.NullInt64
		if err := src.QueryRowContext(ctx, "SELECT "+name+" FROM dashboard_settings WHERE id=1").Scan(&value); err != nil {
			return err
		}
		if !value.Valid {
			continue
		}
		if value.Int64 != 0 && value.Int64 != 1 {
			return fmt.Errorf("legacy %s: %w", name, ErrInvalid)
		}
		if _, err := dst.ExecContext(ctx, "UPDATE runtime_settings SET "+name+"=? WHERE id=1", value.Int64); err != nil {
			return err
		}
	}
	return nil
}

func optionalLegacyMillis(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	t, err := parseLegacyTime(v)
	if err != nil {
		return nil, err
	}
	return millis(t), nil
}
func textVal(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
func nullText(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}
func nullableInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}
func nullableFloat(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}
func nullBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
