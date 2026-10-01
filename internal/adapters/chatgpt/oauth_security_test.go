package chatgpt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOAuthErrorDoesNotExposeUpstreamEcho(t *testing.T) {
	for _, raw := range []string{`{"error":"invalid_grant","error_description":"refresh_token=synthetic-do-not-expose"}`, `{"error":{"code":"synthetic-do-not-expose","message":"Authorization: Bearer synthetic-do-not-expose"}}`} {
		err := errorFromStatus(400, []byte(raw))
		if strings.Contains(err.Code+err.Message, "synthetic-do-not-expose") {
			t.Fatal("OAuth provider echo leaked")
		}
	}
}

func TestOAuthRedirectNeverForwardsGrant(t *testing.T) {
	var forwarded atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
	defer sink.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	client := NewOAuthClient(OAuthConfig{BaseURL: provider.URL, HTTPClient: provider.Client()})
	if _, err := client.Refresh(context.Background(), "synthetic-private-refresh"); err == nil {
		t.Fatal("redirect treated as successful OAuth")
	}
	if forwarded.Load() != 0 {
		t.Fatal("OAuth grant followed provider redirect")
	}
}
