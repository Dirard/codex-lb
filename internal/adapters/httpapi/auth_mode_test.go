package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
)

func TestTrustedHeaderRequiresSocketProvenanceAndKeepsPasswordFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "admin.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DashboardAuthMode: "trusted_header", DashboardAuthHeader: "X-Auth-Request-Email", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	server := New(store, vault, cfg, nil)
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(nil, nil)
	call := func(path, peer string, values []string, password bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost:2455"+path, nil)
		r.RemoteAddr = peer
		for _, value := range values {
			r.Header.Add(cfg.DashboardAuthHeader, value)
		}
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if password {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if call("/api/accounts", "198.51.100.1:123", []string{"admin"}, false).Code != 401 {
		t.Fatal("untrusted socket impersonated admin")
	}
	for _, values := range [][]string{nil, {""}, {"admin", "attacker"}} {
		if call("/api/accounts", "127.0.0.1:123", values, false).Code != 401 {
			t.Fatal("missing/duplicate auth header accepted")
		}
	}
	if call("/api/accounts", "127.0.0.1:123", []string{"admin"}, false).Code != 200 {
		t.Fatal("trusted authentication refused")
	}
	if call("/api/accounts", "198.51.100.1:123", nil, true).Code != 200 {
		t.Fatal("password fallback unavailable")
	}
	response := call("/api/dashboard-auth/session", "127.0.0.1:123", []string{"admin"}, false)
	var state application.AuthState
	if json.Unmarshal(response.Body.Bytes(), &state) != nil || !state.Authenticated || state.AuthMode != "trusted_header" || state.PasswordSessionActive {
		t.Fatal("wrong trusted-header session shape")
	}
}

func TestDisabledDashboardModeIsExplicitAndKeepsCSRF(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "disabled.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{DashboardAuthMode: "disabled"}, nil)
	handler := server.Handler(nil, nil)
	request := func(method, path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader(`{"name":"test","accountIds":[],"limits":[]}`))
		r.RemoteAddr = "192.0.2.3:123"
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if request("GET", "/api/accounts", "").Code != 200 {
		t.Fatal("explicit disabled mode unavailable")
	}
	if request("POST", "/api/account-groups", "https://attacker.invalid").Code != 403 {
		t.Fatal("disabled auth bypassed CSRF")
	}
	if request("POST", "/api/dashboard-auth/password/setup", "").Code != 400 {
		t.Fatal("disabled mode allowed password management")
	}
}

func TestAuthModeConfigurationFailsClosed(t *testing.T) {
	for _, cfg := range []Config{{DashboardAuthMode: "typo"}, {DashboardAuthMode: "trusted_header", DashboardAuthHeader: "X-Actor"}, {DashboardAuthMode: "trusted_header", DashboardAuthHeader: "Authorization", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}} {
		if ValidateConfig(cfg) == nil {
			t.Fatal("unsafe authentication configuration accepted")
		}
	}
}

func TestBootstrapSessionMatchesPasswordSetupIdentityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, peer, forwarded string
		requiresToken         bool
	}{
		{"direct local", "127.0.0.1:123", "", false},
		{"remote", "192.0.2.1:123", "", true},
		{"remote spoofs local", "192.0.2.1:123", "127.0.0.1", true},
		{"trusted proxy forwards remote", "127.0.0.1:123", "192.0.2.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := newAccountsTestServer(t, &httpStubOAuth{})
			server.config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			handler := server.Handler(nil, nil)
			call := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader(body))
				r.RemoteAddr = tc.peer
				r.Header.Set("Content-Type", "application/json")
				if tc.forwarded != "" {
					r.Header.Set("X-Forwarded-For", tc.forwarded)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			response := call(http.MethodGet, "/api/dashboard-auth/session", "")
			var state application.AuthState
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil || !state.BootstrapRequired || state.BootstrapTokenRequired != tc.requiresToken || state.Authenticated {
				t.Fatalf("initial state: %d %s", response.Code, response.Body.String())
			}
			if call(http.MethodGet, "/api/accounts", "").Code != 401 {
				t.Fatal("bootstrap state granted administrator access")
			}
			setup := call(http.MethodPost, "/api/dashboard-auth/password/setup", `{"password":"synthetic-password"}`)
			if tc.requiresToken {
				if setup.Code != 403 {
					t.Fatalf("remote setup bypassed token: %d", setup.Code)
				}
			} else if setup.Code != 200 || json.Unmarshal(setup.Body.Bytes(), &state) != nil || !state.Authenticated || state.BootstrapRequired || state.BootstrapTokenRequired {
				t.Fatalf("local setup: %d %s", setup.Code, setup.Body.String())
			}
		})
	}
}
