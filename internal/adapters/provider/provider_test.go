package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type testCipher struct{}

func (testCipher) Encrypt(value []byte) ([]byte, error) { return append([]byte("enc:"), value...), nil }
func (testCipher) Decrypt(value []byte) ([]byte, error) {
	if !strings.HasPrefix(string(value), "enc:") {
		return nil, errors.New("invalid ciphertext")
	}
	return value[len("enc:"):], nil
}

type sourceStore struct {
	source     domain.ModelSource
	credential domain.AccountCredential
}

func (s *sourceStore) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	return []domain.ModelSource{s.source}, nil
}
func (s *sourceStore) GetModelSource(_ context.Context, id string) (domain.ModelSource, error) {
	if id != s.source.ID {
		return domain.ModelSource{}, domain.ErrNotFound
	}
	return s.source, nil
}
func (s *sourceStore) SaveModelSource(_ context.Context, source domain.ModelSource, credential *domain.AccountCredential) error {
	s.source = source
	if credential != nil {
		s.credential = *credential
	}
	return nil
}
func (s *sourceStore) DeleteModelSource(_ context.Context, id string) error {
	if id != s.source.ID {
		return domain.ErrNotFound
	}
	s.source = domain.ModelSource{}
	return nil
}
func (s *sourceStore) GetAccountCredential(_ context.Context, id string) (domain.AccountCredential, error) {
	if id != s.credential.AccountID {
		return domain.AccountCredential{}, domain.ErrNotFound
	}
	return s.credential, nil
}

type tokenSource struct{}

func (tokenSource) AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error) {
	return "chatgpt-access-token", nil
}

func (tokenSource) ForceRefresh(context.Context, domain.Account, string) (string, error) {
	return "chatgpt-refreshed-token", nil
}

