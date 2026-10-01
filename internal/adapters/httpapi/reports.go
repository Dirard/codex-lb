package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// registerReportsRoutes is mounted inside the existing authenticated /api mux.
func (s *Server) registerReportsRoutes(mux *http.ServeMux, repo application.ReportsRepository) {
	service := application.NewReportsService(repo, nil)
	mux.HandleFunc("GET /api/dashboard/overview", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.DashboardOverview(r.Context(), r.URL.Query().Get("timeframe"), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/dashboard/projections", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.DashboardProjections(r.Context(), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/api-keys/{id}/trends", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.APIKeyTrends(r.Context(), r.PathValue("id"), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/api-keys/{id}/usage-7d", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.APIKeyUsage7Day(r.Context(), r.PathValue("id"), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/accounts/{id}/trends", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.AccountTrends(r.Context(), r.PathValue("id"), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	for _, path := range []string{"/api/conversations", "/api/conversations/{$}"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			params, ok := parseConversationParams(w, r)
			if !ok {
				return
			}
			response, err := service.Conversations(r.Context(), params)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, response)
		})
	}
	mux.HandleFunc("GET /api/conversations/{id...}", func(w http.ResponseWriter, r *http.Request) {
		response, err := service.ConversationDetails(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	for _, path := range []string{"/api/reports", "/api/reports/{$}"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			response, err := service.Reports(r.Context(), application.ReportParams{
				StartDate: q.Get("start_date"), EndDate: q.Get("end_date"), Timezone: q.Get("timezone"),
				AccountIDs: q["account_id"], APIKeyIDs: q["api_key_id"],
				Model: q.Get("model"), UserAgentGroup: q.Get("useragent_group")})
			if err != nil {
				if errors.Is(err, domain.ErrInvalid) {
					writeError(w, http.StatusBadRequest, "invalid_report_date_range", "Invalid report date range or filters")
				} else {
					s.fail(w, err)
				}
				return
			}
			writeJSON(w, http.StatusOK, response)
		})
	}
	for _, path := range []string{"/api/request-logs", "/api/request-logs/{$}"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			params, ok := parseRequestLogParams(w, r)
			if !ok {
				return
			}
			response, err := service.RequestLogs(r.Context(), params)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, response)
		})
	}
	mux.HandleFunc("GET /api/request-logs/options", func(w http.ResponseWriter, r *http.Request) {
		params, ok := parseRequestLogParams(w, r)
		if !ok {
			return
		}
		response, err := service.RequestLogOptions(r.Context(), params)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func parseRequestLogParams(w http.ResponseWriter, r *http.Request) (application.RequestLogParams, bool) {
	q := r.URL.Query()
	p := application.RequestLogParams{Search: q.Get("search"), ConversationID: q.Get("conversation_id"),
		Timeframe: q.Get("timeframe"), AccountIDs: q["accountId"], APIKeyIDs: q["apiKeyId"],
		Statuses: q["status"], Models: q["model"], ReasoningEfforts: q["reasoningEffort"],
		ModelOptions: q["modelOption"]}
	if q.Has("limit") {
		value, err := strconv.Atoi(q.Get("limit"))
		if err != nil || value < 1 || value > 1000 {
			writeError(w, 422, "invalid_request", "Invalid request log limit")
			return p, false
		}
		p.Limit = value
	}
	if q.Has("offset") {
		value, err := strconv.Atoi(q.Get("offset"))
		if err != nil || value < 0 {
			writeError(w, 422, "invalid_request", "Invalid request log offset")
			return p, false
		}
		p.Offset = value
	}
	for _, part := range []struct {
		name string
		dst  **time.Time
	}{{"since", &p.Since}, {"until", &p.Until}} {
		if raw := q.Get(part.name); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				writeError(w, 422, "invalid_request", "Invalid request log time")
				return p, false
			}
			*part.dst = &value
		}
	}
	return p, true
}

func parseConversationParams(w http.ResponseWriter, r *http.Request) (application.ConversationParams, bool) {
	q := r.URL.Query()
	p := application.ConversationParams{Search: q.Get("search"), Timeframe: q.Get("timeframe")}
	if q.Has("limit") {
		value, err := strconv.Atoi(q.Get("limit"))
		if err != nil || value < 1 || value > 1000 {
			writeError(w, 422, "invalid_request", "Invalid conversation limit")
			return p, false
		}
		p.Limit = value
	}
	if q.Has("offset") {
		value, err := strconv.Atoi(q.Get("offset"))
		if err != nil || value < 0 {
			writeError(w, 422, "invalid_request", "Invalid conversation offset")
			return p, false
		}
		p.Offset = value
	}
	if raw := q.Get("since"); raw != "" {
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, 422, "invalid_request", "Invalid conversation time")
			return p, false
		}
		p.Since = &value
	}
	return p, true
}
