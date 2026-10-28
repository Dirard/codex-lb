package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

// NET-23: operations resolve the client like Responses requests: forwarded
// addresses only from trusted proxies and the User-Agent product token as group.
func TestOperationOptionsResolveTrustedClientIdentity(t *testing.T) {
	handler := NewCodexOperationsHandler(nil, nil, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	for _, test := range []struct {
		peer, want string
	}{
		{"10.0.0.1:1234", "203.0.113.24"},
		{"192.0.2.1:1234", "192.0.2.1"},
	} {
		r := httptest.NewRequest("POST", "http://localhost/backend-api/codex/responses/compact", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("X-Forwarded-For", "203.0.113.24")
		r.Header.Set("X-Real-Ip", "203.0.113.24")
		r.Header.Set("User-Agent", "codex_cli_rs/0.150.0 (Linux; x86_64)")
		r.Header.Set("X-Codex-User-Agent-Group", "spoofed")
		options := handler.operationOptions(r, "key")
		if options.ClientIP != test.want || options.UserAgentGroup != "codex_cli_rs" {
			t.Fatalf("peer %s: client=%q group=%q", test.peer, options.ClientIP, options.UserAgentGroup)
		}
	}
}
