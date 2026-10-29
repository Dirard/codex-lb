package httpapi

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// keyUsageCSVHeader is the column order of the per-day export (R31).
var keyUsageCSVHeader = []string{"date", "group_id", "group_name", "key_id", "key_name", "requests", "input_tokens",
	"output_tokens", "cached_input_tokens", "reasoning_tokens", "cost_usd", "errors", "cancelled"}

// registerKeyUsageReportRoutes mounts the reports for API-key users inside the
// authenticated, compressed /api mux (dashboard redesign use case 5).
func (s *Server) registerKeyUsageReportRoutes(mux *http.ServeMux) {
	service := application.NewKeyUsageReportService(s.store, nil)
	for _, path := range []string{"/api/reports/keys", "/api/reports/keys/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params, ok := keyUsageReportParams(w, r)
			if !ok {
				return
			}
			report, err := service.Report(r.Context(), params)
			if err != nil {
				s.failKeyUsageReport(w, err)
				return
			}
			writeJSON(w, http.StatusOK, report)
		})))
	}
	mux.Handle("GET /api/reports/keys/export.csv", serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params, ok := keyUsageReportParams(w, r)
		if !ok {
			return
		}
		// The rows are computed in a read snapshot that is closed before
		// anything is written to the client.
		export, err := service.Export(r.Context(), params)
		if err != nil {
			s.failKeyUsageReport(w, err)
			return
		}
		header := w.Header()
		header.Set("Content-Type", "text/csv; charset=utf-8")
		header.Set("Content-Disposition", `attachment; filename="key-usage-`+export.Period.Start+`_`+export.Period.End+`.csv"`)
		w.WriteHeader(http.StatusOK)
		out := csv.NewWriter(w)
		if err := out.Write(keyUsageCSVHeader); err != nil {
			return
		}
		record := make([]string, len(keyUsageCSVHeader))
		for row := range export.Rows() {
			groupID := ""
			if row.GroupID != nil {
				groupID = *row.GroupID
			}
			reasoning := ""
			if tokens := row.Usage.ReasoningKnownTokens(); tokens != nil {
				reasoning = strconv.FormatInt(*tokens, 10)
			}
			record = append(record[:0], row.Date, csvText(groupID), csvText(row.GroupName), csvText(row.KeyID), csvText(row.KeyName),
				strconv.FormatInt(row.Usage.Requests, 10), strconv.FormatInt(row.Usage.InputTokens, 10),
				strconv.FormatInt(row.Usage.OutputTokens, 10), strconv.FormatInt(row.Usage.CachedInputTokens, 10), reasoning,
				strconv.FormatFloat(domain.RoundCostUSD(row.Usage.CostUSD), 'f', 6, 64),
				strconv.FormatInt(row.Usage.Errors, 10), strconv.FormatInt(row.Usage.Cancelled, 10))
			if err := out.Write(record); err != nil {
				// The client went away; the snapshot is already closed.
				return
			}
		}
		out.Flush()
	})))
}

// csvText keeps a spreadsheet from evaluating a text cell as a formula.
func csvText(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}

// keyUsageReportQuery lists the parameters of the key reports; any other
// parameter is refused so that a mistyped filter never widens a report.
var keyUsageReportQuery = map[string]bool{"group_id": true, "api_key_id": true, "ungrouped": true,
	"start_date": true, "end_date": true, "timezone": true}

// keyUsageReportParams validates the transport parameters of the key reports.
func keyUsageReportParams(w http.ResponseWriter, r *http.Request) (application.KeyUsageReportParams, bool) {
	query := r.URL.Query()
	for name, values := range query {
		repeatable := name == "group_id" || name == "api_key_id"
		if !keyUsageReportQuery[name] || (!repeatable && len(values) > 1) {
			writeError(w, http.StatusBadRequest, "invalid_report_filters", "Unknown or repeated report parameter")
			return application.KeyUsageReportParams{}, false
		}
	}
	var ungrouped bool
	switch query.Get("ungrouped") {
	case "", "0", "false":
	case "1", "true":
		ungrouped = true
	default:
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "ungrouped must be 1 or 0")
		return application.KeyUsageReportParams{}, false
	}
	loc, ok := requestTimezone(w, r)
	if !ok {
		return application.KeyUsageReportParams{}, false
	}
	return application.KeyUsageReportParams{StartDate: query.Get("start_date"), EndDate: query.Get("end_date"),
		Location: loc, GroupIDs: query["group_id"], KeyIDs: query["api_key_id"], Ungrouped: ungrouped}, true
}

func (s *Server) failKeyUsageReport(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrReportPeriod):
		writeError(w, http.StatusBadRequest, "invalid_report_date_range", "Invalid report date range: at most 731 days, start on or before end")
	case errors.Is(err, application.ErrReportFilters):
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "Invalid report selection")
	case errors.Is(err, domain.ErrReportTooLarge):
		writeError(w, http.StatusBadRequest, "report_too_large", "The selection times the period is too large; narrow the selection or shorten the period")
	default:
		s.fail(w, err)
	}
}
