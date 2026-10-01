package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func newID(prefix string) string {
	var value [18]byte
	rand.Read(value[:])
	return prefix + base64.RawURLEncoding.EncodeToString(value[:])
}

func (s *Server) registerAdmin(mux *http.ServeMux) {
	for _, path := range []string{"/api/account-groups", "/api/account-groups/{$}"} {
		mux.HandleFunc("GET "+path, s.listGroups)
		mux.HandleFunc("POST "+path, s.saveGroup)
	}
	mux.HandleFunc("PUT /api/account-groups/{id}", s.saveGroup)
	mux.HandleFunc("DELETE /api/account-groups/{id}", s.deleteGroup)
	for _, path := range []string{"/api/api-keys", "/api/api-keys/{$}"} {
		mux.HandleFunc("GET "+path, s.listKeys)
		mux.HandleFunc("POST "+path, s.saveKey)
	}
	mux.HandleFunc("PATCH /api/api-keys/{id}", s.saveKey)
	mux.HandleFunc("DELETE /api/api-keys/{id}", s.deleteKey)
	mux.HandleFunc("POST /api/api-keys/{id}/regenerate", s.regenerateKey)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.saveSettings)
	for _, path := range []string{"/api/settings/runtime/connect-address", "/api/settings/runtime/connect-address/{$}"} {
		mux.HandleFunc("GET "+path, s.runtimeConnectAddress)
	}
	mux.HandleFunc("GET /api/accounts", s.listAccounts)
	mux.HandleFunc("PUT /api/accounts/{id}/groups", s.setAccountGroups)
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if groups == nil {
		groups = []domain.AccountGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name     string             `json:"name"`
		Accounts []string           `json:"accountIds"`
		Limits   []domain.LimitRule `json:"limits"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	id := r.PathValue("id")
	if id != "" {
		if _, err := s.store.GetGroup(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
	} else {
		id = newID("grp_")
	}
	group := domain.AccountGroup{ID: id, Name: payload.Name, AccountIDs: payload.Accounts, Limits: payload.Limits}
	if err := s.store.SaveGroup(r.Context(), group, time.Now().UTC()); err != nil {
		s.fail(w, err)
		return
	}
	group, err := s.store.GetGroup(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteGroup(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	secret, err := s.store.LoadAdminSecret(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	days := 0
	if settings.RequestLogRetentionDays != nil {
		days = *settings.RequestLogRetentionDays
	}
	historyDays := 0
	if settings.UsageHistoryRetentionDays != nil {
		historyDays = *settings.UsageHistoryRetentionDays
	}
	writeJSON(w, http.StatusOK, struct {
		domain.RuntimeSettings
		TOTPConfigured                       bool    `json:"totpConfigured"`
		RequestLogRetentionDays              int     `json:"requestLogRetentionDays"`
		UsageHistoryRetentionDays            int     `json:"usageHistoryRetentionDays"`
		StickyReallocationBudgetThresholdPct float64 `json:"stickyReallocationBudgetThresholdPct"`
	}{settings, len(secret.TOTPSecretEncrypted) != 0, days, historyDays, settings.StickyReallocationPrimaryBudgetThresholdPct})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	payload := struct {
		domain.RuntimeSettings
		ExpectedVersion     *int64          `json:"expectedVersion"`
		ClientVersion       json.RawMessage `json:"codexClientVersion"`
		CreateLimit         json.RawMessage `json:"proxyAccountResponseCreateLimit"`
		StreamLimit         json.RawMessage `json:"proxyAccountStreamLimit"`
		RecoveryReserve     json.RawMessage `json:"proxyAccountStreamRecoveryReserve"`
		FairShareThreshold  json.RawMessage `json:"proxyApiKeyFairShareCongestionThresholdPct"`
		WeeklySmoothing     json.RawMessage `json:"weeklyPaceSmoothingMinutes"`
		WeeklyWorkingDays   json.RawMessage `json:"weeklyPaceWorkingDays"`
		CacheAffinityTTL    *int            `json:"openaiCacheAffinityMaxAgeSeconds"`
		StickyPrimary       *float64        `json:"stickyReallocationPrimaryBudgetThresholdPct"`
		StickySecondary     *float64        `json:"stickyReallocationSecondaryBudgetThresholdPct"`
		StickyLegacyPrimary *float64        `json:"stickyReallocationBudgetThresholdPct"`
	}{RuntimeSettings: settings}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ExpectedVersion != nil && *payload.ExpectedVersion != settings.Version {
		writeError(w, http.StatusConflict, "settings_conflict", "Settings changed; reload before saving")
		return
	}
	if len(payload.ClientVersion) > 0 {
		var value string
		if json.Unmarshal(payload.ClientVersion, &value) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Invalid Codex client version")
			return
		}
		payload.CodexClientVersion = value
	}
	if payload.CacheAffinityTTL != nil {
		payload.OpenAICacheAffinityMaxAgeSeconds = *payload.CacheAffinityTTL
	}
	primary := payload.StickyPrimary
	if primary == nil {
		primary = payload.StickyLegacyPrimary
	}
	if primary != nil {
		payload.StickyReallocationPrimaryBudgetThresholdPct = *primary
	}
	if payload.StickySecondary != nil {
		payload.StickyReallocationSecondaryBudgetThresholdPct = *payload.StickySecondary
	}
	if len(payload.WeeklySmoothing) > 0 {
		var value int
		if json.Unmarshal(payload.WeeklySmoothing, &value) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Invalid weekly pace smoothing interval")
			return
		}
		payload.WeeklyPaceSmoothingMinutes = value
	}
	if len(payload.WeeklyWorkingDays) > 0 {
		var value string
		if json.Unmarshal(payload.WeeklyWorkingDays, &value) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Invalid weekly pace working days")
			return
		}
		payload.WeeklyPaceWorkingDays = value
	}
	for _, field := range []struct {
		raw     json.RawMessage
		dst     **int
		maximum int
	}{
		{payload.CreateLimit, &payload.ProxyAccountResponseCreateLimitOverride, 0},
		{payload.StreamLimit, &payload.ProxyAccountStreamLimitOverride, 0},
		{payload.RecoveryReserve, &payload.ProxyAccountStreamRecoveryReserveOverride, 0},
		{payload.FairShareThreshold, &payload.ProxyApiKeyFairShareCongestionThresholdPctOverride, 100},
	} {
		if len(field.raw) == 0 {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(field.raw), []byte("null")) {
			*field.dst = nil
			continue
		}
		var value int
		if json.Unmarshal(field.raw, &value) != nil || value < 0 || field.maximum > 0 && value > field.maximum {
			writeError(w, http.StatusBadRequest, "invalid_request", "Invalid account admission setting")
			return
		}
		*field.dst = &value
	}
	stream := settings.ProxyAccountStreamLimitEnvironmentValue
	if payload.ProxyAccountStreamLimitOverride != nil {
		stream = *payload.ProxyAccountStreamLimitOverride
	}
	reserve := settings.ProxyAccountStreamRecoveryReserveEnvironmentValue
	if payload.ProxyAccountStreamRecoveryReserveOverride != nil {
		reserve = *payload.ProxyAccountStreamRecoveryReserveOverride
	}
	if stream > 0 && reserve > stream {
		writeError(w, http.StatusBadRequest, "invalid_proxy_account_stream_recovery_reserve", "Recovery reserve exceeds account stream limit")
		return
	}
	payload.Version = settings.Version
	if err := s.store.SaveSettings(r.Context(), payload.RuntimeSettings); err != nil {
		s.fail(w, err)
		return
	}
	if s.catalog != nil && payload.CodexClientVersion != settings.CodexClientVersion {
		s.catalog.RequestRefresh()
	}
	s.getSettings(w, r)
}

type usageSummary struct {
	RequestCount      int64   `json:"requestCount"`
	TotalTokens       int64   `json:"totalTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	TotalCostUSD      float64 `json:"totalCostUsd"`
}

