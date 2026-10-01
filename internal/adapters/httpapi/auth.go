package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"
)

const sessionCookie = "codex_lb_dashboard_session"

func formatInt(value int64) string { return strconv.FormatInt(value, 10) }
func errorType(err error) string   { return fmt.Sprintf("%T", err) }

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dashboard-auth/session", s.authSession)
	mux.HandleFunc("POST /api/dashboard-auth/password/setup", s.passwordSetup)
	mux.HandleFunc("POST /api/dashboard-auth/password/login", s.passwordLogin)
	mux.HandleFunc("POST /api/dashboard-auth/password/change", s.passwordChange)
	mux.HandleFunc("DELETE /api/dashboard-auth/password", s.passwordRemove)
	mux.HandleFunc("POST /api/dashboard-auth/logout", func(w http.ResponseWriter, r *http.Request) {
		s.cookie(w, r, "", -1)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/dashboard-auth/totp/setup/start", s.totpStart)
	mux.HandleFunc("POST /api/dashboard-auth/totp/setup/confirm", s.totpConfirm)
	mux.HandleFunc("POST /api/dashboard-auth/totp/verify", s.totpVerify)
	mux.HandleFunc("POST /api/dashboard-auth/totp/disable", s.totpVerify)
}

func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	s.respondSession(w, r, sessionToken(r))
}

func (s *Server) respondSession(w http.ResponseWriter, r *http.Request, token string) {
	state, err := s.dashboardState(r, token)
	if err != nil {
		s.fail(w, err)
		return
	}
	state.BootstrapTokenConfigured = s.config.BootstrapToken != ""
	state.BootstrapTokenRequired = state.BootstrapRequired && s.bootstrapTokenRequired(r)
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) bootstrapTokenRequired(r *http.Request) bool {
	return !ResolveIdentity(r, s.config.TrustedProxies).Local && !s.alternativeAdmin(r)
}

func (s *Server) sessionTTL(r *http.Request) (time.Duration, error) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		return 0, err
	}
	ttl := time.Duration(settings.DashboardSessionTTL) * time.Second
	if ttl <= 0 {
		ttl = 365 * 24 * time.Hour
	}
	id := ResolveIdentity(r, s.config.TrustedProxies)
	if ttl > 30*24*time.Hour && (!id.Local || id.Forwarded || s.authMode() != "standard") {
		ttl = 12 * time.Hour
	}
	return ttl, nil
}

func (s *Server) cookie(w http.ResponseWriter, r *http.Request, token string, seconds int) {
	s.sessionCookie(w, r, sessionCookie, "/", token, seconds)
}

func (s *Server) sessionCookie(w http.ResponseWriter, r *http.Request, name, path, token string, seconds int) {
	id := ResolveIdentity(r, s.config.TrustedProxies)
	secure := r.TLS != nil
	if id.TrustedPeer && id.IP.IsValid() && len(r.Header.Values("X-Forwarded-Proto")) == 1 {
		secure = secure || r.Header.Get("X-Forwarded-Proto") == "https"
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: seconds})
}

func (s *Server) passwordSetup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password       string `json:"password"`
		BootstrapToken string `json:"bootstrapToken"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if s.bootstrapTokenRequired(r) {
		expected, supplied := sha256.Sum256([]byte(s.config.BootstrapToken)), sha256.Sum256([]byte(payload.BootstrapToken))
		if s.config.BootstrapToken == "" || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			writeError(w, http.StatusForbidden, "invalid_bootstrap_token", "A valid bootstrap token is required for remote setup")
			return
		}
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.auth.SetupPassword(r.Context(), payload.Password, ttl)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	s.respondSession(w, r, token)
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.auth.Login(r.Context(), ResolveIdentity(r, s.config.TrustedProxies).IP.String(), payload.Password, ttl)
	if err != nil {
		s.fail(w, err)
		return
	}
	state, err := s.auth.State(r.Context(), token)
	if err != nil {
		s.fail(w, err)
		return
	}
	if state.TOTPRequiredOnLogin && ttl > 5*time.Minute {
		ttl = 5 * time.Minute
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	s.respondSession(w, r, token)
}

func (s *Server) passwordChange(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.auth.ChangePassword(r.Context(), sessionToken(r), payload.Current, payload.New, ttl)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) passwordRemove(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.auth.RemovePassword(r.Context(), sessionToken(r), payload.Password); err != nil {
		s.fail(w, err)
		return
	}
	s.cookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) totpStart(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.StartTOTP(r.Context(), sessionToken(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	code, err := qr.Encode(setup.OTPAuthURI, qr.M)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Secret     string `json:"secret"`
		OTPAuthURI string `json:"otpauthUri"`
		QR         string `json:"qrSvgDataUri"`
	}{setup.Secret, setup.OTPAuthURI, "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())})
}

func (s *Server) totpConfirm(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.auth.ConfirmTOTP(r.Context(), sessionToken(r), ResolveIdentity(r, s.config.TrustedProxies).IP.String(), payload.Secret, payload.Code, ttl)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) totpVerify(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	disable := strings.HasSuffix(r.URL.Path, "/disable")
	token, err := s.auth.VerifyTOTP(r.Context(), sessionToken(r), ResolveIdentity(r, s.config.TrustedProxies).IP.String(), payload.Code, ttl, disable)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	if disable {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		s.respondSession(w, r, token)
	}
}
