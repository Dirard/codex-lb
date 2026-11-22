package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// A Chat Completions request for an Antigravity model is translated to
// Responses and served by the proxy; the source's stored refresh token is
// never sent as a bearer to a pass-through Chat endpoint.
func TestNativeChatForAnAntigravityModelIsTranslated(t *testing.T) {
	var direct atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		direct.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	responses := &nativeTestProvider{}
	store, _, handler := newNativeTestServer(t, upstream, responses, 0)
	now := time.Now().UTC()
	zero := 0.0
	source := domain.ModelSource{
		ID: "src_native_antigravity", Name: "agent@example.test", Kind: domain.ModelSourceAntigravity, BaseURL: domain.AntigravityBaseURL,
		Enabled: true, Chat: true, Responses: true, CreatedAt: now, UpdatedAt: now,
		ProviderConfig: domain.ModelSourceProviderConfig{Antigravity: &domain.AntigravityAccountInfo{Email: "agent@example.test", Project: "aicode-consumers"}},
		Models: []domain.ModelSourceModel{{Model: "gpt-5.5", Streaming: true, Tools: true, Enabled: true,
			InputPerMillion: &zero, CachedPerMillion: &zero, OutputPerMillion: &zero, CreatedAt: now, UpdatedAt: now}},
	}
	if err := store.SaveModelSource(context.Background(), source, &domain.AccountCredential{AccountID: source.ID}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"object":"chat.completion"`) {
		t.Fatalf("chat for an Antigravity model: %d %s", w.Code, w.Body.String())
	}
	if direct.Load() != 0 || len(responses.requests) != 1 {
		t.Fatalf("direct calls %d, translated calls %d", direct.Load(), len(responses.requests))
	}
}