func summarizeUsage(totals domain.UsageTotals) usageSummary {
	return usageSummary{totals.RequestCount, totals.Usage.InputTokens + totals.Usage.OutputTokens, totals.Usage.CachedInputTokens, float64(totals.Usage.CostMicrodollars) / 1e6}
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	type windows struct {
		Primary   *float64 `json:"primaryRemainingPercent"`
		Secondary *float64 `json:"secondaryRemainingPercent"`
		Monthly   *float64 `json:"monthlyRemainingPercent"`
	}
	type accountView struct {
		domain.Account
		DisplayName             string          `json:"displayName"`
		SubscriptionActiveUntil *time.Time      `json:"subscriptionActiveUntil"`
		Auth                    accountAuthView `json:"auth"`
		Usage                   windows         `json:"usage"`
		ResetPrimary            *time.Time      `json:"resetAtPrimary"`
		ResetSecondary          *time.Time      `json:"resetAtSecondary"`
		ResetMonthly            *time.Time      `json:"resetAtMonthly"`
		WindowMinutesPrimary    *int            `json:"windowMinutesPrimary"`
		WindowMinutesSecondary  *int            `json:"windowMinutesSecondary"`
		WindowMinutesMonthly    *int            `json:"windowMinutesMonthly"`
		CapacityPrimary         *float64        `json:"capacityCreditsPrimary"`
		RemainingPrimary        *float64        `json:"remainingCreditsPrimary"`
		CapacitySecondary       *float64        `json:"capacityCreditsSecondary"`
		RemainingSecondary      *float64        `json:"remainingCreditsSecondary"`
		CapacityMonthly         *float64        `json:"capacityCreditsMonthly"`
		RemainingMonthly        *float64        `json:"remainingCreditsMonthly"`
		*accountUsageDetails
		AvailableResetCredits    *int         `json:"availableResetCredits"`
		ResetCreditNearestExpiry *time.Time   `json:"resetCreditNearestExpiresAt"`
		RequestUsage             usageSummary `json:"requestUsage"`
	}
	views := make([]accountView, 0, len(accounts))
	for _, account := range accounts {
		quotas, err := s.store.ListAccountQuota(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		creditsStatus, err := s.store.LoadAccountCreditStatus(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		additionalQuotas, err := s.store.ListAccountAdditionalQuotas(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		totals, err := s.store.UsageTotals(r.Context(), "", account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		view := accountView{Account: account, DisplayName: account.Alias, RequestUsage: summarizeUsage(totals)}
		var refusalAt *time.Time
		if account.Status == domain.AccountQuotaExceeded {
			refusalAt, err = s.store.LoadAccountQuotaRefusalAt(r.Context(), account.ID)
			if err != nil {
				s.fail(w, err)
				return
			}
		}
		view.Status = application.EffectiveAccountQuotaStatus(account, quotas, creditsStatus, refusalAt, time.Now().UTC())
		credential, err := s.store.GetAccountCredential(r.Context(), account.ID)
		if err == nil {
			view.Auth, view.SubscriptionActiveUntil = accountAuthentication(s.cipher, credential)
		}
		if view.DisplayName == "" {
			view.DisplayName = account.Email
		}
		primary, secondary, monthly := accountQuotasView(quotas, account.PlanType)
		view.Usage.Primary, view.Usage.Secondary = primary.RemainingPercent, secondary.RemainingPercent
		view.Usage.Monthly = monthly.RemainingPercent
		view.ResetPrimary, view.ResetSecondary = primary.ResetAt, secondary.ResetAt
		view.ResetMonthly = monthly.ResetAt
		view.WindowMinutesPrimary, view.WindowMinutesSecondary = primary.WindowMinutes, secondary.WindowMinutes
		view.WindowMinutesMonthly = monthly.WindowMinutes
		view.CapacityPrimary, view.RemainingPrimary = primary.CapacityCredits, primary.RemainingCredits
		view.CapacitySecondary, view.RemainingSecondary = secondary.CapacityCredits, secondary.RemainingCredits
		view.CapacityMonthly, view.RemainingMonthly = monthly.CapacityCredits, monthly.RemainingCredits
		view.accountUsageDetails = accountUsageDetailsFromStored(creditsStatus, additionalQuotas, settings.AdditionalQuotaRoutingPolicies)
		credits := accountResetCreditsView(r.Context(), s.usage, account)
		view.AvailableResetCredits, view.ResetCreditNearestExpiry = credits.AvailableCount, credits.NearestExpiresAt
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, struct {
		Accounts []accountView `json:"accounts"`
	}{views})
}
