package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestFirstUpstreamEventHookRunsForNonstreamClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.created","response":{"id":"resp_hook"}}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_hook","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer upstream.Close()
	adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokenSource{}, testCipher{}, Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	var hooks atomic.Int32
	target := application.ResponseTarget{Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}, KeyID: "key", OnFirstUpstreamEvent: func() { hooks.Add(1) }}
	result, err := adapter.Respond(context.Background(), target, []byte(`{"model":"gpt-6-sol","input":"hello"}`), nil)
	if err != nil || !result.UsageKnown || hooks.Load() != 1 {
		t.Fatalf("nonstream client did not release create lease on first upstream event: result=%+v hooks=%d error=%v", result, hooks.Load(), err)
	}
}

func TestTranslatedChatSyntheticCreatedDoesNotReleaseCreateLease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	source, credential := zaiSource(upstream.URL)
	adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: upstream.Client()})
	defer adapter.Close()
	var hooks atomic.Int32
	target := application.ResponseTarget{Account: source.Account(), KeyID: "key", OnFirstUpstreamEvent: func() { hooks.Add(1) }}
	result, err := adapter.Respond(context.Background(), target, []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`), func(event application.ResponseEvent) error {
		if event.Type == "response.created" && hooks.Load() != 0 {
			t.Error("synthetic prelude released create lease")
		}
		return nil
	})
	if err != nil || !result.UsageKnown || hooks.Load() != 1 {
		t.Fatalf("actual translated Chat event did not release create lease: result=%+v hooks=%d error=%v", result, hooks.Load(), err)
	}
}
