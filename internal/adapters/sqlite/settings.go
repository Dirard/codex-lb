package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"codex-lb/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// LoadCodexClientVersion reads the current setting without loading unrelated
// routing policy on each upstream operation.
func (s *Store) LoadCodexClientVersion(ctx context.Context) (string, error) {
	var version string
	err := s.readDB.QueryRowContext(ctx, `SELECT codex_client_version FROM runtime_settings WHERE id=1`).Scan(&version)
	if err != nil {
		return "", err
	}
	if !domain.ValidCodexClientVersion(version) {
		return "", ErrInvalid
	}
	return version, nil
}

func (s *Store) LoadSettings(ctx context.Context) (domain.RuntimeSettings, error) {
	var v domain.RuntimeSettings
	var retention, historyRetention sql.NullInt64
	var createOverride, streamOverride, reserveOverride, fairShareOverride sql.NullInt64
	var additionalPolicies string
	err := s.readDB.QueryRowContext(ctx, `SELECT api_key_auth_enabled,hide_upstream_quota_from_keys,
 sticky_threads_enabled,totp_required_on_login,prohibit_fast_mode,
 prefer_earlier_reset_accounts,prefer_earlier_reset_window,
 show_reset_credit_badges,show_reset_credit_expiry_badge,import_without_overwrite,
	 routing_strategy,relative_availability_power,relative_availability_top_k,
	 single_account_id,upstream_stream_transport,
	http_transport_policy,dashboard_session_ttl,warmup_model,limit_warmup_enabled,limit_warmup_model,
	limit_warmup_windows,limit_warmup_prompt,limit_warmup_cooldown_seconds,
	limit_warmup_idle_threshold_percent,limit_warmup_exhausted_threshold_percent,
	limit_warmup_min_available_percent,limit_warmup_staggered_idle_enabled,
	additional_quota_routing_policies,version,
	 request_log_retention_days,usage_history_retention_days,
	 proxy_account_response_create_limit,proxy_account_stream_limit,
	 proxy_account_stream_recovery_reserve,proxy_api_key_fair_share_congestion_threshold_pct,
	 weekly_pace_working_days,weekly_pace_smoothing_minutes,openai_cache_affinity_max_age_seconds,
	 sticky_reallocation_primary_budget_threshold_pct,sticky_reallocation_secondary_budget_threshold_pct,codex_client_version
 FROM runtime_settings WHERE id=1`).Scan(&v.APIKeyAuthEnabled, &v.HideUpstreamQuotaFromKeys,
		&v.StickyThreadsEnabled, &v.TOTPRequiredOnLogin, &v.ProhibitFastMode,
		&v.PreferEarlierResetAccounts, &v.PreferEarlierResetWindow,
		&v.ShowResetCreditBadges, &v.ShowResetCreditExpiryBadge, &v.ImportWithoutOverwrite,
		&v.RoutingStrategy, &v.RelativeAvailabilityPower, &v.RelativeAvailabilityTopK,
		&v.SingleAccountID, &v.UpstreamStreamTransport,
		&v.HTTPTransportPolicy, &v.DashboardSessionTTL, &v.WarmupModel, &v.LimitWarmupEnabled,
		&v.LimitWarmupModel, &v.LimitWarmupWindows, &v.LimitWarmupPrompt,
		&v.LimitWarmupCooldownSeconds, &v.LimitWarmupIdlePercent,
		&v.LimitWarmupExhaustedPercent, &v.LimitWarmupMinAvailablePercent,
		&v.LimitWarmupStaggeredIdleEnabled, &additionalPolicies, &v.Version,
		&retention, &historyRetention, &createOverride, &streamOverride, &reserveOverride, &fairShareOverride,
		&v.WeeklyPaceWorkingDays, &v.WeeklyPaceSmoothingMinutes, &v.OpenAICacheAffinityMaxAgeSeconds,
		&v.StickyReallocationPrimaryBudgetThresholdPct, &v.StickyReallocationSecondaryBudgetThresholdPct, &v.CodexClientVersion)
	if err != nil {
		return v, err
	}
	if !domain.ValidCodexClientVersion(v.CodexClientVersion) {
		return v, ErrInvalid
	}
	if err := normalizeWeeklyPaceSettings(&v); err != nil {
		return v, err
	}
	if err := validateAffinitySettings(v); err != nil {
		return v, err
	}
	for _, field := range []struct {
		value sql.NullInt64
		dst   **int
	}{
		{createOverride, &v.ProxyAccountResponseCreateLimitOverride},
		{streamOverride, &v.ProxyAccountStreamLimitOverride},
		{reserveOverride, &v.ProxyAccountStreamRecoveryReserveOverride},
		{fairShareOverride, &v.ProxyApiKeyFairShareCongestionThresholdPctOverride},
	} {
		if field.value.Valid {
			if field.value.Int64 < 0 || int64(int(field.value.Int64)) != field.value.Int64 {
				return v, ErrInvalid
			}
			value := int(field.value.Int64)
			*field.dst = &value
		}
	}
	env := s.accountAdmissionEnv
	v.ProxyAccountResponseCreateLimitEnvironmentValue = env.create
	v.ProxyAccountStreamLimitEnvironmentValue = env.stream
	v.ProxyAccountStreamRecoveryReserveEnvironmentValue = env.reserve
	v.ProxyApiKeyFairShareCongestionThresholdPctEnvironmentValue = env.fairShare
	v.ProxyAccountResponseCreateLimit = effectiveAccountAdmission(v.ProxyAccountResponseCreateLimitOverride, env.create)
	v.ProxyAccountStreamLimit = effectiveAccountAdmission(v.ProxyAccountStreamLimitOverride, env.stream)
	v.ProxyAccountStreamRecoveryReserve = effectiveAccountAdmission(v.ProxyAccountStreamRecoveryReserveOverride, env.reserve)
	v.ProxyApiKeyFairShareCongestionThresholdPct = effectiveAccountAdmission(v.ProxyApiKeyFairShareCongestionThresholdPctOverride, env.fairShare)
	if v.ProxyAccountStreamLimit > 0 && v.ProxyAccountStreamRecoveryReserve > v.ProxyAccountStreamLimit {
		return v, ErrInvalid
	}
	if retention.Valid {
		days := int(retention.Int64)
		v.RequestLogRetentionDays = &days
	}
	if historyRetention.Valid {
		days := int(historyRetention.Int64)
		v.UsageHistoryRetentionDays = &days
	}
	if err == nil {
		if json.Unmarshal([]byte(additionalPolicies), &v.AdditionalQuotaRoutingPolicies) != nil {
			return v, ErrInvalid
		}
		if v.AdditionalQuotaRoutingPolicies == nil {
			v.AdditionalQuotaRoutingPolicies = map[string]string{}
		}
	}
	return v, err
}

