package httpapi

import (
	"net/http"

	"codex-lb/internal/application"
)

// registerGroupsSummaryRoutes mounts GET /api/groups/summary, the Groups page
// endpoint (dashboard redesign use case 7), inside the authenticated,
// compressed /api mux.
func (s *Server) registerGroupsSummaryRoutes(mux *http.ServeMux) {
	summary := application.NewGroupsSummaryService(s.store, s.shellStatus(), nil)
	for _, path := range []string{"/api/groups/summary", "/api/groups/summary/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			loc, ok := requestTimezone(w, r)
			if !ok {
				return
			}
			response, err := summary.Summary(r.Context(), loc)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, response)
		})))
	}
}
