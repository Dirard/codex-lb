package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func localKeyFixture(t *testing.T) (*sqlite.Store, *Server) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "local.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.EnsureLocalProxyKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	return store, New(store, vault, Config{}, nil)
}

func TestLocalKeylessPolicyIsSocketBoundAndRechecksSetting(t *testing.T) {
	store, server := localKeyFixture(t)
	handler := server.proxyIngress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := authenticateProxyKey(r, store)
		if err != nil {
			writeProxyError(w, err)
			return
		}
		writeJSON(w, 200, struct {
			ID string `json:"id"`
		}{key.ID})
	}))
	call := func(peer, forwarded, path string, capability bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost:2455"+path, nil)
		r.RemoteAddr = peer
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		if capability {
			r.Header.Set("X-Codex-Lb-Required-Capability", "cyber")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if response := call("127.0.0.1:123", "", "/v1/responses", false); response.Code != 200 || !strings.Contains(response.Body.String(), domain.LocalProxyKeyID) {
		t.Fatal("keyless local unavailable")
	}
	if call("198.51.100.1:123", "127.0.0.1", "/v1/responses", false).Code != 401 {
		t.Fatal("forwarded localhost bypassed key auth")
	}
	if call("127.0.0.1:123", "", "/v1/responses", true).Code != 401 {
		t.Fatal("required-capability request did not require a key")
	}
	if call("127.0.0.1:123", "", "/v1/usage", false).Code != 401 {
		t.Fatal("self-service usage did not require a key")
	}
	server.config.UnauthenticatedClientCIDRs = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if call("192.0.2.9:123", "", "/v1/responses", false).Code != 200 {
		t.Fatal("explicit socket CIDR ignored")
	}
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if call("127.0.0.1:123", "", "/v1/responses", false).Code != 401 || call("192.0.2.9:123", "", "/v1/responses", false).Code != 401 {
		t.Fatal("enabling auth did not revoke keyless access")
	}
	key, err := store.GetAPIKey(context.Background(), domain.LocalProxyKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindAPIKeyByHash(context.Background(), key.KeyHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("internal principal can authenticate as a Bearer key")
	}
	keys, err := store.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 0 {
		t.Fatal("internal principal exposed in editable key list")
	}
	if err := store.SaveAPIKey(context.Background(), key, time.Now()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("internal principal editable")
	}
}

type localReplyProvider struct{}

func (localReplyProvider) Respond(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	response := json.RawMessage(`{"id":"local-response","output":[],"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`)
	if err := emit(application.ResponseEvent{Type: "response.completed", Data: json.RawMessage(`{"type":"response.completed","response":` + string(response) + `}`)}); err != nil {
		return application.ResponseResult{}, err
	}
	return application.ResponseResult{ResponseID: "local-response", Response: response, Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
}

func TestKeylessWebSocketRevokedWhenAuthenticationEnabled(t *testing.T) {
	store, api := localKeyFixture(t)
	ctx := context.Background()
	account := domain.Account{ID: "local-account", Kind: domain.AccountChatGPT, Provider: "openai", Email: "synthetic@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now()}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	secret, err := api.cipher.Encrypt([]byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
	proxy := application.NewProxy(store, localReplyProvider{}, api.cipher, application.ProxyConfig{})
	server := httptest.NewServer(api.Handler(NewProxyHandler(store, proxy, nil), nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	create := []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hello"}`)
	if err := connection.Write(ctx, websocket.MessageText, create); err != nil {
		t.Fatal(err)
	}
	_, response, err := connection.Read(ctx)
	if err != nil || !strings.Contains(string(response), "response.completed") {
		t.Fatal("keyless response failed")
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(ctx, websocket.MessageText, create); err != nil {
		t.Fatal(err)
	}
	_, response, err = connection.Read(ctx)
	if err != nil || !strings.Contains(string(response), "invalid_api_key") {
		t.Fatal("open WebSocket kept keyless access after auth was enabled")
	}
	totals, err := store.UsageTotals(ctx, domain.LocalProxyKeyID, "")
	if err != nil || totals.RequestCount != 1 {
		t.Fatal("keyless response bypassed accounting")
	}
}
