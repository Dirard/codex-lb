package httpapi

import (
	"net/http"
)

// registerDashboardStatusRoute is mounted inside the authenticated /api mux.
// It returns the shell status (dashboard redesign use case 1): readiness,
// quota sync and rollup fold times, re-login and reset-credit counts and the
// settings the header and status bar show, from in-memory state and a fixed
// number of reads. Pages with their own poll receive the same object in it.
func (s *Server) registerDashboardStatusRoute(mux *http.ServeMux) {
	shell := s.shellStatus()
	mux.Handle("GET /api/dashboard/status", serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := shell.Status(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
}