func zaiSource(baseURL string) (domain.ModelSource, domain.AccountCredential) {
	now := time.Now().UTC()
	source := domain.ModelSource{
		ID: "src_zai", Name: "Z.AI", Kind: domain.ModelSourceZAI, BaseURL: baseURL,
		Enabled: true, Health: "unknown", Chat: true, CreatedAt: now, UpdatedAt: now,
		Models: []domain.ModelSourceModel{{
			SourceID: "src_zai", Model: "gpt-5.6-sol", Aliases: []string{"codex-alias"},
			UpstreamModel: "glm-5.2", Streaming: true, Tools: true, Vision: true, Enabled: true,
			InputPerMillion: floatPointer(2), CachedPerMillion: floatPointer(.2), OutputPerMillion: floatPointer(10),
			RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["low","high"],"default_reasoning_level":"low"}`,
			CreatedAt:       now, UpdatedAt: now,
		}},
	}
	return source, domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: []byte("enc:zai-key")}
}

func TestProviderRejectsCredentialFromReimportedAccount(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx := context.Background()
	store := &sourceStore{source: domain.ModelSource{ID: "source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: server.URL + "/v1", Enabled: true, Responses: true, Chat: true, Embeddings: true, Audio: true,
		Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}, {Model: "whisper-x", Enabled: true}}},
		credential: domain.AccountCredential{AccountID: "source", Generation: 1, ExternalKeyEncrypted: []byte("enc:new-key")}}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
	external := domain.Account{ID: "source", Kind: domain.AccountExternal, Generation: 0}
	responseTarget := application.ResponseTarget{Account: external, KeyID: "key"}
	operationTarget := application.CodexOperationTarget{Account: external, KeyID: "key"}
	checks := []struct {
		name string
		call func() error
	}{
		{"responses", func() error {
			_, err := adapter.Respond(ctx, responseTarget, []byte(`{"model":"model","input":"hi"}`), nil)
			return err
		}},
		{"native chat", func() error {
			_, err := adapter.NativeChat(ctx, responseTarget, []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}]}`), false, nil)
			return err
		}},
		{"embeddings", func() error {
			_, err := adapter.Embeddings(ctx, responseTarget, []byte(`{"model":"model","input":"hi"}`))
			return err
		}},
		{"transcription", func() error {
			_, err := adapter.Transcribe(ctx, operationTarget, application.CodexTranscriptionRequest{Model: "whisper-x", Audio: []byte("audio")})
			return err
		}},
	}
	for _, check := range checks {
		var failure *application.ProviderFailure
		if err := check.call(); !errors.As(err, &failure) || failure.Code != "model_source_credential_unavailable" {
			t.Fatalf("%s accepted a new credential: %v", check.name, err)
		}
	}
	store.credential = domain.AccountCredential{AccountID: "chatgpt", Generation: 1, AccessTokenEncrypted: []byte("enc:new-token")}
	chatgpt := domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT, Generation: 0}
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"responses", func() error {
			_, err := adapter.Respond(ctx, application.ResponseTarget{Account: chatgpt}, []byte(`{"model":"gpt-test","input":"hi"}`), nil)
			return err
		}},
		{"control", func() error {
			_, err := adapter.Control(ctx, application.CodexOperationTarget{Account: chatgpt}, application.CodexControlRequest{Method: "GET", Path: "thread/goal/get"})
			return err
		}},
		{"realtime", func() error {
			return adapter.Realtime(ctx, application.CodexOperationTarget{Account: chatgpt}, application.CodexRealtimeRequest{CallID: "call"}, nil)
		}},
	} {
		var failure *application.ProviderFailure
		if err := check.call(); !errors.As(err, &failure) || failure.Code != "chatgpt_credential_unavailable" {
			t.Fatalf("%s accepted a new credential: %v", check.name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("stale target reached upstream %d times", calls.Load())
	}
}

func TestExternalChatCompletionsMapsModelAndPreservesUsage(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer zai-key" {
			t.Fatalf("unexpected endpoint or credential: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"service_tier":"standard","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`))
	}))
	defer server.Close()
	source, credential := zaiSource(server.URL + "/v1")
	store := &sourceStore{source: source, credential: credential}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})

	result, err := adapter.Respond(context.Background(), application.ResponseTarget{
		Account: source.Account(), KeyID: "key",
	}, mustJSON(map[string]any{"model": "codex-alias", "input": "hello", "stream": false}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if request["model"] != "glm-5.2" || !reflect.DeepEqual(request["thinking"], map[string]any{"type": "enabled"}) ||
		request["reasoning_effort"] != nil {
		t.Fatalf("Z.AI request was not mapped/customized: %#v", request)
	}
	if result.Usage.InputTokens != 10 || result.Usage.CachedInputTokens != 4 ||
		result.Usage.OutputTokens != 5 || result.Usage.ReasoningTokens != 2 || result.ServiceTier != "standard" {
		t.Fatalf("usage or service tier was lost: %+v", result)
	}
}

func TestExternalChatStreamAndReasoningCapability(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"service_tier\":\"flex\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	source, credential := zaiSource(server.URL + "/v1")
	store := &sourceStore{source: source, credential: credential}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	events := make([]application.ResponseEvent, 0, 8)
	result, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: source.Account(), KeyID: "key"}, mustJSON(map[string]any{
		"model": "gpt-5.6-sol", "input": "hello", "stream": true,
		"reasoning": map[string]string{"effort": "high"},
	}), func(event application.ResponseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request["stream_options"], map[string]any{"include_usage": true}) || request["reasoning_effort"] != "high" {
		t.Fatalf("stream request contract was not preserved: %#v", request)
	}
	if result.ServiceTier != "flex" || result.Usage.InputTokens+result.Usage.OutputTokens != 5 || len(events) == 0 ||
		events[len(events)-1].Type != "response.completed" {
		t.Fatalf("stream result or terminal event was invalid: %+v %+v", result, events)
	}
	if _, err = adapter.Respond(context.Background(), application.ResponseTarget{Account: source.Account()}, mustJSON(map[string]any{
		"model": "gpt-5.6-sol", "input": "hello", "reasoning": map[string]string{"effort": "ultra"},
	}), nil); err == nil {
		t.Fatal("unsupported reasoning effort was accepted")
	}
}

func TestZAIResponsesPreservesNativeProtocol(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses_only", true: "responses_preferred"}[chat], func(t *testing.T) {
			var request map[string]json.RawMessage
			response := `{"id":"resp_zai","status":"completed","output":[{"type":"function_call","call_id":"call_native","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":4}}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/responses" || r.Header.Get("Authorization") != "Bearer zai-key" {
					t.Error("native Responses endpoint or credential mismatch")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			source, credential := zaiSource(server.URL + "/api/v1")
			source.Chat, source.Responses = chat, true
			if err := source.Validate(); err != nil {
				t.Fatal(err)
			}
			adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
			body := json.RawMessage(`{"model":"codex-alias","input":[{"type":"function_call_output","call_id":"call_prior","output":"ok"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],"reasoning":{"effort":"high"},"max_output_tokens":16,"stream":false}`)
			result, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: source.Account(), KeyID: "key"}, body, nil)
			if err != nil {
				t.Fatal(err)
			}
			var original map[string]json.RawMessage
			_ = json.Unmarshal(body, &original)
			for _, field := range []string{"input", "tools", "reasoning", "max_output_tokens"} {
				if string(request[field]) != string(original[field]) {
					t.Errorf("native %s was transformed: %s", field, request[field])
				}
			}
			for _, field := range []string{"messages", "thinking", "reasoning_effort", "stream_options"} {
				if _, exists := request[field]; exists {
					t.Errorf("Chat-only field %s was injected", field)
				}
			}
			if string(request["model"]) != `"glm-5.2"` || result.ResponseID != "resp_zai" ||
				result.Usage.InputTokens != 10 || result.Usage.CachedInputTokens != 4 || result.Usage.OutputTokens != 5 ||
				!strings.Contains(string(result.Response), `"call_id":"call_native"`) {
				t.Fatalf("native model, tool call or usage lost: %+v", result)
			}
		})
	}
}

