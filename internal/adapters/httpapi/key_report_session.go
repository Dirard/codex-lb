package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const keyReportCookie = "codex_lb_key_report_session"
const keyReportCookiePath = "/api/key-reports"

type keyReportSessionState struct {
	Authenticated bool `json:"authenticated"`
}

// registerKeyReportSession keeps browser grants outside admin and bearer APIs.
func (s *Server) registerKeyReportSession(mux *http.ServeMux) {
	auth := application.NewKeyReportAuth(s.store, s.cipher)
	reports := application.NewReportsService(s.store, nil)
	for _, suffix := range []string{"", "/{$}"} {
		mux.HandleFunc("POST /api/key-reports/session"+suffix, func(w http.ResponseWriter, r *http.Request) {
			key, err := authenticateBearerKey(r, s.store)
			if err != nil || domain.IsInternalKey(key.ID) {
				if err == nil {
					err = invalidKey()
				}
				writeProxyError(w, err)
				return
			}
			ttl, err := s.sessionTTL(r)
			if err != nil {
				s.fail(w, err)
				return
			}
			token, expires, err := auth.Issue(key, ttl)
			if err != nil {
				s.fail(w, err)
				return
			}
			s.sessionCookie(w, r, keyReportCookie, keyReportCookiePath, token, max(1, int(time.Until(expires).Seconds())))
			writeJSON(w, http.StatusOK, keyReportSessionState{Authenticated: true})
		})
		mux.HandleFunc("GET /api/key-reports/session"+suffix, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Cookie")
			_, err := auth.Authenticate(r.Context(), keyReportToken(r))
			if err != nil && !errors.Is(err, application.ErrAuthentication) {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, keyReportSessionState{Authenticated: err == nil})
		})
		mux.HandleFunc("DELETE /api/key-reports/session"+suffix, func(w http.ResponseWriter, r *http.Request) {
			s.sessionCookie(w, r, keyReportCookie, keyReportCookiePath, "", -1)
			writeJSON(w, http.StatusOK, keyReportSessionState{})
		})
		mux.HandleFunc("GET /api/key-reports/reports"+suffix, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Cookie")
			key, err := auth.Authenticate(r.Context(), keyReportToken(r))
			if err != nil {
				s.fail(w, err)
				return
			}
			serveKeyReports(w, r, reports, key)
		})
	}
}

func keyReportToken(r *http.Request) string {
	cookies := r.CookiesNamed(keyReportCookie)
	if len(cookies) != 1 {
		return ""
	}
	return cookies[0].Value
}

func keyReportFirewallPath(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	return path == "/api/key-reports/reports" || path == "/api/key-reports/session" && r.Method == http.MethodPost
}
