package httpapi

import (
	"net/http"
	"time"

	"codex-lb/internal/application"
)

// shellStatus is the dashboard shell status use case of this server. The
// reset-credit counter is the usage service when it is configured.
func (s *Server) shellStatus() *application.ShellStatusService {
	var credits application.ResetCreditCounter
	if s.usage != nil {
		credits = s.usage
	}
	return application.NewShellStatusService(s.store, credits)
}

// registerOverviewRoutes mounts the overview, the directory and the single
// key read inside the authenticated, compressed /api mux.
func (s *Server) registerOverviewRoutes(mux *http.ServeMux) {
	shell := s.shellStatus()
	overview := application.NewOverviewService(s.store, shell, nil)
	directory := application.NewDirectoryService(s.store, nil)
	for _, path := range []string{"/api/overview", "/api/overview/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			loc, ok := requestTimezone(w, r)
			if !ok {
				return
			}
			response, err := overview.Overview(r.Context(), loc)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, response)
		})))
	}
	for _, path := range []string{"/api/directory", "/api/directory/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response, err := directory.Directory(r.Context())
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, response)
		})))
	}
	mux.HandleFunc("GET /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		key, err := s.store.GetAPIKey(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, key)
	})
}

// requestTimezone parses the optional IANA timezone parameter; an empty value
// is UTC and an unknown zone answers 400 invalid_timezone.
func requestTimezone(w http.ResponseWriter, r *http.Request) (*time.Location, bool) {
	name := r.URL.Query().Get("timezone")
	if name == "" {
		return time.UTC, true
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" || len(name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_timezone", "Unknown time zone")
		return nil, false
	}
	return loc, true
}
