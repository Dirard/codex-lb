package httpapi

import (
	"context"
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// registerWarmupRoutes registers the admin force-probe endpoint backed by the
// shared warmup executor. Handlers stay behind requireAdmin.
func (s *Server) registerWarmupRoutes(mux *http.ServeMux, warmups *application.WarmupService) {
	mux.HandleFunc("POST /api/accounts/{id}/probe", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSON(w, r, &payload) {
				return
			}
		}
		result, err := warmups.ProbeAccount(r.Context(), r.PathValue("id"), payload.Model)
		if err != nil {
			s.writeWarmupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status                     string   `json:"status"`
			AccountID                  string   `json:"accountId"`
			ProbeStatusCode            *int     `json:"probeStatusCode"`
			PrimaryUsedPercentBefore   *float64 `json:"primaryUsedPercentBefore"`
			PrimaryUsedPercentAfter    *float64 `json:"primaryUsedPercentAfter"`
			SecondaryUsedPercentBefore *float64 `json:"secondaryUsedPercentBefore"`
			SecondaryUsedPercentAfter  *float64 `json:"secondaryUsedPercentAfter"`
			AccountStatusBefore        string   `json:"accountStatusBefore"`
			AccountStatusAfter         string   `json:"accountStatusAfter"`
		}{
			result.Status, result.AccountID, result.ProbeStatusCode,
			result.PrimaryBefore, result.PrimaryAfter, result.SecondaryBefore, result.SecondaryAfter,
			result.AccountStatusBefore, result.AccountStatusAfter,
		})
	})
}

func (s *Server) writeWarmupError(w http.ResponseWriter, err error) {
	// Upstream error strings can echo credential material; fixed copy only.
	switch {
	case errors.Is(err, application.ErrWarmupIneligible):
		writeError(w, http.StatusConflict, "account_not_probable",
			"The account is not eligible for probing in its current state")
	case errors.Is(err, application.ErrWarmupQuotaRefused):
		writeError(w, http.StatusBadGateway, "probe_quota_refused",
			"The probe was rejected by the upstream quota limit")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable, "probe_cancelled",
			"The probe was cancelled before completion")
	default:
		// Not-found keeps its domain mapping; everything else gets a fixed
		// generic message with no upstream echo.
		if errors.Is(err, domain.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, "probe_failed",
			"The probe request could not be completed")
	}
}
