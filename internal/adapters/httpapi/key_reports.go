package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// serveReports authenticates first and binds every report filter to that key.
func (h *KeyUsageHandler) serveReports(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	key, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "Invalid report filters")
		return
	}
	for name, values := range query {
		switch name {
		case "start_date", "end_date", "timezone", "model", "useragent_group":
			if len(values) == 1 {
				continue
			}
		}
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "Invalid report filters")
		return
	}
	report, err := h.reports.Reports(r.Context(), application.ReportParams{
		StartDate: query.Get("start_date"), EndDate: query.Get("end_date"), Timezone: query.Get("timezone"),
		Model: query.Get("model"), UserAgentGroup: query.Get("useragent_group"), APIKeyIDs: []string{key.ID},
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "invalid_report_date_range", "Invalid report date range or filters")
		} else {
			h.fail(w, err)
		}
		return
	}
	limits, err := h.reports.KeyLimits(r.Context(), key.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.KeyReportsResponse{
		KeyReportLimitSummary: limits,
		Summary:               report.Summary, Comparison: report.Comparison, Daily: report.Daily,
		ByModel: report.ByModel, ByUserAgent: report.ByUserAgent,
	})
}
