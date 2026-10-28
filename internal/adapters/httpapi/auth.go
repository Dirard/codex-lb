package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"rsc.io/qr"
)

const (
	sessionCookie       = "codex_lb_dashboard_session"
	secureSessionCookie = "__Host-" + sessionCookie
	// authBodyLimit bounds every unauthenticated authentication request body.
	authBodyLimit = 8 << 10
)

func formatInt(value int64) string { return strconv.FormatInt(value, 10) }
func errorType(err error) string   { return fmt.Sprintf("%T", err) }

// secureRequest reports whether browsers reach this request over HTTPS: the
// operator declared an HTTPS public origin, TLS terminated here, or a trusted
// proxy reported https.
func (s *Server) secureRequest(r *http.Request) bool {
	if s.config.PublicOrigin != "" || r.TLS != nil {
		return true
	}
	id := ResolveIdentity(r, s.config.TrustedProxies)
	return id.TrustedPeer && id.IP.IsValid() && len(r.Header.Values("X-Forwarded-Proto")) == 1 && r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) adminCookieName(r *http.Request) string {
	if s.secureRequest(r) {
		return secureSessionCookie
	}
	return sessionCookie
}

func (s *Server) sessionToken(r *http.Request) string {
	cookies := r.CookiesNamed(s.adminCookieName(r))
	if len(cookies) != 1 {
		return ""
	}
	return cookies[0].Value
}

// clientKey is the rate-limit identity of the caller after trusted-proxy resolution.
func (s *Server) clientKey(r *http.Request) string {
	return ResolveIdentity(r, s.config.TrustedProxies).IP.String()
}

// limitBody rejects oversized bodies before a handler reads them.
func limitBody(limit int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next(w, r)
	}
}

func (s *Server) registerAuth(mux *http.ServeMux) {
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, limitBody(authBodyLimit, handler))
	}
	handle("GET /api/dashboard-auth/session", s.authSession)
	handle("POST /api/dashboard-auth/password/setup", s.passwordSetup)
	handle("POST /api/dashboard-auth/password/login", s.passwordLogin)
	handle("POST /api/dashboard-auth/password/change", s.passwordChange)
	handle("DELETE /api/dashboard-auth/password", s.passwordRemove)
	handle("POST /api/dashboard-auth/logout", s.logout)
	handle("POST /api/dashboard-auth/logout-all", s.logoutAll)
	handle("POST /api/dashboard-auth/totp/setup/start", s.totpStart)
	handle("POST /api/dashboard-auth/totp/setup/confirm", s.totpConfirm)
	handle("POST /api/dashboard-auth/totp/verify", s.totpVerify)
	handle("POST /api/dashboard-auth/totp/disable", s.totpVerify)
}

func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	s.respondSession(w, r, s.sessionToken(r))
}

func (s *Server) respondSession(w http.ResponseWriter, r *http.Request, token string) {
	state, err := s.dashboardState(r, token)
	if err != nil {
		s.fail(w, err)
		return
	}
	state.BootstrapTokenSource = s.auth.BootstrapSource()
	state.BootstrapTokenConfigured = state.BootstrapTokenSource != ""
	state.BootstrapTokenRequired = state.BootstrapRequired && !s.alternativeAdmin(r)
	writeJSON(w, http.StatusOK, state)
}

// sessionTTL applies the configured lifetime to every client: locality is not
// an authorization input, so no request earns a longer session.
func (s *Server) sessionTTL(r *http.Request) (time.Duration, error) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		return 0, err
	}
	ttl := time.Duration(settings.DashboardSessionTTL) * time.Second
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return min(ttl, application.MaxDashboardSessionTTL), nil
}

func (s *Server) cookie(w http.ResponseWriter, r *http.Request, token string, seconds int) {
	s.sessionCookie(w, r, s.adminCookieName(r), "/", token, seconds)
}

// clearAdminCookies expires both cookie names so a change of secure mode
// cannot leave a stale session cookie behind.
func (s *Server) clearAdminCookies(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if s.secureRequest(r) {
		http.SetCookie(w, &http.Cookie{Name: secureSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	}
}

func (s *Server) sessionCookie(w http.ResponseWriter, r *http.Request, name, path, token string, seconds int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: path, HttpOnly: true, Secure: s.secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: seconds})
}

// authFail logs rejected authentication without submitted secrets, then
// writes the public error.
func (s *Server) authFail(w http.ResponseWriter, r *http.Request, err error) {
	var auth *application.AuthError
	if errors.As(err, &auth) {
		switch auth.Code {
		case "invalid_credentials", "invalid_totp_code", "invalid_bootstrap_token", "login_rate_limited", "reauthentication_required":
			s.logger.Warn("dashboard authentication rejected", "reason", auth.Code, "client", s.clientKey(r), "path", r.URL.Path)
		}
	}
	s.fail(w, err)
}

func (s *Server) passwordSetup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password       string `json:"password"`
		BootstrapToken string `json:"bootstrapToken"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, err := s.sessionTTL(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.auth.SetupPassword(r.Context(), s.clientKey(r), payload.BootstrapToken, payload.Password, !s.alternativeAdmin(r), ttl)
	if err != nil {
		s.authFail(w, r, err)
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
	token, err := s.auth.Login(r.Context(), s.clientKey(r), payload.Password, ttl)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	state, err := s.auth.State(r.Context(), token)
	if err != nil {
		s.fail(w, err)
		return
	}
	if state.TOTPRequiredOnLogin {
		ttl = min(ttl, application.PendingTOTPSessionTTL)
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
	token, err := s.auth.ChangePassword(r.Context(), s.sessionToken(r), s.clientKey(r), payload.Current, payload.New, ttl)
	if err != nil {
		s.authFail(w, r, err)
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
	if err := s.auth.RemovePassword(r.Context(), s.sessionToken(r), s.clientKey(r), payload.Password); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.clearAdminCookies(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), s.sessionToken(r)); err != nil {
		s.fail(w, err)
		return
	}
	s.clearAdminCookies(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.LogoutAll(r.Context(), s.sessionToken(r)); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.clearAdminCookies(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) totpStart(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.StartTOTP(r.Context(), s.sessionToken(r))
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
	token, err := s.auth.ConfirmTOTP(r.Context(), s.sessionToken(r), s.clientKey(r), payload.Secret, payload.Code, ttl)
	if err != nil {
		s.authFail(w, r, err)
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
	token, err := s.auth.VerifyTOTP(r.Context(), s.sessionToken(r), s.clientKey(r), payload.Code, ttl, disable)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.cookie(w, r, token, int(ttl.Seconds()))
	if disable {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		s.respondSession(w, r, token)
	}
}

// confirmPassword enforces password re-entry for a sensitive action unless the
// installation authenticates administrators outside dashboard passwords.
func (s *Server) confirmPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	if s.alternativeAdmin(r) {
		return true
	}
	if err := s.auth.Reauthenticate(r.Context(), s.sessionToken(r), s.clientKey(r), password); err != nil {
		s.authFail(w, r, err)
		return false
	}
	return true
}
