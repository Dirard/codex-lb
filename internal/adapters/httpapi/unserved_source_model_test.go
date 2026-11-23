package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
	"github.com/coder/websocket"
)

// A key whose only available account is a model source must hear that the
// requested model is not available there, not that it has no price: the
// source serves other models, and every model it serves has a price.
func TestUnservedModelOnASourceIsNotAvailableRatherThanUnpriced(t *testing.T) {
	ctx := context.Background()
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		t.Error("an unserved model reached a provider")
		return application.ResponseResult{}, nil
	}))
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "source.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	sources := application.NewModelSourceService(store, vault)
	proxy.ResolvePrice = func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		if account.Kind == domain.AccountExternal {
			return sources.Price(ctx, account, model)
		}
		_, price, err := pricing.LookupCodex(model)
		return price, err
	}
	secret, err := vault.Encrypt([]byte("synthetic-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	price := 0.1
	source := domain.ModelSource{ID: "src_glm", Name: "Z.AI", Kind: domain.ModelSourceZAI, BaseURL: "https://api.example.test/v4", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "glm-5.3", Enabled: true, InputPerMillion: &price, CachedPerMillion: &price, OutputPerMillion: &price}}}
	if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountPaused // the subscription account is unavailable
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-luna","input":"hello","stream":true}`))
	request.Header.Set("Authorization", "Bearer synthetic-key")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"code":"model_not_available"`) || strings.Contains(string(body), "model_not_priced") {
		t.Fatalf("HTTP answer: %d %s", response.StatusCode, body)
	}

	wsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := connection.Write(wsCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-luna","input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	_, frame, err := connection.Read(wsCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frame), `"code":"model_not_available"`) || !strings.Contains(string(frame), `"status":404`) || strings.Contains(string(frame), "model_not_priced") {
		t.Fatalf("WebSocket frame: %s", frame)
	}
}