func TestQuotaErrorsRemainDistinctFromGeneric429(t *testing.T) {
	for _, test := range []struct {
		body  string
		quota bool
		code  string
	}{
		{`{"error":{"code":"usage_limit_reached","message":"quota"}}`, true, "insufficient_quota"},
		{`{"error":{"code":"temporarily_busy","message":"retry"}}`, false, "rate_limit_exceeded"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(test.body))
		}))
		source, credential := zaiSource(server.URL)
		adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
		_, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: source.Account(), KeyID: "key"}, mustJSON(map[string]any{
			"model": "gpt-5.6-sol", "input": "hello",
		}), nil)
		server.Close()
		var failure *application.ProviderFailure
		if !errors.As(err, &failure) || failure.QuotaRefused != test.quota || failure.Code != test.code {
			t.Fatalf("429 classification mismatch for %s: %#v", test.body, err)
		}
	}
}

func TestChatGPTBridgeHeadersCredentialAndRawResponses(t *testing.T) {
	var request map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" || r.Header.Get("Authorization") != "Bearer chatgpt-access-token" ||
			r.Header.Get("ChatGPT-Account-ID") != "chatgpt-account" || r.Header.Get("Originator") != "codex_cli_rs" ||
			r.Header.Get("Version") != "0.156.0" || r.Header.Get("User-Agent") != "codex_cli_rs/0.156.0" ||
			r.Header.Get("Cookie") != "" {
			t.Fatalf("ChatGPT bridge headers leaked or were incomplete: %+v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		if _, present := request["max_output_tokens"]; present {
			t.Error("unsupported output cap reached subscription HTTP")
		}
		if string(request["stream"]) != "true" || string(request["store"]) != "false" || len(request["input"]) == 0 || request["input"][0] != '[' {
			t.Error("subscription request was not normalized")
		}
		if string(request["reasoning"]) != `{"effort":"max","summary":"auto"}` {
			t.Error("subscription reasoning wire alias was not applied")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + `{"type":"response.completed","response":{"id":"resp_chatgpt","status":"completed","output":[],"service_tier":"priority","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":1}},"future":{"kept":true}}}` + "\n\n"))
	}))
	defer server.Close()
	credential := domain.AccountCredential{AccountID: "acct", AccessTokenEncrypted: []byte("enc:oauth")}
	store := &sourceStore{credential: credential}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL + "/codex"})
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-account"}
	body := mustJSON(map[string]any{"model": "gpt-6-sol", "input": "hello", "max_output_tokens": 16, "future_request": "kept", "reasoning": map[string]string{"effort": "ultra", "summary": "auto"}})
	result, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: account, KeyID: "key"}, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"effort":"ultra"`) || !strings.Contains(string(body), `"max_output_tokens":16`) {
		t.Fatal("wire alias mutated caller's body")
	}
	if string(request["future_request"]) != `"kept"` || result.ResponseID != "resp_chatgpt" ||
		result.ServiceTier != "priority" || result.Usage.CachedInputTokens != 1 || result.Usage.ReasoningTokens != 1 {
		t.Fatalf("ChatGPT passthrough result was altered: %s %+v", result.Response, result)
	}
}

func TestChatGPTWebSocketBridgeUsesResponseCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		if r.Header.Get("ChatGPT-Account-ID") != "chatgpt-account" || r.Header.Get("Originator") != "codex_cli_rs" ||
			r.Header.Get("Version") != "9.9.9" || r.Header.Get("User-Agent") != "codex_cli_rs/9.9.9" {
			t.Fatalf("WebSocket bridge headers invalid: %+v", r.Header)
		}
		_, create, err := connection.Read(context.Background())
		if err != nil {
			t.Errorf("read create: %v", err)
			return
		}
		var payload map[string]any
		if json.Unmarshal(create, &payload) != nil || payload["type"] != "response.create" || payload["stream"] != nil || payload["max_output_tokens"] != nil {
			t.Fatalf("invalid response.create: %s", create)
		}
		if !reflect.DeepEqual(payload["reasoning"], map[string]any{"effort": "max", "summary": "auto"}) {
			t.Error("WebSocket reasoning wire alias was not applied")
		}
		_ = connection.Write(context.Background(), websocket.MessageText, mustJSON(map[string]any{
			"type": "response.completed", "response": map[string]any{
				"id": "resp_ws", "status": "completed", "service_tier": "default",
				"usage": map[string]uint{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
			},
		}))
	}))
	defer server.Close()
	credential := domain.AccountCredential{AccountID: "acct", AccessTokenEncrypted: []byte("enc:oauth")}
	adapter := New(&sourceStore{credential: credential}, tokenSource{}, testCipher{}, Config{
		HTTPClient: server.Client(), ChatGPTBaseURL: server.URL + "/codex", CodexVersion: "9.9.9",
	})
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-account"}
	events := make([]application.ResponseEvent, 0, 2)
	result, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: account, KeyID: "key", UseWebSocket: true}, mustJSON(map[string]any{
		"model": "gpt-6-sol", "input": "hello", "stream": true, "max_output_tokens": 16,
		"reasoning": map[string]string{"effort": "ultra", "summary": "auto"},
	}), func(event application.ResponseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp_ws" || result.ServiceTier != "default" || len(events) != 1 || events[0].Type != "response.completed" {
		t.Fatalf("WebSocket bridge result invalid: %+v %+v", result, events)
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func floatPointer(value float64) *float64 { return &value }