func (s *Store) SaveSettings(ctx context.Context, v domain.RuntimeSettings) error {
	v.CodexClientVersion = strings.TrimSpace(v.CodexClientVersion)
	if !domain.ValidCodexClientVersion(v.CodexClientVersion) {
		return ErrInvalid
	}
	if err := validateAffinitySettings(v); err != nil {
		return err
	}
	if err := normalizeWeeklyPaceSettings(&v); err != nil {
		return err
	}
	for _, override := range []*int{v.ProxyAccountResponseCreateLimitOverride, v.ProxyAccountStreamLimitOverride,
		v.ProxyAccountStreamRecoveryReserveOverride, v.ProxyApiKeyFairShareCongestionThresholdPctOverride} {
		if override != nil && *override < 0 {
			return ErrInvalid
		}
	}
	if v.ProxyApiKeyFairShareCongestionThresholdPctOverride != nil && *v.ProxyApiKeyFairShareCongestionThresholdPctOverride > 100 {
		return ErrInvalid
	}
	streamLimit := effectiveAccountAdmission(v.ProxyAccountStreamLimitOverride, s.accountAdmissionEnv.stream)
	reserve := effectiveAccountAdmission(v.ProxyAccountStreamRecoveryReserveOverride, s.accountAdmissionEnv.reserve)
	if streamLimit > 0 && reserve > streamLimit {
		return ErrInvalid
	}
	v.WarmupModel = strings.TrimSpace(v.WarmupModel)
	if v.WarmupModel == "" || len(v.WarmupModel) > 128 {
		return fmt.Errorf("invalid warmup model: %w", ErrInvalid)
	}
	additionalPolicies := make(map[string]string, len(v.AdditionalQuotaRoutingPolicies))
	for key, policy := range v.AdditionalQuotaRoutingPolicies {
		definition, known := domain.KnownAdditionalQuota(key)
		policy = strings.ToLower(strings.TrimSpace(policy))
		if !known || !slices.Contains([]string{"inherit", "normal", "burn_first", "preserve"}, policy) {
			return ErrInvalid
		}
		if previous, present := additionalPolicies[definition.QuotaKey]; present && previous != policy {
			return ErrInvalid
		}
		additionalPolicies[definition.QuotaKey] = policy
	}
	encodedPolicies, err := json.Marshal(additionalPolicies)
	if err != nil {
		return err
	}
	if v.Version < 1 || v.DashboardSessionTTL < 60 || v.RoutingStrategy == "" ||
		v.UpstreamStreamTransport == "" || v.HTTPTransportPolicy == "" || v.PreferEarlierResetWindow == "" {
		return fmt.Errorf("runtime settings: %w", ErrInvalid)
	}
	if v.DashboardSessionTTL > 366*24*3600 || !slices.Contains([]string{"usage_weighted", "round_robin", "capacity_weighted", "sequential_drain", "reset_drain", "single_account", "relative_availability", "fill_first"}, v.RoutingStrategy) ||
		!slices.Contains([]string{"default", "auto", "http", "websocket"}, v.UpstreamStreamTransport) ||
		!slices.Contains([]string{"smart", "always_http", "always_websocket", "pinned"}, v.HTTPTransportPolicy) ||
		!slices.Contains([]string{"primary", "secondary"}, v.PreferEarlierResetWindow) {
		return fmt.Errorf("unsupported runtime settings: %w", ErrInvalid)
	}
	if !(v.RelativeAvailabilityPower > 0) || math.IsNaN(v.RelativeAvailabilityPower) || math.IsInf(v.RelativeAvailabilityPower, 0) ||
		v.RelativeAvailabilityTopK < 1 || v.RelativeAvailabilityTopK > 20 {
		return fmt.Errorf("invalid relative availability settings: %w", ErrInvalid)
	}
	if !slices.Contains([]string{"primary", "secondary", "both"}, v.LimitWarmupWindows) ||
		v.LimitWarmupModel == "" || len(v.LimitWarmupModel) > 128 ||
		v.LimitWarmupPrompt == "" || len(v.LimitWarmupPrompt) > 512 ||
		v.LimitWarmupCooldownSeconds < 60 ||
		!validWarmupPercent(v.LimitWarmupExhaustedPercent) ||
		!validWarmupPercent(v.LimitWarmupIdlePercent) ||
		!validWarmupPercent(v.LimitWarmupMinAvailablePercent) {
		return fmt.Errorf("invalid limit warm-up settings: %w", ErrInvalid)
	}
	if v.RequestLogRetentionDays != nil && (*v.RequestLogRetentionDays < 0 || *v.RequestLogRetentionDays > 3650 || *v.RequestLogRetentionDays > 0 && *v.RequestLogRetentionDays < 30) {
		return ErrInvalid
	}
	if v.UsageHistoryRetentionDays != nil && (*v.UsageHistoryRetentionDays < 0 || *v.UsageHistoryRetentionDays > 3650 ||
		*v.UsageHistoryRetentionDays > 0 && *v.UsageHistoryRetentionDays < 45) {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		if v.TOTPRequiredOnLogin {
			var present int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM admin_secret
 WHERE id=1 AND password_hash<>'' AND totp_secret_encrypted IS NOT NULL AND length(totp_secret_encrypted)>0`).Scan(&present); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("TOTP login requires configured admin secret: %w", ErrInvalid)
				}
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE runtime_settings SET
 api_key_auth_enabled=?,hide_upstream_quota_from_keys=?,sticky_threads_enabled=?,
 totp_required_on_login=?,prohibit_fast_mode=?,prefer_earlier_reset_accounts=?,
 prefer_earlier_reset_window=?,show_reset_credit_badges=?,show_reset_credit_expiry_badge=?,
 import_without_overwrite=?,
	 routing_strategy=?,relative_availability_power=?,relative_availability_top_k=?,
	 single_account_id=?,upstream_stream_transport=?,http_transport_policy=?,
	dashboard_session_ttl=?,warmup_model=?,limit_warmup_enabled=?,limit_warmup_model=?,
	limit_warmup_windows=?,limit_warmup_prompt=?,limit_warmup_cooldown_seconds=?,
	limit_warmup_idle_threshold_percent=?,limit_warmup_exhausted_threshold_percent=?,
	limit_warmup_min_available_percent=?,limit_warmup_staggered_idle_enabled=?,
	additional_quota_routing_policies=?,
	 request_log_retention_days=?,usage_history_retention_days=?,
	 proxy_account_response_create_limit=?,proxy_account_stream_limit=?,
	 proxy_account_stream_recovery_reserve=?,proxy_api_key_fair_share_congestion_threshold_pct=?,
	 weekly_pace_working_days=?,weekly_pace_smoothing_minutes=?,openai_cache_affinity_max_age_seconds=?,
	 sticky_reallocation_primary_budget_threshold_pct=?,sticky_reallocation_secondary_budget_threshold_pct=?,codex_client_version=?,version=version+1
 WHERE id=1 AND version=?`, boolInt(v.APIKeyAuthEnabled), boolInt(v.HideUpstreamQuotaFromKeys),
			boolInt(v.StickyThreadsEnabled), boolInt(v.TOTPRequiredOnLogin), boolInt(v.ProhibitFastMode),
			boolInt(v.PreferEarlierResetAccounts), v.PreferEarlierResetWindow,
			boolInt(v.ShowResetCreditBadges), boolInt(v.ShowResetCreditExpiryBadge), boolInt(v.ImportWithoutOverwrite),
			v.RoutingStrategy, v.RelativeAvailabilityPower, v.RelativeAvailabilityTopK,
			v.SingleAccountID, v.UpstreamStreamTransport, v.HTTPTransportPolicy, v.DashboardSessionTTL, v.WarmupModel,
			boolInt(v.LimitWarmupEnabled), v.LimitWarmupModel, v.LimitWarmupWindows,
			v.LimitWarmupPrompt, v.LimitWarmupCooldownSeconds, v.LimitWarmupIdlePercent,
			v.LimitWarmupExhaustedPercent, v.LimitWarmupMinAvailablePercent,
			boolInt(v.LimitWarmupStaggeredIdleEnabled), string(encodedPolicies),
			v.RequestLogRetentionDays,
			v.UsageHistoryRetentionDays,
			nullableIntValue(v.ProxyAccountResponseCreateLimitOverride), nullableIntValue(v.ProxyAccountStreamLimitOverride),
			nullableIntValue(v.ProxyAccountStreamRecoveryReserveOverride), nullableIntValue(v.ProxyApiKeyFairShareCongestionThresholdPctOverride),
			v.WeeklyPaceWorkingDays, v.WeeklyPaceSmoothingMinutes,
			v.OpenAICacheAffinityMaxAgeSeconds, v.StickyReallocationPrimaryBudgetThresholdPct, v.StickyReallocationSecondaryBudgetThresholdPct,
			v.CodexClientVersion, v.Version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		return nil
	})
}

