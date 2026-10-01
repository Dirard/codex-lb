package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func reportSessionServer(t *testing.T) (*Server, *sqlite.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		key := domain.APIKey{ID: id, Name: id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-"+id))),
			KeyPrefix: "synthetic", IsActive: true, CreatedAt: now}
		if err := store.SaveAPIKey(context.Background(), key, now); err != nil {
			t.Fatal(err)
		}
	}
	server := New(store, vault, Config{}, nil)
	public := http.NewServeMux()
	NewKeyUsageHandler(store).RegisterPublicRoutes(public)
	public.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authenticateProxyKey(r, store); err != nil {
			writeProxyError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return server, store, server.Handler(public, nil)
}

func reportSessionRequest(handler http.Handler, method, path, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestKeyReportBrowserSessionLifecycle(t *testing.T) {
	server, store, handler := reportSessionServer(t)
	ctx := context.Background()
	admin, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/"} {
		login := reportSessionRequest(handler, "POST", "/api/key-reports/session"+suffix, "synthetic-a", nil)
		cookies := login.Result().Cookies()
		if login.Code != 200 || len(cookies) != 1 || login.Body.String() != "{\"authenticated\":true}\n" {
			t.Fatal("login must issue exactly one report cookie and no credential payload", login.Code)
		}
		cookie := cookies[0]
		if cookie.Name != keyReportCookie || cookie.Path != keyReportCookiePath || cookie.Domain != "" || !cookie.HttpOnly ||
			cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge < 86390 || cookie.MaxAge > 86400 {
			t.Fatal("report cookie attributes incorrect")
		}
		for _, path := range []string{"/api/key-reports/session", "/api/key-reports/reports"} {
			result := reportSessionRequest(handler, "GET", path+suffix, "", cookie)
			if result.Code != 200 || result.Header().Get("Cache-Control") != "no-store" || !strings.Contains(result.Header().Get("Vary"), "Cookie") {
				t.Fatal("cookie session failed or may be cached", path, result.Code)
			}
		}
		// Reconstructing the server keeps access; no per-process session state is required.
		restarted := New(store, server.cipher, Config{}, nil).Handler(nil, nil)
		if got := reportSessionRequest(restarted, "GET", "/api/key-reports/reports", "", cookie); got.Code != 200 {
			t.Fatal("session lost after server restart", got.Code)
		}
		for _, path := range []string{"/api/accounts", "/api/api-keys", "/v1/usage/reports", "/v1/usage/reports/"} {
			if got := reportSessionRequest(handler, "GET", path, "", cookie); got.Code != 401 {
				t.Fatal("report cookie authorized another surface", path, got.Code)
			}
		}
		if got := reportSessionRequest(handler, "POST", "/v1/responses", "", cookie); got.Code != 401 {
			t.Fatal("report cookie authorized generation", got.Code)
		}
		if got := reportSessionRequest(handler, "GET", "/api/key-reports/reports?api_key_id=b", "", cookie); got.Code != 400 {
			t.Fatal("cookie report accepted another key selector")
		}
		if got := reportSessionRequest(handler, "GET", "/api/key-reports/reports", "synthetic-a", nil); got.Code != 401 {
			t.Fatal("cookie endpoint accepted bearer credentials")
		}
		for _, wrong := range []*http.Cookie{
			{Name: keyReportCookie, Value: admin},
			{Name: keyReportCookie, Value: "altered-" + cookie.Value},
		} {
			if got := reportSessionRequest(handler, "GET", "/api/key-reports/reports", "", wrong); got.Code != 401 {
				t.Fatal("wrong-purpose/altered cookie accepted")
			}
		}
		if got := reportSessionRequest(handler, "GET", "/api/accounts", "", &http.Cookie{Name: sessionCookie, Value: cookie.Value}); got.Code != 401 {
			t.Fatal("report grant accepted as admin cookie")
		}
		logout := reportSessionRequest(handler, "DELETE", "/api/key-reports/session"+suffix, "", cookie)
		cleared := logout.Result().Cookies()
		if logout.Code != 200 || len(cleared) != 1 || cleared[0].Name != keyReportCookie || cleared[0].Value != "" || cleared[0].MaxAge != -1 || cleared[0].Path != cookie.Path {
			t.Fatal("logout did not clear exactly the report cookie")
		}
		if got := reportSessionRequest(handler, "GET", "/api/key-reports/session", "", nil); got.Body.String() != "{\"authenticated\":false}\n" {
			t.Fatal("logged-out browser restored a session")
		}
	}
	login := reportSessionRequest(handler, "POST", "/api/key-reports/session", "synthetic-a", nil)
	cookie := login.Result().Cookies()[0]
	key, err := store.GetAPIKey(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []func(*domain.APIKey){
		func(k *domain.APIKey) { k.IsActive = false },
		func(k *domain.APIKey) { expiry := time.Now().Add(-time.Hour); k.ExpiresAt = &expiry },
		func(k *domain.APIKey) { k.KeyHash = fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-rotated-key"))) },
	} {
		next := key
		update(&next)
		if err := store.SaveAPIKey(ctx, next, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := reportSessionRequest(handler, "GET", "/api/key-reports/reports", "", cookie); got.Code != 401 {
			t.Fatal("invalidated key retained report access", got.Code)
		}
		if got := reportSessionRequest(handler, "GET", "/api/key-reports/session", "", cookie); got.Code != 200 || strings.Contains(got.Body.String(), "true") {
			t.Fatal("invalidated key retained session status")
		}
	}
	if err := store.DeleteAPIKey(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got := reportSessionRequest(handler, "GET", "/api/key-reports/reports", "", cookie); got.Code != 401 {
		t.Fatal("deleted key retained session access")
	}
}

func TestKeyReportSessionHTTPSAndCSRF(t *testing.T) {
	server, store, _ := reportSessionServer(t)
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.DashboardSessionTTL = 365 * 24 * 60 * 60
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"tls", "trusted", "spoofed"} {
		t.Run(mode, func(t *testing.T) {
			server.config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			r := httptest.NewRequest("POST", "http://localhost/api/key-reports/session", nil)
			r.Header.Set("Authorization", "Bearer synthetic-b")
			r.RemoteAddr = "198.51.100.1:1234"
			if mode == "tls" {
				r.TLS = &tls.ConnectionState{}
			} else {
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-For", "198.51.100.1")
				if mode == "trusted" {
					r.RemoteAddr = "127.0.0.1:1234"
				}
			}
			w := httptest.NewRecorder()
			server.Handler(nil, nil).ServeHTTP(w, r)
			if w.Code != 200 || len(w.Result().Cookies()) != 1 {
				t.Fatal("HTTPS login failed", w.Code)
			}
			cookie := w.Result().Cookies()[0]
			if cookie.Secure != (mode != "spoofed") || cookie.MaxAge > 12*60*60 || cookie.MaxAge < 12*60*60-10 {
				t.Fatal("incorrect Secure or remote lifetime policy")
			}
		})
	}
	for _, method := range []string{"POST", "DELETE"} {
		r := httptest.NewRequest(method, "http://localhost/api/key-reports/session", nil)
		r.Header.Set("Authorization", "Bearer synthetic-b")
		r.Header.Set("Origin", "https://untrusted.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		server.Handler(nil, nil).ServeHTTP(w, r)
		if w.Code != 403 || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("cross-origin session mutation accepted", method)
		}
	}
}

type failingReportSessionStore struct{ Repository }

func (failingReportSessionStore) GetAPIKey(context.Context, string) (domain.APIKey, error) {
	return domain.APIKey{}, errors.New("temporary database failure")
}

func TestKeyReportSessionStorageFailureIsNotLogout(t *testing.T) {
	server, store, handler := reportSessionServer(t)
	login := reportSessionRequest(handler, "POST", "/api/key-reports/session", "synthetic-a", nil)
	cookie := login.Result().Cookies()[0]
	broken := New(failingReportSessionStore{store}, server.cipher, Config{}, nil).Handler(nil, nil)
	for _, path := range []string{"/api/key-reports/session", "/api/key-reports/reports"} {
		got := reportSessionRequest(broken, "GET", path, "", cookie)
		if got.Code != 500 || got.Header().Get("Set-Cookie") != "" {
			t.Fatal("storage failure cleared session or misclassified authentication", got.Code)
		}
	}
	state := reportSessionRequest(handler, "GET", "/api/key-reports/session", "", cookie)
	var decoded keyReportSessionState
	if state.Code != 200 || json.Unmarshal(state.Body.Bytes(), &decoded) != nil || !decoded.Authenticated {
		t.Fatal("session did not recover after transient failure")
	}
}
