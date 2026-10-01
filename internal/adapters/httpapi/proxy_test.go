package httpapi_test

import (
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
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

type wireProvider func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error)

func (p wireProvider) Respond(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	return p(ctx, target, body, emit)
}

func wireFixture(t *testing.T, provider wireProvider) (*httptest.Server, *sqlite.Store) {
	server, store, _ := wireFixtureWithProxy(t, provider)
	return server, store
}

func wireFixtureWithProxy(t *testing.T, provider wireProvider) (*httptest.Server, *sqlite.Store, *application.Proxy) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "wire.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SaveAccount(ctx, domain.Account{ID: "wire-account", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", Email: "synthetic@example.invalid", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Encrypt([]byte("synthetic-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "wire-account", AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "wire-key", Name: "test", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-key"))), KeyPrefix: "synthetic", IsActive: true, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	proxy := application.NewProxy(store, provider, vault, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1, QueueTimeout: time.Second})
	server := httptest.NewServer(httpapi.NewProxyHandler(store, proxy, nil))
	t.Cleanup(server.Close)
	return server, store, proxy
}

func wireComplete(id string, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	body := json.RawMessage(fmt.Sprintf(`{"id":%q,"object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":10}}`, id))
	if emit != nil {
		terminal, _ := json.Marshal(struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}{"response.completed", body})
		if err := emit(application.ResponseEvent{Type: "response.completed", Data: terminal}); err != nil {
			return application.ResponseResult{}, err
		}
	}
	return application.ResponseResult{ResponseID: id, Response: body, Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 10}, UsageKnown: true}, nil
}

func TestResponsesHTTPBurstAndAliases(t *testing.T) {
	var calls atomic.Int64
	provider := wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		id := fmt.Sprintf("wire_%d", calls.Add(1))
		if emit != nil {
			for i := range 512 {
				event := application.ResponseEvent{Type: "response.output_text.delta", Data: json.RawMessage(fmt.Sprintf(`{"type":"response.output_text.delta","delta":"chunk %d"}`, i))}
				if err := emit(event); err != nil {
					return application.ResponseResult{}, err
				}
			}
		}
		return wireComplete(id, emit)
	})
	server, _ := wireFixture(t, provider)
	client := server.Client()
	client.Timeout = 5 * time.Second
	for _, path := range []string{"/v1/responses", "/v1/responses/", "/backend-api/codex/responses", "/backend-api/codex/responses/"} {
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(`{"model":"gpt-6-sol","input":"hi","stream":true}`))
		req.Header.Set("Authorization", "Bearer synthetic-key")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || strings.Count(string(body), "event: response.output_text.delta") != 512 || !strings.Contains(string(body), "event: response.completed") {
			t.Fatalf("burst/alias failed status=%d error=%v", res.StatusCode, err)
		}
	}
	req, _ := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader("not-gzip"))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	req.Header.Set("Content-Encoding", "gzip")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 || calls.Load() != 4 {
		t.Fatal("invalid compressed body dispatched upstream")
	}
}

func TestResponsesWebSocketRevocation(t *testing.T) {
	var calls atomic.Int64
	server, store := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete(fmt.Sprintf("ws_%d", calls.Add(1)), emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	create := []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hello"}`)
	if err := conn.Write(ctx, websocket.MessageText, create); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("first WebSocket turn failed: %v", err)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.KeyHash = fmt.Sprintf("%x", sha256.Sum256([]byte("rotated-synthetic-key")))
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, create); err != nil {
		t.Fatal(err)
	}
	_, body, err = conn.Read(ctx)
	if err != nil || !strings.Contains(string(body), "invalid_api_key") || calls.Load() != 1 {
		t.Fatalf("key rotation did not revoke WebSocket admission: %v", err)
	}
}
