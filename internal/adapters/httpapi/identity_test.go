package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIdentityResolvesOnlyThroughTrustedHops(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name    string
		peer    string
		host    string
		headers http.Header
		want    string
	}{
		{"local", "127.0.0.1:5678", "localhost:2455", nil, "127.0.0.1"},
		{"DNS rebinding", "127.0.0.1:5678", "attacker.invalid", nil, "127.0.0.1"},
		{"untrusted", "192.0.2.1:1234", "localhost", http.Header{"X-Forwarded-For": {"127.0.0.1"}}, "192.0.2.1"},
		{"preseeded", "127.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=127.0.0.1", "for=203.0.113.24"}}, "203.0.113.24"},
		{"trusted chain", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=127.0.0.1, for=10.0.0.2"}}, "127.0.0.1"},
		{"consensus", "10.0.0.1:1234", "localhost", http.Header{"X-Forwarded-For": {"203.0.113.24"}, "X-Real-Ip": {"203.0.113.24"}}, "203.0.113.24"},
		{"disagreement", "10.0.0.1:1234", "localhost", http.Header{"X-Forwarded-For": {"127.0.0.1"}, "X-Real-Ip": {"203.0.113.24"}}, ""},
		{"duplicate singleton", "10.0.0.1:1234", "localhost", http.Header{"X-Real-Ip": {"", "127.0.0.1"}}, ""},
		{"IPv6 quoted", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {`for="[::1]:1234";proto=https`}}, "::1"},
		{"IPv6 unquoted", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=[::1]"}}, ""},
		{"malformed earlier hop", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=unknown, for=203.0.113.24"}}, ""},
		{"repeated for", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=127.0.0.1;for=127.0.0.1"}}, ""},
		{"invalid port", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {`for="127.0.0.1:65536"`}}, ""},
		{"trailing delimiter", "10.0.0.1:1234", "localhost", http.Header{"Forwarded": {"for=127.0.0.1,"}}, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost/api/dashboard-auth/password/setup", nil)
			r.RemoteAddr, r.Host = tt.peer, tt.host
			if tt.headers != nil {
				r.Header = tt.headers
			}
			id := ResolveIdentity(r, trusted)
			actual := ""
			if id.IP.IsValid() {
				actual = id.IP.String()
			}
			if actual != tt.want {
				t.Fatalf("identity=%+v, want IP=%s", id, tt.want)
			}
		})
	}
	r := httptest.NewRequest("POST", "http://localhost/api/dashboard-auth/password/setup", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "")
	r.Header.Add("X-Forwarded-For", "127.0.0.1")
	if id := ResolveIdentity(r, nil); !id.Forwarded || id.TrustedPeer {
		t.Fatal("later forwarded field was not observed")
	}
}
