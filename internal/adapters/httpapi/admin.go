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
	for _, path := range []string{"/api/account-groups/{id}/reset-usage", "/api/account-groups/{id}/reset-usage/{$}"} {
		mux.HandleFunc("POST "+path, s.resetGroupUsage)
	}
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
	mux.Handle("GET /api/accounts", serverTiming(http.HandlerFunc(s.listAccounts)))
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

func (s *Server) resetGroupUsage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ResetGroupUsage(r.Context(), r.PathValue("id"), time.Now().UTC()); err != nil {
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
	days := settings.EffectiveRequestLogRetentionDays()
	historyDays := settings.EffectiveUsageHistoryRetentionDays()
	// A configured second factor is always required at sign-in; the stored
	// flag no longer controls login and is reported as the derived state.
	settings.TOTPRequiredOnLogin = len(secret.TOTPSecretEncrypted) != 0
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
	// totpRequiredOnLogin is read-only: TOTP is required whenever configured.
	payload.TOTPRequiredOnLogin = settings.TOTPRequiredOnLogin
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

// accountWindowsView is the remaining percent of each subscription window.
type accountWindowsView struct {
	Primary   *float64 `json:"primaryRemainingPercent"`
	Secondary *float64 `json:"secondaryRemainingPercent"`
	Monthly   *float64 `json:"monthlyRemainingPercent"`
}

// accountUsage7dView is an account's usage over the last 7 days.
type accountUsage7dView struct {
	Requests    int64   `json:"requests"`
	TotalTokens int64   `json:"totalTokens"`
	CostUSD     float64 `json:"costUsd"`
}

// accountListView is one row of GET /api/accounts. The embedded account's
// own displayName (which falls back to the id) is shadowed by DisplayName.
type accountListView struct {
	domain.Account
	DisplayName             string             `json:"displayName"`
	SubscriptionActiveUntil *time.Time         `json:"subscriptionActiveUntil"`
	Auth                    accountAuthView    `json:"auth"`
	Usage                   accountWindowsView `json:"usage"`
	ResetPrimary            *time.Time         `json:"resetAtPrimary"`
	ResetSecondary          *time.Time         `json:"resetAtSecondary"`
	ResetMonthly            *time.Time         `json:"resetAtMonthly"`
	WindowMinutesPrimary    *int               `json:"windowMinutesPrimary"`
	WindowMinutesSecondary  *int               `json:"windowMinutesSecondary"`
	WindowMinutesMonthly    *int               `json:"windowMinutesMonthly"`
	CapacityPrimary         *float64           `json:"capacityCreditsPrimary"`
	RemainingPrimary        *float64           `json:"remainingCreditsPrimary"`
	CapacitySecondary       *float64           `json:"capacityCreditsSecondary"`
	RemainingSecondary      *float64           `json:"remainingCreditsSecondary"`
	CapacityMonthly         *float64           `json:"capacityCreditsMonthly"`
	RemainingMonthly        *float64           `json:"remainingCreditsMonthly"`
	*accountUsageDetails
	AvailableResetCredits    *int               `json:"availableResetCredits"`
	ResetCreditNearestExpiry *time.Time         `json:"resetCreditNearestExpiresAt"`
	RequestUsage             usageSummary       `json:"requestUsage"`
	Groups                   []string           `json:"groups"`
	Usage7d                  accountUsage7dView `json:"usage7d"`
	ResetCreditsObservedAt   *time.Time         `json:"resetCreditsObservedAt"`
}

// listAccounts is GET /api/accounts (dashboard redesign use case 8): one
// batch read of every account, the settings and the shell status, a fixed
// number of statements for any number of accounts, and no provider call.
// Only the token ciphertexts whose state and expiry the view returns are
// decrypted.
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	views, err := s.store.ListAccountViews(r.Context(), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	shell, err := s.shellStatus().Status(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	rows := make([]accountListView, 0, len(views.Accounts))
	for _, entry := range views.Accounts {
		account := entry.Account
		view := accountListView{Account: account, DisplayName: account.Alias, RequestUsage: summarizeUsage(entry.Totals),
			Groups: entry.GroupIDs, Usage7d: accountUsage7dView{Requests: entry.Usage7d.Requests,
				TotalTokens: entry.Usage7d.InputTokens + entry.Usage7d.OutputTokens, CostUSD: entry.Usage7d.CostUSD}}
		view.Status = application.EffectiveAccountQuotaStatus(account, entry.Quotas, entry.Credits, entry.RefusalAt, now)
		if entry.Credential != nil {
			view.Auth, view.SubscriptionActiveUntil = accountAuthentication(s.cipher, *entry.Credential)
		}
		if view.DisplayName == "" {
			view.DisplayName = account.Email
		}
		primary, secondary, monthly := accountQuotasView(entry.Quotas, account.PlanType)
		view.Usage = accountWindowsView{primary.RemainingPercent, secondary.RemainingPercent, monthly.RemainingPercent}
		view.ResetPrimary, view.ResetSecondary, view.ResetMonthly = primary.ResetAt, secondary.ResetAt, monthly.ResetAt
		view.WindowMinutesPrimary, view.WindowMinutesSecondary = primary.WindowMinutes, secondary.WindowMinutes
		view.WindowMinutesMonthly = monthly.WindowMinutes
		view.CapacityPrimary, view.RemainingPrimary = primary.CapacityCredits, primary.RemainingCredits
		view.CapacitySecondary, view.RemainingSecondary = secondary.CapacityCredits, secondary.RemainingCredits
		view.CapacityMonthly, view.RemainingMonthly = monthly.CapacityCredits, monthly.RemainingCredits
		view.accountUsageDetails = accountUsageDetailsFromStored(entry.Credits, entry.AdditionalQuotas, settings.AdditionalQuotaRoutingPolicies)
		if s.usage != nil {
			if credits := s.usage.CachedResetCredits(account); credits != nil {
				observed := credits.ObservedAt
				view.AvailableResetCredits, view.ResetCreditNearestExpiry = &credits.AvailableCount, credits.NearestExpiresAt
				if !observed.IsZero() {
					view.ResetCreditsObservedAt = &observed
				}
			}
		}
		rows = append(rows, view)
	}
	writeJSON(w, http.StatusOK, struct {
		Accounts []accountListView  `json:"accounts"`
		Shell    domain.ShellStatus `json:"shell"`
	}{rows, shell})
}
