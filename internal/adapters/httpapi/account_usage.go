package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// registerAccountUsageRoutes registers manual reset-credit endpoints. The
// composition root supplies the service (root wires it in Handler next to
// registerAccountRoutes); handlers stay wrapped by requireAdmin.
func (s *Server) registerAccountUsageRoutes(mux *http.ServeMux, usage *application.AccountUsageService) {
	mux.HandleFunc("GET /api/accounts/{id}/usage-reset-credits", func(w http.ResponseWriter, r *http.Request) {
		count, err := usage.UsageResetCreditCount(r.Context(), r.PathValue("id"))
		if err != nil {
			s.writeUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			AccountID string `json:"accountId"`
			Credits   struct {
				Available int `json:"availableCount"`
			} `json:"rateLimitResetCredits"`
		}{r.PathValue("id"), struct {
			Available int `json:"availableCount"`
		}{count}})
	})
	mux.HandleFunc("GET /api/accounts/{id}/rate-limit-reset-credits", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := usage.ResetCreditsSnapshot(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
	consume := func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			RedeemRequestID string `json:"redeemRequestId"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSON(w, r, &payload) {
				return
			}
		}
		var result application.ResetCreditResult
		var err error
		if strings.HasSuffix(r.URL.Path, "/usage-reset-credits/consume") {
			result, err = usage.ConsumeUsageResetCredit(r.Context(), r.PathValue("id"), payload.RedeemRequestID)
		} else {
			result, err = usage.ConsumeResetCredit(r.Context(), r.PathValue("id"), payload.RedeemRequestID)
		}
		if err != nil {
			s.writeUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
	for _, path := range []string{"/api/accounts/{id}/rate-limit-reset-credits/consume", "/api/accounts/{id}/usage-reset-credits/consume"} {
		mux.HandleFunc("POST "+path, consume)
	}
}

func (s *Server) writeUsageError(w http.ResponseWriter, err error) {
	var usageErr *application.UsageError
	if errors.As(err, &usageErr) {
		// Upstream-controlled strings never reach the response verbatim: an
		// error envelope can echo Authorization material back. Only the fixed
		// protocol codes below are surfaced; everything else collapses to a
		// generic code with no upstream message.
		code := "usage_upstream_failed"
		switch usageErr.Code {
		case "network_error", "invalid_request", "invalid_response", "tokenexpired", "token_expired":
			code = usageErr.Code
		}
		status := http.StatusServiceUnavailable
		switch usageErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
			status = usageErr.Status
		}
		writeError(w, status, code, "The upstream usage service rejected the request")
		return
	}
	if errors.Is(err, application.ErrNoAvailableResetCredit) {
		writeError(w, http.StatusConflict, "no_available_reset_credit", "No available reset credit")
		return
	}
	s.fail(w, err)
}

type accountUsageWindowJSON struct {
	UsedPercent   float64 `json:"usedPercent"`
	ResetAt       *int64  `json:"resetAt,omitempty"`
	WindowMinutes *int    `json:"windowMinutes,omitempty"`
}

type accountAdditionalQuotaJSON struct {
	QuotaKey        *string                 `json:"quotaKey"`
	LimitName       string                  `json:"limitName"`
	MeteredFeature  string                  `json:"meteredFeature"`
	DisplayLabel    *string                 `json:"displayLabel"`
	RoutingPolicy   *string                 `json:"routingPolicy"`
	PrimaryWindow   *accountUsageWindowJSON `json:"primaryWindow"`
	SecondaryWindow *accountUsageWindowJSON `json:"secondaryWindow"`
}

// accountUsageDetails is populated from durable quota metadata for the
// account list, including immediately after process restart.
type accountUsageDetails struct {
	CreditsHas       *bool                        `json:"creditsHas"`
	CreditsUnlimited *bool                        `json:"creditsUnlimited"`
	CreditsBalance   *float64                     `json:"creditsBalance"`
	AdditionalQuotas []accountAdditionalQuotaJSON `json:"additionalQuotas"`
}

func accountUsageDetailsFromStored(credits *domain.AccountCreditStatus, quotas []domain.AccountAdditionalQuota, policies map[string]string) *accountUsageDetails {
	details := &accountUsageDetails{AdditionalQuotas: []accountAdditionalQuotaJSON{}}
	if credits != nil {
		details.CreditsHas, details.CreditsUnlimited, details.CreditsBalance = credits.Has, credits.Unlimited, credits.Balance
	}
	positions := make(map[string]int)
	for _, quota := range quotas {
		position, found := positions[quota.QuotaKey]
		if !found {
			label, policy := quota.LimitName, "inherit"
			if definition, known := domain.KnownAdditionalQuota(quota.QuotaKey); known {
				label, policy = definition.DisplayLabel, definition.RoutingPolicy
				if override, present := policies[definition.QuotaKey]; present {
					policy = override
				}
			}
			key := quota.QuotaKey
			position = len(details.AdditionalQuotas)
			positions[key] = position
			details.AdditionalQuotas = append(details.AdditionalQuotas, accountAdditionalQuotaJSON{
				QuotaKey: &key, LimitName: quota.LimitName, MeteredFeature: quota.MeteredFeature,
				DisplayLabel: &label, RoutingPolicy: &policy,
			})
		}
		window := &accountUsageWindowJSON{UsedPercent: quota.UsedPercent, WindowMinutes: quota.WindowMinutes}
		if quota.ResetAt != nil {
			reset := quota.ResetAt.Unix()
			window.ResetAt = &reset
		}
		if quota.Window == "primary" {
			details.AdditionalQuotas[position].PrimaryWindow = window
		} else if quota.Window == "secondary" {
			details.AdditionalQuotas[position].SecondaryWindow = window
		}
	}
	return details
}
