package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type nativeTestProvider struct {
	requests []json.RawMessage
	stream   bool
}

func (p *nativeTestProvider) Respond(_ context.Context, _ application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	response := []byte(`{"id":"resp_native","object":"response","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native hello"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`)
	if p.stream {
		events := []application.ResponseEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_native","status":"in_progress","model":"gpt-5.5"}}`)},
			{Type: "response.output_item.added", Data: []byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}`)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"stream hello"}`)},
			{Type: "response.output_item.done", Data: []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"stream hello"}]}}`)},
			{Type: "response.completed", Data: []byte(`{"type":"response.completed","response":{"id":"resp_native","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}}`)},
		}
		for _, event := range events {
			if err := emit(event); err != nil {
				return application.ResponseResult{}, err
			}
		}
	}
	return application.ResponseResult{ResponseID: "resp_native", Response: response, Usage: domain.UsageAmount{InputTokens: 3, OutputTokens: 2, CachedInputTokens: 1, ReasoningTokens: 1}, UsageKnown: true}, nil
}

type nativeAdmission struct{}

func (nativeAdmission) Acquire(context.Context) (func(), error) { return func() {}, nil }

type nativeTokenSource struct{}

func (nativeTokenSource) AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error) {
	return "unused", nil
}
func (nativeTokenSource) ForceRefresh(context.Context, domain.Account, string) (string, error) {
	return "unused", nil
}

