package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderRedirectDoesNotReplayCredentialOrPayload(t *testing.T) {
	var forwarded atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
	defer sink.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	adapter := New(provider.Client(), nil)
	defer adapter.Close()
	_, err := adapter.Execute(context.Background(), Target{ProviderID: "p", AccountID: "a", KeyID: "k", BaseURL: provider.URL, Credential: "synthetic-credential", Capabilities: ResponsesCapabilities()}, Request{Body: json.RawMessage(`{"model":"gpt-6-sol","input":"private prompt"}`)})
	if err == nil || forwarded.Load() != 0 {
		t.Fatal("provider redirect replayed request or credentials")
	}
}
