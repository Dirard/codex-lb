package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type securityFixture struct {
	server  *Server
	store   *sqlite.Store
	handler http.Handler
	logs    *lockedBuffer
	dir     string
}

func newSecurityFixture(t *testing.T, config Config) *securityFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "security.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	server := New(store, vault, config, slog.New(slog.NewTextHandler(logs, nil)))
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &securityFixture{server: server, store: store, handler: server.Handler(proxy, ui), logs: logs, dir: dir}
}

// call sends a same-origin JSON request from a direct loopback peer unless mutate changes it.
func (f *securityFixture) call(method, path, body string, cookies []*http.Cookie, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:2455"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:40000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f *securityFixture) setup(t *testing.T, token string) []*http.Cookie {
	t.Helper()
	response := f.call("POST", "/api/dashboard-auth/password/setup", `{"password":"synthetic-password","bootstrapToken":"`+token+`"}`, nil, nil)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("setup: %d %s", response.Code, response.Body.String())
	}
	return response.Result().Cookies()
}

func TestGeneratedBootstrapTokenIsPublishedOnceAndReplacedAfterRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard-bootstrap-token")
	f := newSecurityFixture(t, Config{BootstrapTokenFile: credentials.NewBootstrapTokenFile(path)})
	if err := f.server.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file missing or not private: %v", err)
	}
	raw, _ := os.ReadFile(path)
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 || strings.Contains(f.logs.String(), token) || !strings.Contains(f.logs.String(), path) {
		t.Fatal("token too short, logged, or its path not logged")
	}
	var state application.AuthState
	session := f.call("GET", "/api/dashboard-auth/session", "", nil, nil)
	if json.Unmarshal(session.Body.Bytes(), &state) != nil || !state.BootstrapTokenRequired || state.BootstrapTokenSource != application.BootstrapSourceFile {
		t.Fatalf("session state: %s", session.Body.String())
	}
	if got := f.call("POST", "/api/dashboard-auth/password/setup", `{"password":"synthetic-password"}`, nil, nil); got.Code != http.StatusForbidden {
		t.Fatalf("loopback setup without token: %d", got.Code)
	}
	cookies := f.setup(t, token)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("token file survived setup")
	}
	if got := f.call("DELETE", "/api/dashboard-auth/password", `{"password":"synthetic-password"}`, cookies, nil); got.Code != http.StatusOK {
		t.Fatalf("remove password: %d %s", got.Code, got.Body.String())
	}
	raw, err = os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) == token {
		t.Fatal("password removal did not publish a new token")
	}
	if got := f.call("POST", "/api/dashboard-auth/password/setup", `{"password":"synthetic-password","bootstrapToken":"`+token+`"}`, nil, nil); got.Code != http.StatusForbidden {
		t.Fatalf("used token accepted again: %d", got.Code)
	}
}

func TestSecureModeCookiesHeadersAndForwardingWarning(t *testing.T) {
	f := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken, PublicOrigin: "https://lb.example"})
	cookies := f.setup(t, testBootstrapToken)
	cookie := cookies[0]
	if cookie.Name != secureSessionCookie || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("secure-mode cookie = %+v", cookie)
	}
	for _, path := range []string{"/", "/api/settings"} {
		response := f.call("GET", path, "", cookies, nil)
		csp := response.Header().Get("Content-Security-Policy")
		if response.Code != http.StatusOK || !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
			!strings.Contains(csp, "object-src 'none'") || response.Header().Get("Strict-Transport-Security") == "" {
			t.Fatalf("%s headers: %d %v", path, response.Code, response.Header())
		}
	}
	if csp := f.call("GET", "/v1/models", "", nil, nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("proxy CSP = %q", csp)
	}
	// The public origin is trusted by cross-origin protection.
	if got := f.call("POST", "/api/dashboard-auth/logout", "", cookies, func(r *http.Request) { r.Header.Set("Origin", "https://lb.example") }); got.Code != http.StatusOK {
		t.Fatalf("public origin rejected: %d", got.Code)
	}

	plain := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken})
	plainCookie := plain.setup(t, testBootstrapToken)[0]
	if plainCookie.Name != sessionCookie || plainCookie.Secure || plainCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("plain cookie = %+v", plainCookie)
	}
	if plain.call("GET", "/", "", nil, nil).Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS sent without HTTPS")
	}
	for range 2 {
		plain.call("GET", "/api/dashboard-auth/session", "", nil, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.5") })
	}
	if got := strings.Count(plain.logs.String(), "untrusted loopback peer"); got != 1 {
		t.Fatalf("untrusted forwarding warnings = %d", got)
	}
}

func TestAuthenticationBodiesAreBounded(t *testing.T) {
	f := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken})
	large := `{"password":"` + strings.Repeat("a", 9<<10) + `"}`
	for _, path := range []string{"/api/dashboard-auth/password/login", "/api/dashboard-auth/password/setup", "/api/key-reports/session"} {
		if got := f.call("POST", path, large, nil, nil); got.Code != http.StatusRequestEntityTooLarge || !strings.Contains(got.Body.String(), "request_too_large") {
			t.Fatalf("%s oversized body: %d %s", path, got.Code, got.Body.String())
		}
	}
	chunked := f.call("POST", "/api/dashboard-auth/password/login", large, nil, func(r *http.Request) { r.ContentLength = -1 })
	if chunked.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unsized oversized body: %d", chunked.Code)
	}
}

