package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestRequiredSubscriptionNeverDowngradesHandshakeToHTTP(t *testing.T) {
	var handshakes, posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handshakes.Add(1)
		} else {
			posts.Add(1)
		}
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer server.Close()
	adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
	defer adapter.Close()
	_, err := adapter.Respond(context.Background(), application.ResponseTarget{
		KeyID: "key", Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT, SecurityWorkAuthorized: true},
		UseWebSocket: true, AllowHTTPFallback: true, RequiredCapability: true,
	}, json.RawMessage(`{"model":"gpt-5.4","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil })
	if err == nil || handshakes.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("REQUIRED transport downgraded: err=%v handshake=%d POST=%d", err, handshakes.Load(), posts.Load())
	}
}
