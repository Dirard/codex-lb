package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestRuntimeConnectAddressResolution(t *testing.T) {
	for _, tc := range []struct {
		host, override, want string
		answers              []netip.Addr
		dns                  bool
	}{
		{host: "127.0.0.1:2455", override: "  lb.internal:2455  ", want: "lb.internal:2455"},
		{host: "192.0.2.5:2455", want: "192.0.2.5"},
		{host: "localhost:2455", want: "<codex-lb-ip-or-dns>"},
		{host: "[::1]:2455", want: "<codex-lb-ip-or-dns>"},
		{host: "0.0.0.0", want: "<codex-lb-ip-or-dns>"},
		{host: "", want: "<codex-lb-ip-or-dns>"},
		{host: "bad&command", want: "<codex-lb-ip-or-dns>"},
		{host: "lb.internal", want: "192.0.2.9", dns: true,
			answers: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.0.2.9")}},
		{host: "lb.unknown", want: "lb.unknown", dns: true},
		{host: "lb.internal", want: "lb.internal", dns: true, answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
	} {
		t.Run(tc.host+tc.override, func(t *testing.T) {
			called := false
			lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
				called = true
				if network != "ip4" || host != tc.host {
					t.Errorf("unexpected DNS lookup %s %s", network, host)
				}
				if tc.answers == nil {
					return nil, errors.New("offline unresolved host")
				}
				return tc.answers, nil
			}
			if got := resolveConnectAddress(context.Background(), tc.host, tc.override, lookup); got != tc.want || called != tc.dns {
				t.Fatalf("address=%q dns=%v want=%q/%v", got, called, tc.want, tc.dns)
			}
		})
	}
	for _, unsafe := range []string{"host&command", "host command", "$(command)", "https://host/path"} {
		if ValidateConfig(Config{ConnectAddress: unsafe}) == nil {
			t.Fatal("unsafe address accepted in copyable command")
		}
	}
}

func TestRuntimeConnectAddressRequiresAdminAndIgnoresForwardedHost(t *testing.T) {
	server, _, _ := newAccountsTestServer(t, &httpStubOAuth{})
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-connect-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(nil, nil)
	for _, path := range []string{"/api/settings/runtime/connect-address", "/api/settings/runtime/connect-address/"} {
		for _, authorized := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodGet, "http://192.0.2.5:2455"+path, nil)
			r.Header.Set("X-Forwarded-Host", "attacker.invalid")
			if authorized {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if !authorized {
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated runtime address: %d", w.Code)
				}
				continue
			}
			var response struct {
				ConnectAddress string `json:"connectAddress"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ConnectAddress != "192.0.2.5" {
				t.Fatalf("runtime address: %d %s", w.Code, w.Body.String())
			}
		}
	}
}
