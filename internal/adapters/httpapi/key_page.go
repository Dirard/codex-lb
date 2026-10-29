package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// keyPageService is the key page use case of this server.
func (s *Server) keyPageService() *application.KeyPageService {
	return application.NewKeyPageService(s.store, s.shellStatus(), nil)
}

// keyPagePageParams and keyPageRequestParams are the only parameters the
// key page routes accept (R25).
var (
	keyPagePageParams    = []string{"start_date", "end_date", "timezone"}
	keyPageRequestParams = []string{"cursor", "limit", "errors_only"}
)

// registerKeyPageRoutes mounts the administrator key page inside the
// authenticated, compressed /api mux.
func (s *Server) registerKeyPageRoutes(mux *http.ServeMux) {
	service := s.keyPageService()
	for _, suffix := range []string{"", "/{$}"} {
		mux.Handle("GET /api/api-keys/{id}/page"+suffix, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params, ok := keyPageParams(w, r)
			if !ok {
				return
			}
			response, err := service.AdminPage(r.Context(), r.PathValue("id"), params)
			if err != nil {
				s.failKeyPage(w, err, "invalid_report_date_range")
				return
			}
			writeJSON(w, http.StatusOK, response)
		})))
		mux.Handle("GET /api/api-keys/{id}/requests"+suffix, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params, ok := keyRequestParams(w, r)
			if !ok {
				return
			}
			response, err := service.Requests(r.Context(), domain.KeyPageAdmin, r.PathValue("id"), params)
			if err != nil {
				s.failKeyPage(w, err, "invalid_report_filters")
				return
			}
			writeJSON(w, http.StatusOK, response)
		})))
	}
}

// registerKeyPageOwnerRoutes mounts the owner's key page beside the report
// session routes (R28): the report cookie alone authenticates, the key is
// revalidated on every read, the firewall applies (keyReportFirewallPath)
// and responses are no-store, vary by cookie and are compressed.
func (s *Server) registerKeyPageOwnerRoutes(mux *http.ServeMux, auth *application.KeyReportAuth, suffix string) {
	service := s.keyPageService()
	owner := func(serve func(w http.ResponseWriter, r *http.Request, key domain.APIKey)) http.Handler {
		return compressJSON(serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			key, err := auth.Authenticate(r.Context(), keyReportToken(r))
			if err != nil {
				s.fail(w, err)
				return
			}
			serve(w, r, key)
		})))
	}
	mux.Handle("GET /api/key-reports/page"+suffix, owner(func(w http.ResponseWriter, r *http.Request, key domain.APIKey) {
		params, ok := keyPageParams(w, r)
		if !ok {
			return
		}
		response, err := service.OwnerPage(r.Context(), key, params)
		if err != nil {
			s.failKeyPage(w, err, "invalid_report_date_range")
			return
		}
		writeJSON(w, http.StatusOK, response)
	}))
	mux.Handle("GET /api/key-reports/requests"+suffix, owner(func(w http.ResponseWriter, r *http.Request, key domain.APIKey) {
		params, ok := keyRequestParams(w, r)
		if !ok {
			return
		}
		response, err := service.Requests(r.Context(), domain.KeyPageOwner, key.ID, params)
		if err != nil {
			s.failKeyPage(w, err, "invalid_report_filters")
			return
		}
		writeJSON(w, http.StatusOK, response)
	}))
}

// failKeyPage answers invalid input with 400 and code, everything else like
// any administrator operation.
func (s *Server) failKeyPage(w http.ResponseWriter, err error, code string) {
	if errors.Is(err, domain.ErrInvalid) {
		writeError(w, http.StatusBadRequest, code, "Invalid key page parameters")
		return
	}
	s.fail(w, err)
}

// keyPageQuery parses the query and rejects any parameter outside allowed or
// given more than once with 400 invalid_report_filters.
func keyPageQuery(w http.ResponseWriter, r *http.Request, allowed []string) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil {
		for name, values := range query {
			if !slices.Contains(allowed, name) || len(values) != 1 {
				err = domain.ErrInvalid
				break
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "Invalid report filters")
		return nil, false
	}
	return query, true
}

func keyPageParams(w http.ResponseWriter, r *http.Request) (application.KeyPageParams, bool) {
	query, ok := keyPageQuery(w, r, keyPagePageParams)
	if !ok {
		return application.KeyPageParams{}, false
	}
	loc, ok := requestTimezone(w, r)
	if !ok {
		return application.KeyPageParams{}, false
	}
	return application.KeyPageParams{StartDate: query.Get("start_date"), EndDate: query.Get("end_date"), Location: loc}, true
}

func keyRequestParams(w http.ResponseWriter, r *http.Request) (application.KeyRequestsParams, bool) {
	query, ok := keyPageQuery(w, r, keyPageRequestParams)
	if !ok {
		return application.KeyRequestsParams{}, false
	}
	params := application.KeyRequestsParams{Cursor: query.Get("cursor")}
	var err error
	if raw := query.Get("limit"); raw != "" {
		params.Limit, err = strconv.Atoi(raw)
		if err == nil && params.Limit == 0 {
			err = domain.ErrInvalid
		}
	}
	if raw := query.Get("errors_only"); err == nil && raw != "" {
		params.ErrorsOnly, err = strconv.ParseBool(raw)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report_filters", "Invalid report filters")
		return params, false
	}
	return params, true
}
