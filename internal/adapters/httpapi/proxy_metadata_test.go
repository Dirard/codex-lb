package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRequestMetadataUsesResolvedPeerAndLegacyConversationHeaders(t *testing.T) {
	r := httptest.NewRequest("POST", "/backend-api/codex/responses", nil)
	r.RemoteAddr = "198.51.100.2:1000"
	r.Header.Set("User-Agent", "codex_cli_rs/1.0 (synthetic)")
	r.Header.Set("Thread-Id", "thread-1")
	r.Header.Set("Session_id", "session-1")
	r.Header.Set("X-Forwarded-For", "192.0.2.3")
	proxy := &ProxyHandler{}
	got := proxy.responseOptions(r, "k")
	if got.UserAgentGroup != "codex_cli_rs" || got.ConversationID != "thread-1" || got.SessionID != "session-1" || got.ClientIP != "198.51.100.2" {
		t.Fatalf("incorrect untrusted metadata: %+v", got)
	}
	proxy.trusted = []netip.Prefix{netip.MustParsePrefix("198.51.100.2/32")}
	got = proxy.responseOptions(r, "k")
	if got.ClientIP != "192.0.2.3" {
		t.Fatal("trusted proxy identity not resolved")
	}
}
