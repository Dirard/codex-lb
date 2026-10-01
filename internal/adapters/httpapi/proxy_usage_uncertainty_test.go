package httpapi_test

import (
	"context"
	"encoding/json"
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
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
	"github.com/coder/websocket"
)

func TestResponsesRouteRetainsUnknownUsageFromLoopbackUpstream(t *testing.T) {
	tests := []struct {
		name, event, errorBody     string
		upstreamStatus, wantStatus int
		settled, failed, tokens    int64
		pending                    int
	}{
		{name: "explicit zero", event: `{"type":"response.completed","response":{"id":"resp_zero","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`, upstreamStatus: 200, wantStatus: 200, settled: 1},
		{name: "derived total", event: `{"type":"response.completed","response":{"id":"resp_derived","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, upstreamStatus: 200, wantStatus: 200, settled: 1, tokens: 3},
		{name: "inconsistent total", event: `{"type":"response.completed","response":{"id":"resp_bad_total","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":8}}}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "invalid cached count", event: `{"type":"response.completed","response":{"id":"resp_bad_cached","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":3}}}}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "missing usage", event: `{"type":"response.completed","response":{"id":"resp_missing","status":"completed","output":[]}}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "partial usage", event: `{"type":"response.completed","response":{"id":"resp_partial","status":"completed","output":[],"usage":{"input_tokens":2,"total_tokens":2}}}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "truncated stream", event: `{"type":"response.output_text.delta","delta":"partial"}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "overflow usage", event: `{"type":"response.completed","response":{"id":"resp_overflow","status":"completed","output":[],"usage":{"input_tokens":9223372036854775808,"output_tokens":0,"total_tokens":9223372036854775808}}}`, upstreamStatus: 200, wantStatus: 502, pending: 1},
		{name: "charged failure", event: `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","output":[],"error":{"code":"synthetic_failure"},"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`, upstreamStatus: 200, wantStatus: 200, settled: 1, failed: 1, tokens: 3},
		{name: "generic 429", errorBody: `{"error":{"code":"rate_limit_exceeded"}}`, upstreamStatus: 429, wantStatus: 429, pending: 1},
		{name: "actual quota", errorBody: `{"error":{"code":"usage_limit_reached"}}`, upstreamStatus: 429, wantStatus: 503, settled: 1, failed: 1},
		{name: "quota partial usage", errorBody: `{"error":{"code":"usage_limit_reached"},"usage":{"input_tokens":2}}`, upstreamStatus: 429, wantStatus: 429, pending: 1},
		{name: "quota invalid usage", errorBody: `{"error":{"code":"usage_limit_reached"},"usage":"invalid"}`, upstreamStatus: 429, wantStatus: 429, pending: 1},
		{name: "quota overflow usage", errorBody: `{"error":{"code":"usage_limit_reached"},"usage":{"input_tokens":9223372036854775808,"output_tokens":0}}`, upstreamStatus: 429, wantStatus: 502, pending: 1},
		{name: "timeout", errorBody: `{"error":{"code":"timeout"}}`, upstreamStatus: 408, wantStatus: 408, pending: 1},
		{name: "upstream unavailable", errorBody: `{"error":{"code":"synthetic_failure"}}`, upstreamStatus: 503, wantStatus: 503, pending: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if test.upstreamStatus != 200 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.upstreamStatus)
					_, _ = io.WriteString(w, test.errorBody)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+test.event+"\n\n")
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			ctx := context.Background()
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.HTTPTransportPolicy = "always_http"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.Limits[0].MaxValue = 2050
			if err := store.SaveAPIKey(ctx, key, key.CreatedAt); err != nil {
				t.Fatal(err)
			}
			respond := func() (int, string) {
				req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello"}`))
				req.Header.Set("Authorization", "Bearer synthetic-key")
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				body, _ := io.ReadAll(res.Body)
				return res.StatusCode, string(body)
			}
			status, body := respond()
			if status != test.wantStatus || calls.Load() != 1 {
				t.Fatalf("response=%d %s upstream calls=%d", status, body, calls.Load())
			}
			totals, err := store.UsageTotals(ctx, "wire-key", "")
			pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || pendingErr != nil || totals.RequestCount != test.settled || totals.FailedCount != test.failed ||
				totals.Usage.InputTokens+totals.Usage.OutputTokens != test.tokens || len(pending) != test.pending {
				t.Fatalf("accounting mismatch: totals=%+v pending=%+v errors=%v/%v", totals, pending, err, pendingErr)
			}
			if test.name == "missing usage" {
				status, _ = respond()
				if status != 429 || calls.Load() != 1 {
					t.Fatalf("held limit bypassed: status=%d upstream calls=%d", status, calls.Load())
				}
			}
		})
	}
}

