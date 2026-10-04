package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func TestGroupedKeyCatalogAndRequestReachAssignedModelSource(t *testing.T) {
	ctx := context.Background()
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, r.URL.Path+" "+body.Model+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"source-response","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	var adapter *provider.Adapter
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if target.Account.ID != "zai-source" {
			t.Errorf("GLM routing escaped to account %q", target.Account.ID)
			return application.ResponseResult{}, errors.New("GLM routed to a ChatGPT account")
		}
		return adapter.Respond(ctx, target, body, emit)
	}))
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "source.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	sources := application.NewModelSourceService(store, vault)
	modelPricing := application.NewModelPricingService(store, nil)
	proxy.ResolvePrice = func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		if account.Kind == domain.AccountExternal {
			return sources.Price(ctx, account, model)
		}
		return modelPricing.ResolveCodex(ctx, model)
	}
	secret, err := vault.Encrypt([]byte("synthetic-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	inputPrice, outputPrice := 1.0, 1.0
	source := domain.ModelSource{
		ID: "zai-source", Name: "Z.AI", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: upstream.URL + "/v1", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "glm-5.3", Streaming: true, Enabled: true,
			InputPerMillion: &inputPrice, OutputPerMillion: &outputPrice}},
	}
	if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{
		AccountID: source.ID, ExternalKeyEncrypted: secret,
	}); err != nil {
		t.Fatal(err)
	}
	group := domain.AccountGroup{ID: "chatgpt-only", Name: "ChatGPT only", AccountIDs: []string{"wire-account"}}
	if err := store.SaveGroup(ctx, group, time.Now()); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.GroupID, key.AllowedModels = &group.ID, []string{"glm-5.3"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	adapter = provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client()})
	defer adapter.Close()

	catalogService := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	proxy.Catalog = catalogService
	catalog := http.NewServeMux()
	httpapi.NewModelCatalogHandler(store, catalogService).RegisterPublicRoutes(catalog)
	getModels := func() string {
		request := httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
		request.Header.Set("Authorization", "Bearer synthetic-key")
		response := httptest.NewRecorder()
		catalog.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("catalog status = %d: %s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	payload := getModels()
	var listed struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(payload), &listed); err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, model := range listed.Models {
		if model.Slug == "glm-5.3" {
			visible = model.Visibility == "list"
		}
	}
	if !visible {
		t.Fatalf("grouped-key catalog omitted visible source model: %s", payload)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses",
		strings.NewReader(`{"model":"glm-5.3","input":"hello","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("source request status = %d: %s calls=%#v", response.StatusCode, data, calls)
	}
	if len(calls) != 1 || calls[0] != "/v1/responses glm-5.3 Bearer synthetic-source-key" {
		t.Fatalf("source dispatch calls = %#v", calls)
	}

	key.SourceAssignmentScopeEnabled, key.AssignedSourceIDs = true, []string{}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if payload := getModels(); strings.Contains(payload, `"glm-5.3"`) {
		t.Fatal("denied source remained visible")
	}
	request, err = http.NewRequest(http.MethodPost, server.URL+"/v1/responses",
		strings.NewReader(`{"model":"glm-5.3","input":"hello","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-key")
	request.Header.Set("Content-Type", "application/json")
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode < 400 || len(calls) != 1 {
		t.Fatalf("denied source request status=%d calls=%#v body=%s", response.StatusCode, calls, data)
	}
}
