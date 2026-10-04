package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type modelCatalogProxyStore struct {
	key domain.APIKey
}

func (s modelCatalogProxyStore) SaveAPIKey(context.Context, domain.APIKey, time.Time) error {
	return nil
}
func (s modelCatalogProxyStore) GetAPIKey(_ context.Context, id string) (domain.APIKey, error) {
	if id == s.key.ID {
		return s.key, nil
	}
	return domain.APIKey{}, domain.ErrNotFound
}
func (s modelCatalogProxyStore) FindAPIKeyByHash(context.Context, string) (domain.APIKey, error) {
	return s.key, nil
}
func (s modelCatalogProxyStore) ListAPIKeys(context.Context) ([]domain.APIKey, error) {
	return nil, nil
}
func (s modelCatalogProxyStore) DeleteAPIKey(context.Context, string) error { return nil }
func (s modelCatalogProxyStore) ResetAPIKeyUsage(context.Context, string, time.Time) error {
	return nil
}
func (s modelCatalogProxyStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	return nil, domain.ErrNoAccounts
}
func (s modelCatalogProxyStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{APIKeyAuthEnabled: true}, nil
}
func (s modelCatalogProxyStore) SaveSettings(context.Context, domain.RuntimeSettings) error {
	return nil
}
func (s modelCatalogProxyStore) LoadAdminSecret(context.Context) (domain.AdminSecret, error) {
	return domain.AdminSecret{}, domain.ErrNotFound
}
func (s modelCatalogProxyStore) SaveAdminSecret(context.Context, domain.AdminSecret) error {
	return nil
}
func (s modelCatalogProxyStore) InitializeAdminSecret(context.Context, domain.AdminSecret) (bool, error) {
	return false, nil
}
func (s modelCatalogProxyStore) AdvanceTOTPStep(context.Context, int64) (bool, error) {
	return false, nil
}

type modelCatalogStore struct {
	sources  []domain.ModelSource
	eligible []domain.Account
	record   domain.ModelCatalogRecord
}

func (s modelCatalogStore) ListAccounts(context.Context) ([]domain.Account, error) {
	return nil, nil
}
func (s modelCatalogStore) GetAccountCredential(context.Context, string) (domain.AccountCredential, error) {
	return domain.AccountCredential{}, domain.ErrNotFound
}
func (s modelCatalogStore) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	return s.sources, nil
}
func (s modelCatalogStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	if len(s.eligible) != 0 {
		return s.eligible, nil
	}
	return s.eligible, domain.ErrNoAccounts
}
func (s modelCatalogStore) LoadModelCatalogSnapshot(context.Context) (domain.ModelCatalogRecord, error) {
	return s.record, nil
}
func (s modelCatalogStore) SaveModelCatalogSnapshot(context.Context, domain.ModelCatalogRecord, ...domain.Account) error {
	return nil
}

func modelCatalogHandler(t *testing.T) *ModelCatalogHandler {
	t.Helper()
	store := modelCatalogStore{
		sources: []domain.ModelSource{{
			ID: "src", Name: "Source", Kind: domain.ModelSourceOpenAICompatible, Enabled: true,
			Chat: true, Responses: true, Models: []domain.ModelSourceModel{{
				SourceID: "src", Model: "source-model", Streaming: true, Enabled: true,
				RawMetadataJSON: `{"source_request_overrides":{"num_ctx":1},"future":{"kept":true},"experimental_supported_tools":[2,"web_search"],"truncation_policy":{"mode":"bad"}}`,
			}},
		}, {
			ID: "chat-src", Name: "Chat Source", Kind: domain.ModelSourceZAI, Enabled: true, Chat: true,
			Models: []domain.ModelSourceModel{{SourceID: "chat-src", Model: "chat-model", Streaming: true, Enabled: true}},
		}, {
			ID: "disabled", Name: "Disabled", Kind: domain.ModelSourceOpenAICompatible, Enabled: false, Chat: true,
			Models: []domain.ModelSourceModel{{SourceID: "disabled", Model: "disabled-model", Enabled: true}},
		}},
		eligible: []domain.Account{
			{ID: "src", Kind: domain.AccountExternal, Status: domain.AccountActive},
			{ID: "chat-src", Kind: domain.AccountExternal, Status: domain.AccountActive},
		},
	}
	key := domain.APIKey{ID: "key", IsActive: true, CreatedAt: time.Now()}
	service := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	return NewModelCatalogHandler(modelCatalogProxyStore{key: key}, service)
}