func TestResponsesSSERouteOmitsUnaccountedTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.output_text.delta","delta":"visible"}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_missing","status":"completed","output":[]}}`+"\n\n")
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HTTPTransportPolicy = "always_http"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), "visible") || !strings.Contains(string(body), "missing_usage") ||
		strings.Contains(string(body), "event: response.completed") {
		t.Fatalf("uncertain SSE exposed success: %d %s", res.StatusCode, body)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("SSE usage was not retained: %+v %v", pending, err)
	}
}

func TestResponsesWebSocketRouteRetainsMissingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		if _, _, err := connection.Read(r.Context()); err != nil {
			return
		}
		_ = connection.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_ws_missing","status":"completed","output":[]}}`))
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HTTPTransportPolicy = "always_websocket"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	_, frame, err := connection.Read(ctx)
	if err != nil || !strings.Contains(string(frame), "missing_usage") || strings.Contains(string(frame), "response.completed") {
		t.Fatalf("uncertain WebSocket terminal: %s %v", frame, err)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("WebSocket usage was not retained: %+v %v", pending, err)
	}
}

func TestTranslatedChatMissingUsageOptInRetainsReservation(t *testing.T) {
	for _, metered := range []bool{true, false} {
		t.Run(map[bool]string{true: "metered", false: "unmetered"}[metered], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected source path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer"}}]}`)
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			vault, err := credentials.Open(filepath.Join(t.TempDir(), "source.key"), true)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := vault.Encrypt([]byte("synthetic-source-key"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			source := domain.ModelSource{
				ID: "uncertain-source", Name: "Offline source", Kind: domain.ModelSourceOpenAICompatible,
				BaseURL: upstream.URL + "/v1", Enabled: true, Chat: true,
				ProviderConfig: domain.ModelSourceProviderConfig{AllowMissingUsage: true},
				Models:         []domain.ModelSourceModel{{Model: "source-test-model", Enabled: true}},
			}
			if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: secret}); err != nil {
				t.Fatal(err)
			}
			subscription, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			subscription.Status = domain.AccountPaused
			if err := store.SaveAccount(ctx, subscription); err != nil {
				t.Fatal(err)
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.SourceAssignmentScopeEnabled = true
			key.AssignedSourceIDs = []string{source.ID}
			if !metered {
				key.Limits = []domain.LimitRule{}
			}
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			proxy.ResolvePrice = func(_ context.Context, _ domain.Account, _ string) (pricing.Price, error) {
				return pricing.Price{Standard: pricing.Rates{Input: 1_000_000, Cached: 1_000_000, Output: 1_000_000}}, nil
			}
			adapter = provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"source-test-model","input":"hello"}`))
			req.Header.Set("Authorization", "Bearer synthetic-key")
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			wantStatus := 502
			if !metered {
				wantStatus = 200
			}
			if res.StatusCode != wantStatus {
				t.Fatalf("source response=%d %s", res.StatusCode, body)
			}
			if !metered {
				var payload struct {
					ID    string          `json:"id"`
					Usage json.RawMessage `json:"usage"`
				}
				if json.Unmarshal(body, &payload) != nil || payload.ID == "" || string(payload.Usage) != "null" {
					t.Fatalf("unknown source usage was presented as zero: %s", body)
				}
				if _, err := store.GetContinuation(ctx, "wire-key", payload.ID, time.Now()); err == nil {
					t.Fatal("unknown source result created finalized continuation ownership")
				}
			}
			totals, err := store.UsageTotals(ctx, "wire-key", "uncertain-source")
			pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || pendingErr != nil || totals.RequestCount != 0 || len(pending) != 1 {
				t.Fatalf("source missing usage was finalized: totals=%+v pending=%+v errors=%v/%v", totals, pending, err, pendingErr)
			}
		})
	}
}
