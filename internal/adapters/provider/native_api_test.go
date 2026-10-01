package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestNativeEmbeddingsPreservesExplicitNullAndAbsentFields(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer external-key" {
			t.Fatalf("invalid embeddings request: %s %+v", r.URL.Path, r.Header)
		}
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2,3.1000000000000001]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer server.Close()
	store := &sourceStore{}
	now := time.Now().UTC()
	store.source = domain.ModelSource{
		ID: "src-embed", Kind: domain.ModelSourceOpenAICompatible, BaseURL: server.URL + "/v1",
		Enabled: true, Embeddings: true, Models: []domain.ModelSourceModel{{
			Model: "public-embed", UpstreamModel: "upstream-embed", Enabled: true, CreatedAt: now, UpdatedAt: now,
		}}, CreatedAt: now, UpdatedAt: now,
	}
	store.credential = domain.AccountCredential{AccountID: "src-embed", ExternalKeyEncrypted: []byte("enc:external-key")}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	request := []byte(`{"model":"public-embed","input":"hello","dimensions":null,"user":null,"encoding_format":"float"}`)
	result, err := adapter.Embeddings(context.Background(), application.ResponseTarget{
		Account: domain.Account{ID: "src-embed", Kind: domain.AccountExternal}, KeyID: "key",
	}, request)
	if err != nil {
		t.Fatal(err)
	}
	var upstream map[string]json.RawMessage
	if json.Unmarshal(received, &upstream) != nil || string(upstream["model"]) != `"upstream-embed"` ||
		string(upstream["dimensions"]) != "null" || string(upstream["user"]) != "null" {
		t.Fatalf("embedding field presence changed: %s", received)
	}
	if _, ok := upstream["absent"]; ok {
		t.Fatal("absent field was synthesized")
	}
	if result.Status != 200 || !result.UsageKnown || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 0 {
		t.Fatalf("embedding result/usage mismatch: %+v", result)
	}
}

func TestNativeEmbeddingsMissingUsageFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()
	store := &sourceStore{}
	now := time.Now().UTC()
	store.source = domain.ModelSource{ID: "src-embed", Kind: domain.ModelSourceOpenAICompatible, BaseURL: server.URL + "/v1", Enabled: true, Embeddings: true, Models: []domain.ModelSourceModel{{Model: "embed", Enabled: true, CreatedAt: now, UpdatedAt: now}}}
	store.credential = domain.AccountCredential{AccountID: "src-embed", ExternalKeyEncrypted: []byte("enc:external-key")}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	result, err := adapter.Embeddings(context.Background(), application.ResponseTarget{Account: domain.Account{ID: "src-embed", Kind: domain.AccountExternal}}, []byte(`{"model":"embed","input":"x"}`))
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "usage_unavailable" || result.UsageKnown || result.Status != 200 {
		t.Fatalf("missing usage did not fail closed: %#v", err)
	}
}

func TestNativeChatDirectStreamAndCredentialRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer external-key" {
			t.Fatalf("invalid direct Chat request: %s %+v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chat_stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"precise_number":3.1000000000000001}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chat_stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3},"message":"token external-key revealed"}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	store := &sourceStore{}
	now := time.Now().UTC()
	store.source = domain.ModelSource{ID: "src-chat", Kind: domain.ModelSourceOpenAICompatible, BaseURL: server.URL + "/v1", Enabled: true, Chat: true, Models: []domain.ModelSourceModel{{Model: "chat-model", Streaming: true, Enabled: true, CreatedAt: now, UpdatedAt: now}}}
	store.credential = domain.AccountCredential{AccountID: "src-chat", ExternalKeyEncrypted: []byte("enc:external-key")}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	events := make([]application.NativeAPIEvent, 0, 3)
	result, err := adapter.NativeChat(context.Background(), application.ResponseTarget{Account: domain.Account{ID: "src-chat", Kind: domain.AccountExternal}}, []byte(`{"model":"chat-model","messages":[{"role":"user","content":"x"}],"stream":true}`), true, func(event application.NativeAPIEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Status != 200 || !result.UsageKnown || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 {
		t.Fatalf("direct Chat stream failed: %+v %v", result, err)
	}
	if len(events) != 3 || events[2].Type != "chat.done" || !bytes.Contains(events[2].Data, []byte("[DONE]")) {
		t.Fatalf("direct Chat terminal event missing: %+v", events)
	}
	combined := strings.Join([]string{string(events[0].Data), string(events[1].Data)}, "\n")
	if strings.Contains(combined, "external-key") || !strings.Contains(combined, "3.1000000000000001") {
		t.Fatalf("credential redaction or numeric precision failed: %s", combined)
	}
}

func TestNativeChatChecksSourceCapabilitiesBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_ok","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer server.Close()
	store := &sourceStore{source: domain.ModelSource{
		ID: "src-chat", Kind: domain.ModelSourceOpenAICompatible, BaseURL: server.URL + "/v1", Enabled: true, Chat: true,
	}, credential: domain.AccountCredential{AccountID: "src-chat", ExternalKeyEncrypted: []byte("enc:external-key")}}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	const simple = `{"model":"chat-model","messages":[{"role":"user","content":"hello"}]}`
	tests := []struct {
		name, body, code string
		stream           bool
		model            domain.ModelSourceModel
	}{
		{name: "streaming", body: simple, stream: true, code: "streaming_unsupported"},
		{name: "tools", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, code: "unsupported_capability"},
		{name: "legacy functions", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"functions":[{"name":"lookup"}]}`, code: "unsupported_capability"},
		{name: "tool choice", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"tool_choice":"required"}`, code: "unsupported_capability"},
		{name: "parallel tools", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":true}`, code: "unsupported_capability"},
		{name: "tool history", body: `{"model":"chat-model","messages":[{"role":"assistant","tool_calls":[{"id":"call-1"}]}]}`, code: "unsupported_capability"},
		{name: "image", body: `{"model":"chat-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`, code: "unsupported_capability"},
		{name: "input image", body: `{"model":"chat-model","messages":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`, code: "unsupported_capability"},
		{name: "input-form image", body: `{"model":"chat-model","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`, code: "unsupported_capability"},
		{name: "reasoning effort", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, code: "unsupported_capability"},
		{name: "reasoning toggle", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"include_reasoning":true}`, code: "unsupported_capability"},
		{name: "thinking toggle", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"enable_thinking":true}`, code: "unsupported_capability"},
		{name: "reasoning level", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, model: domain.ModelSourceModel{RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["low"]}`}, code: "reasoning_effort_unsupported"},
		{name: "nested reasoning level", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"reasoning":{"effort":"high"}}`, model: domain.ModelSourceModel{RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["low"]}`}, code: "reasoning_effort_unsupported"},
		{name: "empty tools and disabled controls", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"tools":[],"tool_choice":"none","parallel_tool_calls":false,"enable_thinking":false}`, code: ""},
		{name: "disabled thinking object", body: `{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"thinking":{"type":"disabled"}}`, code: ""},
		{name: "supported features", body: `{"model":"chat-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"lookup"}}],"reasoning_effort":"high"}`, model: domain.ModelSourceModel{Tools: true, Vision: true, RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["high"]}`}, code: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls.Store(0)
			model := test.model
			model.Model, model.Enabled = "chat-model", true
			store.source.Models = []domain.ModelSourceModel{model}
			result, err := adapter.NativeChat(context.Background(), application.ResponseTarget{Account: domain.Account{ID: "src-chat", Kind: domain.AccountExternal}}, []byte(test.body), test.stream, nil)
			if test.code == "" {
				if err != nil || result.Status != 200 || calls.Load() != 1 {
					t.Fatalf("supported request failed: result=%+v error=%v calls=%d", result, err, calls.Load())
				}
				return
			}
			var failure *application.ProviderFailure
			if !errors.As(err, &failure) || failure.Code != test.code || failure.Dispatched || calls.Load() != 0 {
				t.Fatalf("unsupported request reached upstream: error=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestRedactCredentialPreservesJSONNumberLexeme(t *testing.T) {
	body := []byte(`{"message":"Bearer secret-token","number":9007199254740993,"nested":{"token":"secret-token"}}`)
	redacted := redactCredential(body, "secret-token")
	if strings.Contains(string(redacted), "secret-token") || !strings.Contains(string(redacted), "9007199254740993") {
		t.Fatalf("redaction changed credential or numeric lexeme: %s", redacted)
	}
}