func TestInstallationWideLoginLimitAcrossProxiedClients(t *testing.T) {
	f := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	f.setup(t, testBootstrapToken)
	login := func(client, password string) *httptest.ResponseRecorder {
		return f.call("POST", "/api/dashboard-auth/password/login", `{"password":"`+password+`"}`, nil, func(r *http.Request) { r.Header.Set("X-Forwarded-For", client) })
	}
	for i := range 10 {
		if got := login(fmt.Sprintf("198.51.100.%d", i+1), "wrong-password"); got.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i, got.Code)
		}
	}
	got := login("203.0.113.9", "synthetic-password")
	if got.Code != http.StatusTooManyRequests || got.Header().Get("Retry-After") == "" {
		t.Fatalf("installation-wide limit: %d %v", got.Code, got.Header())
	}
	if !strings.Contains(f.logs.String(), "dashboard authentication rejected") || strings.Contains(f.logs.String(), "wrong-password") {
		t.Fatal("rejections not logged or logged with the secret")
	}
}

func TestConfiguredTOTPIsAlwaysRequiredAtSignIn(t *testing.T) {
	f := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken})
	cookies := f.setup(t, testBootstrapToken)
	start := f.call("POST", "/api/dashboard-auth/totp/setup/start", "", cookies, nil)
	var setup struct {
		Secret string `json:"secret"`
	}
	if start.Code != http.StatusOK || json.Unmarshal(start.Body.Bytes(), &setup) != nil {
		t.Fatalf("totp start: %d", start.Code)
	}
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	confirm := f.call("POST", "/api/dashboard-auth/totp/setup/confirm", `{"secret":"`+setup.Secret+`","code":"`+testTOTPCode(key, time.Now())+`"}`, cookies, nil)
	if confirm.Code != http.StatusOK {
		t.Fatalf("totp confirm: %d %s", confirm.Code, confirm.Body.String())
	}
	full := confirm.Result().Cookies()
	settings := f.call("GET", "/api/settings", "", full, nil)
	var body map[string]any
	if settings.Code != http.StatusOK || json.Unmarshal(settings.Body.Bytes(), &body) != nil || body["totpRequiredOnLogin"] != true {
		t.Fatalf("settings after confirm: %d %s", settings.Code, settings.Body.String())
	}
	login := f.call("POST", "/api/dashboard-auth/password/login", `{"password":"synthetic-password"}`, nil, nil)
	if login.Code != http.StatusOK || login.Result().Cookies()[0].MaxAge > 300 {
		t.Fatalf("password login: %d", login.Code)
	}
	if got := f.call("GET", "/api/settings", "", login.Result().Cookies(), nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("password-only session reached settings: %d", got.Code)
	}
	delete(body, "totpConfigured")
	delete(body, "requestLogRetentionDays")
	delete(body, "usageHistoryRetentionDays")
	delete(body, "stickyReallocationBudgetThresholdPct")
	body["totpRequiredOnLogin"] = false
	payload, _ := json.Marshal(body)
	f.call("PUT", "/api/settings", string(payload), full, nil)
	if got := f.call("GET", "/api/settings", "", full, nil); !strings.Contains(got.Body.String(), `"totpRequiredOnLogin":true`) {
		t.Fatalf("settings API disabled the TOTP requirement: %s", got.Body.String())
	}
	if got := f.call("GET", "/api/settings", "", login.Result().Cookies(), nil); got.Code != http.StatusUnauthorized {
		t.Fatal("password-only session gained access after a settings update")
	}
}

func TestLogoutRevokesCopiedSessionsAndLogoutAllRevokesEverySession(t *testing.T) {
	f := newSecurityFixture(t, Config{BootstrapToken: testBootstrapToken})
	first := f.setup(t, testBootstrapToken)
	second := f.call("POST", "/api/dashboard-auth/password/login", `{"password":"synthetic-password"}`, nil, nil).Result().Cookies()
	if got := f.call("POST", "/api/dashboard-auth/logout", "", first, nil); got.Code != http.StatusOK {
		t.Fatalf("logout: %d", got.Code)
	}
	if got := f.call("GET", "/api/settings", "", first, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("copied cookie survived logout: %d", got.Code)
	}
	third := f.call("POST", "/api/dashboard-auth/password/login", `{"password":"synthetic-password"}`, nil, nil).Result().Cookies()
	if got := f.call("POST", "/api/dashboard-auth/logout-all", "", third, nil); got.Code != http.StatusOK {
		t.Fatalf("logout-all: %d", got.Code)
	}
	for _, cookies := range [][]*http.Cookie{second, third} {
		if got := f.call("GET", "/api/settings", "", cookies, nil); got.Code != http.StatusUnauthorized {
			t.Fatalf("session survived logout-all: %d", got.Code)
		}
	}
	if got := f.call("POST", "/api/dashboard-auth/logout-all", "", nil, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous logout-all: %d", got.Code)
	}
}

// testTOTPCode computes the RFC 6238 SHA-1 six-digit code for now.
func testTOTPCode(key []byte, now time.Time) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(digest[offset:offset+4])&0x7fffffff)%1000000)
}
