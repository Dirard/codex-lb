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

func TestFirewallRoutesEnforceProxyButNotDashboard(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "firewall.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	firewall, err := application.NewFirewall(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	server.ConfigureFirewall(firewall)
	token, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil)
	request := func(method, path, body, peer string, admin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader(body))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "192.0.2.7") // ignored for the untrusted peer
		if admin {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if request("GET", "/v1/models", "", "198.51.100.1:123", false).Code != 204 {
		t.Fatal("empty list blocked")
	}
	if request("POST", "/api/firewall/ips", `{"ipAddress":"192.0.2.7"}`, "127.0.0.1:123", false).Code != 401 {
		t.Fatal("unauthorized firewall mutation")
	}
	if got := request("POST", "/api/firewall/ips", `{"ipAddress":"::ffff:192.0.2.7"}`, "127.0.0.1:123", true); got.Code != 201 {
		t.Fatalf("add: %d", got.Code)
	}
	if request("POST", "/api/firewall/ips", `{"ipAddress":"192.0.2.7"}`, "127.0.0.1:123", true).Code != 409 {
		t.Fatal("duplicate IP accepted")
	}
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses", "/backend-api/files", "/backend-api/transcribe"} {
		if request("POST", path, "{}", "198.51.100.1:123", false).Code != 403 {
			t.Fatalf("firewall bypass at %s", path)
		}
		if request("POST", path, "{}", "192.0.2.7:123", false).Code != 204 {
			t.Fatalf("allowed IP blocked at %s", path)
		}
	}
	view := request("GET", "/api/firewall/ips", "", "198.51.100.1:123", true)
	var body struct {
		Mode string `json:"mode"`
	}
	if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &body) != nil || body.Mode != "allowlist_active" {
		t.Fatal("dashboard locked out")
	}
	for _, path := range []string{"/api/key-reports/reports", "/api/key-reports/reports/"} {
		if got := request("GET", path, "", "198.51.100.1:123", false); got.Code != 403 {
			t.Fatal("browser reports bypassed allowlist", path, got.Code)
		}
	}
	for _, path := range []string{"/api/key-reports/session", "/api/key-reports/session/"} {
		if got := request("POST", path, "", "198.51.100.1:123", false); got.Code != 403 {
			t.Fatal("browser report login bypassed allowlist", path, got.Code)
		}
		for _, method := range []string{"GET", "DELETE"} {
			if got := request(method, path, "", "198.51.100.1:123", false); got.Code != 200 {
				t.Fatal("allowlist prevented session status or logout", method, path, got.Code)
			}
		}
	}
	// A fresh process reconstructs the policy from durable storage.
	reloaded, err := application.NewFirewall(ctx, store)
	if err != nil || reloaded.Allowed(netip.MustParseAddr("198.51.100.1")) {
		t.Fatal("firewall lost on restart")
	}
	if request("DELETE", "/api/firewall/ips/192.0.2.7", "", "127.0.0.1:123", true).Code != 200 {
		t.Fatal("delete failed")
	}
	if request("GET", "/v1/models", "", "198.51.100.1:123", false).Code != 204 {
		t.Fatal("mutation did not invalidate policy")
	}
}

func TestFirewallTrustRequiresACompleteChain(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	r := httptest.NewRequest("GET", "http://localhost:2455/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:123"
	r.Header.Set("X-Real-IP", "192.0.2.1")
	if firewallIdentity(r, trusted).IsValid() {
		t.Fatal("singleton vendor IP trusted")
	}
	r.Header.Set("X-Forwarded-For", "192.0.2.2")
	if got := firewallIdentity(r, trusted); got.String() != "192.0.2.2" {
		t.Fatal("vendor alias altered chain")
	}
	r.Header.Set("Forwarded", "for=192.0.2.3")
	if firewallIdentity(r, trusted).IsValid() {
		t.Fatal("conflicting chains accepted")
	}
	r.Header.Set("Forwarded", "for=192.0.2.2")
	r.Header.Add("X-Forwarded-For", "unknown")
	if firewallIdentity(r, trusted).IsValid() {
		t.Fatal("malformed repeated chain accepted")
	}
}