func validWarmupPercent(v float64) bool {
	return v > 0 && v <= 100 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validateAdminSecret(v domain.AdminSecret) error {
	if _, err := bcrypt.Cost([]byte(v.PasswordHash)); err != nil || !fernetCiphertext(v.TOTPSecretEncrypted) {
		return fmt.Errorf("admin secret: %w", ErrInvalid)
	}
	return nil
}

func (s *Store) LoadAdminSecret(ctx context.Context) (domain.AdminSecret, error) {
	var v domain.AdminSecret
	var step sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `SELECT password_hash,totp_secret_encrypted,totp_last_verified_step
 FROM admin_secret WHERE id=1`).Scan(&v.PasswordHash, &v.TOTPSecretEncrypted, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if step.Valid {
		v.TOTPLastVerifiedStep = &step.Int64
	}
	return v, err
}

func (s *Store) SaveAdminSecret(ctx context.Context, v domain.AdminSecret) error {
	if v.PasswordHash == "" && len(v.TOTPSecretEncrypted) == 0 && v.TOTPLastVerifiedStep == nil {
		return transact(ctx, s.db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM admin_secret WHERE id=1"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE runtime_settings SET totp_required_on_login=0,
 version=version+1 WHERE id=1 AND totp_required_on_login=1`)
			return err
		})
	}
	if err := validateAdminSecret(v); err != nil {
		return err
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_secret
 (id,password_hash,totp_secret_encrypted,totp_last_verified_step) VALUES(1,?,?,?)
 ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash,
 totp_secret_encrypted=excluded.totp_secret_encrypted,
 totp_last_verified_step=excluded.totp_last_verified_step`, v.PasswordHash,
			v.TOTPSecretEncrypted, nullableStep(v.TOTPLastVerifiedStep)); err != nil {
			return err
		}
		if len(v.TOTPSecretEncrypted) == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE runtime_settings SET totp_required_on_login=0,
 version=version+1 WHERE id=1 AND totp_required_on_login=1`)
			return err
		}
		return nil
	})
}

func nullableStep(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func (s *Store) InitializeAdminSecret(ctx context.Context, v domain.AdminSecret) (bool, error) {
	if err := validateAdminSecret(v); err != nil {
		return false, err
	}
	initialized := false
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO admin_secret
 (id,password_hash,totp_secret_encrypted,totp_last_verified_step) VALUES(1,?,?,?)
 ON CONFLICT(id) DO NOTHING`, v.PasswordHash, v.TOTPSecretEncrypted, nullableStep(v.TOTPLastVerifiedStep))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		initialized = true
		_, err = tx.ExecContext(ctx, `UPDATE runtime_settings SET api_key_auth_enabled=1,version=version+1 WHERE id=1`)
		return err
	})
	return initialized, err
}

func (s *Store) AdvanceTOTPStep(ctx context.Context, step int64) (bool, error) {
	if step < 0 {
		return false, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE admin_secret SET totp_last_verified_step=?
 WHERE id=1 AND totp_secret_encrypted IS NOT NULL AND
 (totp_last_verified_step IS NULL OR totp_last_verified_step<?)`, step, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}