func TestModelCatalogRoutesServeCanonicalAndAliasPayloads(t *testing.T) {
	handler := modelCatalogHandler(t)
	mux := http.NewServeMux()
	handler.RegisterPublicRoutes(mux)
	get := func(path string) string {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	canonical := get("/backend-api/codex/models")
	for _, path := range []string{"/backend-api/codex/models/", "/v1/models?client_version=0.156.0", "/v1/models/?client_version=0.156.0"} {
		if alias := get(path); canonical != alias {
			t.Fatalf("canonical and alias %s differ:\n%s\n%s", path, canonical, alias)
		}
	}
	var payload struct {
		Models []map[string]json.RawMessage `json:"models"`
		Object string                       `json:"object"`
		Data   []json.RawMessage            `json:"data"`
	}
	if err := json.Unmarshal([]byte(canonical), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Object != "list" || len(payload.Models) == 0 || len(payload.Data) == 0 {
		t.Fatalf("invalid Codex catalog shape: object=%q models=%d data=%d", payload.Object, len(payload.Models), len(payload.Data))
	}
	if strings.Contains(canonical, "source_request_overrides") {
		t.Fatal("operator request overrides leaked")
	}
	var source map[string]json.RawMessage
	for _, model := range payload.Models {
		if string(model["slug"]) == `"source-model"` {
			source = model
			break
		}
	}
	if source == nil || string(source["future"]) != `{"kept":true}` {
		t.Fatalf("source metadata was not preserved: %#v", source)
	}
	for _, field := range []string{"default_verbosity", "default_reasoning_level", "minimal_client_version"} {
		if string(source[field]) != "null" {
			t.Fatalf("absent Codex optional %s must be null, got %s", field, source[field])
		}
	}
	if string(source["truncation_policy"]) != `{"limit":10000,"mode":"tokens"}` ||
		string(source["experimental_supported_tools"]) != `["web_search"]` {
		t.Fatalf("required Codex fields were not repaired: %s %s", source["truncation_policy"], source["experimental_supported_tools"])
	}
	foundChat, foundDisabled := false, false
	for _, model := range payload.Models {
		if string(model["slug"]) == `"chat-model"` {
			foundChat = true
		}
		if string(model["slug"]) == `"disabled-model"` {
			foundDisabled = true
		}
	}
	if !foundChat || foundDisabled {
		t.Fatalf("streaming source filtering mismatch: chat=%v disabled=%v", foundChat, foundDisabled)
	}
}

func TestCodexCatalogOptionalDefaults(t *testing.T) {
	for _, test := range []struct {
		name                          string
		model                         domain.CatalogModel
		reasoning, verbosity, version string
	}{
		{"absent", domain.CatalogModel{}, "null", "null", "null"},
		{"invalid enums", domain.CatalogModel{DefaultReasoningLevel: "unknown", DefaultVerbosity: "unknown"}, "null", "null", "null"},
		{"declared", domain.CatalogModel{DefaultReasoningLevel: "max", DefaultVerbosity: "medium", MinimalClientVersion: "0.156.0"}, `"max"`, `"medium"`, `"0.156.0"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := codexEntry(test.model)
			for field, want := range map[string]string{"default_reasoning_level": test.reasoning, "default_verbosity": test.verbosity, "minimal_client_version": test.version} {
				if string(entry[field]) != want {
					t.Errorf("%s=%s, want %s", field, entry[field], want)
				}
			}
		})
	}
}

func TestModelCatalogOpenAIShapeAndDashboard(t *testing.T) {
	handler := modelCatalogHandler(t)
	public := http.NewServeMux()
	handler.RegisterPublicRoutes(public)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"list"`) ||
		strings.Contains(response.Body.String(), `"models"`) {
		t.Fatalf("OpenAI shape changed: %d %s", response.Code, response.Body.String())
	}
	admin := http.NewServeMux()
	handler.RegisterAdminRoutes(admin)
	dashboard := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, dashboard)
	body, _ := io.ReadAll(response.Body)
	if response.Code != http.StatusOK || !strings.Contains(string(body), `"supportedReasoningEfforts"`) {
		t.Fatalf("dashboard model metadata missing: %d %s", response.Code, body)
	}
	_ = body
}

func TestCodexCatalogKeepsAllowedModelVisibleForGroupedKeys(t *testing.T) {
	now := time.Now().UTC()
	model := domain.CatalogModel{
		Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol", SupportedInAPI: true,
		MinimalClientVersion: "0.153.0", SourceKind: domain.ModelCatalogSourceSubscription,
		Raw: map[string]json.RawMessage{"visibility": json.RawMessage(`"list"`)},
	}
	store := modelCatalogStore{record: domain.ModelCatalogRecord{
		SchemaVersion: application.ModelCatalogSchemaVersion, RefreshedAt: now,
		Snapshot: &domain.CatalogSnapshot{Models: map[string]domain.CatalogModel{model.Slug: model}},
	}}
	catalog := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	if err := catalog.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	group := "mail-group"
	for _, tc := range []struct {
		name    string
		group   *string
		allowed []string
		visible bool
	}{
		{"restricted", &group, []string{"gpt-6-luna"}, false},
		{"grouped", &group, []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "codex-auto-review", "gpt-6.1-sol"}, true},
		{"ungrouped", nil, []string{"gpt-6.1-sol"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := domain.APIKey{ID: tc.name, IsActive: true, GroupID: tc.group, AllowedModels: tc.allowed, ApplyToCodexModel: true}
			handler := NewModelCatalogHandler(modelCatalogProxyStore{key: key}, catalog)
			mux := http.NewServeMux()
			handler.RegisterPublicRoutes(mux)
			for _, path := range []string{"/backend-api/codex/models", "/v1/models?client_version=0.156.0"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set("Authorization", "Bearer test")
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", path, response.Code)
				}
				var payload struct {
					Models []struct {
						Slug                 string `json:"slug"`
						Visibility           string `json:"visibility"`
						MinimalClientVersion string `json:"minimal_client_version"`
					} `json:"models"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				visible := false
				for _, item := range payload.Models {
					if item.Slug == model.Slug {
						visible = item.Visibility == "list"
						if item.MinimalClientVersion != model.MinimalClientVersion {
							t.Fatal("upstream client compatibility requirement changed")
						}
					}
				}
				if visible != tc.visible {
					t.Fatalf("GET %s model visible=%v, want %v", path, visible, tc.visible)
				}
			}
		})
	}
}