func newNativeTestServer(t *testing.T, embeddings *httptest.Server, responseProvider application.ResponseProvider, limit int64) (*sqlite.Store, *application.CodexResourceOwners, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "native.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	secret := "synthetic-native-key"
	hash := fmt.Sprintf("%x", sha256SumTest([]byte(secret)))
	key := domain.APIKey{
		ID: "key_native", Name: "native", KeyHash: hash, KeyPrefix: "sk-native", IsActive: true,
		CreatedAt: now, AllowedModels: []string{"gpt-5.5", "public-embed"},
	}
	if limit > 0 {
		key.Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: limit, ResetAt: now.Add(time.Hour)}}
	}
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	chatAccount := domain.Account{ID: "acct_native", Kind: domain.AccountChatGPT, Provider: "openai", Email: "native@example.test", PlanType: "pro", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: now}
	if err := store.SaveAccount(ctx, chatAccount); err != nil {
		t.Fatal(err)
	}
	token, err := vault.Encrypt([]byte("chat-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: chatAccount.ID, AccessTokenEncrypted: token, RefreshTokenEncrypted: token, IDTokenEncrypted: token}); err != nil {
		t.Fatal(err)
	}
	if embeddings != nil {
		modelNow := time.Now().UTC()
		source := domain.ModelSource{
			ID: "src_native_embed", Name: "Embed Source", Kind: domain.ModelSourceOpenAICompatible,
			BaseURL: embeddings.URL + "/v1", Enabled: true, Chat: true, Embeddings: true,
			Models: []domain.ModelSourceModel{{
				Model: "public-embed", UpstreamModel: "upstream-embed", Enabled: true, Streaming: true,
				RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["low","high"]}`,
				InputPerMillion: ptrFloat(1), CreatedAt: modelNow, UpdatedAt: modelNow,
			}}, CreatedAt: modelNow, UpdatedAt: modelNow,
		}
		externalKey, _ := vault.Encrypt([]byte("external-key"))
		if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: externalKey}); err != nil {
			t.Fatal(err)
		}
	}
	proxy := application.NewProxy(store, responseProvider, vault, application.ProxyConfig{})
	var embeddingProvider application.NativeEmbeddingProvider
	if embeddings != nil {
		embeddingProvider = provider.New(store, nativeTokenSource{}, vault, provider.Config{HTTPClient: embeddings.Client()})
	}
	var chatProvider application.NativeChatProvider
	if embeddingProvider != nil {
		chatProvider = embeddingProvider.(application.NativeChatProvider)
	}
	service := application.NewNativeAPIService(store, embeddingProvider, chatProvider, proxy)
	service.ConfigureAdmission(nativeAdmission{})
	mux := http.NewServeMux()
	RegisterNativeAPIRoutes(mux, store, service, nil)
	return store, nil, mux
}

func TestNativeChatHTTPContractAndSQLiteAccounting(t *testing.T) {
	provider := &nativeTestProvider{}
	store, _, handler := newNativeTestServer(t, nil, provider, 0)
	request := func(path string, body any) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewReader(encoded))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic-native-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	chatBody := map[string]any{
		"model": "gpt-5.5", "messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function", "function": map[string]any{"name": "shell", "arguments": "{}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
		}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "shell"}}},
	}
	response := request("/v1/chat/completions", chatBody)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"chat.completion"`) ||
		!strings.Contains(response.Body.String(), `"prompt_tokens":3`) {
		t.Fatalf("native chat failed: %d %s", response.Code, response.Body.String())
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls: %d", len(provider.requests))
	}
	var upstream map[string]json.RawMessage
	if json.Unmarshal(provider.requests[0], &upstream) != nil || upstream["messages"] != nil || upstream["tools"] == nil {
		t.Fatalf("Chat request was not converted to Responses: %s", provider.requests[0])
	}
	totals, err := store.UsageTotals(context.Background(), "key_native", "acct_native")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 2 {
		t.Fatalf("native chat accounting: %+v %v", totals, err)
	}

	streamProvider := &nativeTestProvider{stream: true}
	_, _, streamHandler := newNativeTestServer(t, nil, streamProvider, 0)
	chatBody["stream"] = true
	chatBody["stream_options"] = map[string]bool{"include_usage": true}
	encoded, _ := json.Marshal(chatBody)
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions/", bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	streamHandler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"delta":{"role":"assistant"}`) ||
		!strings.Contains(w.Body.String(), `"delta":{"content":"stream hello"}`) ||
		!strings.Contains(w.Body.String(), `"prompt_tokens":4`) || !strings.Contains(w.Body.String(), `"completion_tokens":3`) ||
		!strings.Contains(w.Body.String(), `"total_tokens":7`) || !strings.Contains(w.Body.String(), `"cached_tokens":2`) ||
		!strings.Contains(w.Body.String(), `"reasoning_tokens":1`) || !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("native chat stream failed: %d %s", w.Code, w.Body.String())
	}
}

func TestNativeEmbeddingsHTTPPreservesFieldsAndAccounting(t *testing.T) {
	var received []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer external-key" {
			t.Fatalf("invalid embeddings upstream: %s %+v", r.URL.Path, r.Header)
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat_external","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"direct"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
			return
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 100)
	body := []byte(`{"model":"public-embed","input":"hello","dimensions":null,"user":null,"encoding_format":"float"}`)
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/embeddings/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"object":"list"`) {
		t.Fatalf("native embeddings failed: %d %s", w.Code, w.Body.String())
	}
	var sent map[string]json.RawMessage
	if json.Unmarshal(received, &sent) != nil || string(sent["model"]) != `"upstream-embed"` ||
		string(sent["dimensions"]) != "null" || string(sent["user"]) != "null" || sent["absent"] != nil {
		t.Fatalf("embedding field presence changed: %s", received)
	}
	key, err := store.GetAPIKey(context.Background(), "key_native")
	if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 2 {
		t.Fatalf("embedding limit was not settled to actual usage: %+v %v", key, err)
	}
	totals, err := store.UsageTotals(context.Background(), "key_native", "src_native_embed")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 2 || totals.Usage.OutputTokens != 0 || totals.Usage.CostMicrodollars != 2 {
		t.Fatalf("embedding accounting: %+v %v", totals, err)
	}
}

func TestNativeChatRoutesDirectSourceAndPreservesChatFields(t *testing.T) {
	var received []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected direct Chat path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_external","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"direct"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 100)
	body := []byte(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}],"future":null,"temperature":0.2}`)
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"direct"`) {
		t.Fatalf("direct native chat failed: %d %s", w.Code, w.Body.String())
	}
	var upstreamRequest map[string]json.RawMessage
	if json.Unmarshal(received, &upstreamRequest) != nil || string(upstreamRequest["model"]) != `"upstream-embed"` ||
		upstreamRequest["messages"] == nil || string(upstreamRequest["future"]) != "null" {
		t.Fatalf("direct Chat payload changed: %s", received)
	}
	key, err := store.GetAPIKey(context.Background(), "key_native")
	if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 4 {
		t.Fatalf("direct Chat accounting failed: %+v %v", key, err)
	}
	totals, err := store.UsageTotals(context.Background(), "key_native", "src_native_embed")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 1 || totals.Usage.CostMicrodollars != 3 {
		t.Fatalf("direct Chat totals: %+v %v", totals, err)
	}
}

func sha256SumTest(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func ptrFloat(value float64) *float64 { return &value }

func TestNativeEndpointsApplyEnforcedModelAndSourceCostLimits(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil || string(body["model"]) != `"upstream-embed"` || string(body["future"]) != "null" {
			t.Error("effective model or explicit null lost at provider boundary")
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "embeddings") {
			_, _ = w.Write([]byte(`{"object":"list","data":[],"usage":{"prompt_tokens":2,"total_tokens":2}}`))
		} else {
			if string(body["service_tier"]) != `"default"` || string(body["reasoning_effort"]) != `"low"` {
				t.Error("native Chat bypassed key reasoning policy or global fast-mode prohibition")
			}
			if string(body["reasoningEffort"]) != `"low"` || string(body["reasoning"]) != `{"effort":"low","summary":"auto"}` || string(body["thinking"]) != `{"effort":"low","type":"enabled"}` {
				t.Error("native effort alias bypassed enforced key policy")
			}
			_, _ = w.Write([]byte(`{"id":"chat_forced","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		}
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 0)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "key_native")
	if err != nil {
		t.Fatal(err)
	}
	model := "public-embed"
	key.EnforcedModel = &model
	tier, effort := "priority", "low"
	key.EnforcedServiceTier, key.EnforcedReasoningEffort = &tier, &effort
	key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1000000, ResetAt: time.Now().Add(time.Hour)}}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ProhibitFastMode = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ path, body string }{
		{"/v1/embeddings", `{"model":"not-permitted-before-override","input":"hello","future":null}`},
		{"/v1/chat/completions", `{"model":"not-permitted-before-override","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high","reasoningEffort":"high","reasoning":{"effort":"high","summary":"auto"},"thinking":{"type":"enabled","effort":"high"},"service_tier":"priority","future":null}`},
	} {
		r := httptest.NewRequest("POST", request.path, strings.NewReader(request.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic-native-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", request.path, w.Code, w.Body.String())
		}
	}
	key, err = store.GetAPIKey(ctx, key.ID)
	if err != nil || key.Limits[0].CurrentValue != 5 || calls != 2 {
		t.Fatalf("source cost limit was not settled: %+v calls=%d error=%v", key.Limits, calls, err)
	}
	key.EnforcedReasoningEffort = nil
	key.AllowedReasoningEfforts = []string{"low"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"reasoning_effort":"high"`, `"reasoningEffort":"high"`, `"reasoning":{"effort":"high"}`, `"thinking":{"effort":"high","type":"enabled"}`, `"thinking":"high"`} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}],`+field+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic-native-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 || calls != 2 {
			t.Fatalf("disallowed native reasoning dispatched for %s: status=%d calls=%d", field, w.Code, calls)
		}
	}
}

func TestNativeChatRejectsUndeclaredCapabilitiesBeforeDispatch(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 100)
	ctx := context.Background()
	source, err := store.GetModelSource(ctx, "src_native_embed")
	if err != nil {
		t.Fatal(err)
	}
	source.Models[0].Streaming = false
	source.Models[0].RawMetadataJSON = `{}`
	if err := store.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{
		`,"stream":true`,
		`,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]`,
		`,"input":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]`,
		`,"reasoning_effort":"high"`,
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions/", strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}]`+extra+`}`))
		r.Header.Set("Authorization", "Bearer synthetic-native-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || calls.Load() != 0 || !strings.Contains(w.Body.String(), "unsupported") {
			t.Fatalf("unsupported native capability dispatched: %d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
		}
	}
	key, err := store.GetAPIKey(ctx, "key_native")
	if err != nil || key.Limits[0].CurrentValue != 0 {
		t.Fatalf("undispatched validation charged the key: %+v %v", key.Limits, err)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatal("undispatched validation left uncertain reservations")
	}
}
