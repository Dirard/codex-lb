package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/antigravity"
	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// antigravityGoogle is one loopback stand-in for Google's token endpoint,
// userinfo and Cloud Code. The account has no project yet, so sign-in must
// onboard it; the onboarding operation finishes on its second poll.
type antigravityGoogle struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	email    string
	models   string
	onboards int
	paths    []string
	verifier string
}

const antigravityModels = `{"models":{
"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash","maxTokens":1048576,"maxOutputTokens":65536,"supportsImages":true,"supportsThinking":true},
"claude-opus-5-5-high":{"displayName":"Claude Opus 5.5","maxTokens":1000000,"maxOutputTokens":128000,"supportsImages":true,"supportsThinking":true},
"tab_flash_lite_preview":{},"gemini-pro-orchestrator-g1-pro":{},"gemini-3.1-flash-image":{}}}`

func newAntigravityGoogle(t *testing.T) *antigravityGoogle {
	g := &antigravityGoogle{t: t, email: "agent@example.test", models: antigravityModels}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.paths = append(g.paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		bearer := r.Header.Get("Authorization")
		if r.URL.Path != "/token" && bearer != "Bearer synthetic-google-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/token":
			form, _ := url.ParseQuery(string(body))
			if form.Get("grant_type") == "authorization_code" {
				if form.Get("code") != "4/synthetic-code" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Malformed auth code 4/` + form.Get("code") + `"}`))
					return
				}
				g.verifier = form.Get("code_verifier")
			}
			_, _ = w.Write([]byte(`{"access_token":"synthetic-google-access","expires_in":3599,"refresh_token":"synthetic-google-refresh","token_type":"Bearer"}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"` + g.email + `"}`))
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"currentTier":{"id":"free-tier"},"allowedTiers":[{"id":"free-tier","isDefault":true}]}`))
		case "/v1internal:onboardUser":
			g.onboards++
			if g.onboards%2 == 1 {
				_, _ = w.Write([]byte(`{"name":"operations/onboard","done":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"name":"operations/onboard","done":true,"response":{"cloudaicompanionProject":"onboarded-project"}}`))
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(g.models))
		default:
			t.Errorf("unexpected Google path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.server.Close)
	return g
}

type antigravityAdminFixture struct {
	google *antigravityGoogle
	store  *sqlite.Store
	vault  application.SecretCipher
	admin  http.Handler
	cookie *http.Cookie
	logs   *bytes.Buffer
	bodies []string
}

func newAntigravityAdminFixture(t *testing.T) *antigravityAdminFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "antigravity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	google := newAntigravityGoogle(t)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := application.NewAntigravityService(store, vault, antigravity.New(antigravity.Config{
		HTTPClient: google.server.Client(), TokenURL: google.server.URL + "/token", UserInfoURL: google.server.URL + "/userinfo",
		CloudCodeBaseURL: google.server.URL, OnboardInterval: time.Millisecond,
	}))
	service.ConfigureLogger(logger)
	api := httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, logger)
	api.ConfigureAntigravity(service)
	f := &antigravityAdminFixture{google: google, store: store, vault: vault, admin: api.Handler(nil, nil), logs: logs}
	setup := f.request(http.MethodPost, "/api/dashboard-auth/password/setup", `{"password":"synthetic-antigravity-password","bootstrapToken":"synthetic-bootstrap-token-0123456789abcdef"}`)
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) == 0 {
		t.Fatalf("admin setup: %d", setup.Code)
	}
	f.cookie = setup.Result().Cookies()[0]
	return f
}

func (f *antigravityAdminFixture) request(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5678"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if f.cookie != nil {
		r.AddCookie(f.cookie)
	}
	w := httptest.NewRecorder()
	f.admin.ServeHTTP(w, r)
	f.bodies = append(f.bodies, w.Body.String())
	return w
}

func (f *antigravityAdminFixture) start(t *testing.T, body string) application.AntigravitySignInStart {
	t.Helper()
	w := f.request(http.MethodPost, "/api/antigravity/sign-in", body)
	if w.Code != http.StatusOK {
		t.Fatalf("start sign-in: %d %s", w.Code, w.Body.String())
	}
	var start application.AntigravitySignInStart
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	return start
}

func (f *antigravityAdminFixture) complete(t *testing.T, flowID string) domain.ModelSource {
	t.Helper()
	w := f.request(http.MethodPost, "/api/antigravity/sign-in/"+flowID, `{"code":"4/synthetic-code"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("complete sign-in: %d %s", w.Code, w.Body.String())
	}
	var source domain.ModelSource
	if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	return source
}

// noSecrets checks every response body and log line for Google's tokens and
// the PKCE verifier.
func (f *antigravityAdminFixture) noSecrets(t *testing.T) {
	t.Helper()
	secrets := []string{"synthetic-google-access", "synthetic-google-refresh"}
	if f.google.verifier != "" {
		secrets = append(secrets, f.google.verifier)
	}
	for _, secret := range secrets {
		for _, body := range f.bodies {
			if strings.Contains(body, secret) {
				t.Fatalf("a response carries a secret: %s", body)
			}
		}
		if strings.Contains(f.logs.String(), secret) {
			t.Fatalf("the log carries a secret: %s", f.logs.String())
		}
	}
}

func TestAntigravitySignInThroughTheAdminAPI(t *testing.T) {
	f := newAntigravityAdminFixture(t)
	start := f.start(t, "")
	parsed, err := url.Parse(start.AuthorizationURL)
	if err != nil || parsed.Host != "accounts.google.com" {
		t.Fatalf("authorization URL: %s", start.AuthorizationURL)
	}
	query := parsed.Query()
	if query.Get("redirect_uri") != antigravity.RedirectURI || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" ||
		query.Get("access_type") != "offline" || query.Get("prompt") != "consent" || query.Get("state") == "" || query.Get("client_id") != antigravity.ClientID {
		t.Fatalf("authorization query: %v", query)
	}
	if until := time.Until(start.ExpiresAt); until < 9*time.Minute || until > 10*time.Minute {
		t.Fatalf("flow expiry: %s", start.ExpiresAt)
	}
	source := f.complete(t, start.FlowID)
	info := source.ProviderConfig.Antigravity
	if source.Kind != domain.ModelSourceAntigravity || source.Name != "agent@example.test" || source.BaseURL != domain.AntigravityBaseURL ||
		!source.Responses || !source.Chat || !source.StickyOnly || source.AccountStatus != domain.AccountActive ||
		info == nil || info.Email != "agent@example.test" || info.Project != "onboarded-project" || info.Tier != "free-tier" {
		t.Fatalf("source: %+v %+v", source, info)
	}
	if f.google.onboards != 2 {
		t.Fatalf("onboarding polls: %d", f.google.onboards)
	}
	if len(source.Models) != 2 || source.Models[0].Model != "claude-opus-5-5-high" || source.Models[1].Model != "gemini-3.8-flash-high" {
		t.Fatalf("models: %+v", source.Models)
	}
	for _, model := range source.Models {
		if model.InputPerMillion == nil || *model.InputPerMillion != 0 || model.CachedPerMillion == nil || *model.CachedPerMillion != 0 ||
			model.OutputPerMillion == nil || *model.OutputPerMillion != 0 || !model.Tools || !model.Streaming || !model.Vision || model.RawMetadataJSON == "" {
			t.Fatalf("model: %+v", model)
		}
	}
	// The refresh token is stored encrypted; the source's account needs no
	// egress decision and is eligible like any saved model source.
	ctx := context.Background()
	credential, err := f.store.GetAccountCredential(ctx, source.ID)
	if err != nil || bytes.Contains(credential.ExternalKeyEncrypted, []byte("synthetic-google-refresh")) {
		t.Fatalf("credential: %v", err)
	}
	plain, err := f.vault.Decrypt(credential.ExternalKeyEncrypted)
	if err != nil || string(plain) != "synthetic-google-refresh" {
		t.Fatalf("credential does not decrypt to the refresh token: %v", err)
	}
	account, err := f.store.GetAccount(ctx, source.ID)
	if err != nil || account.RequiresEgressDecision || account.Status != domain.AccountActive || !account.StickyOnly {
		t.Fatalf("account: %+v %v", account, err)
	}
	listed := f.request(http.MethodGet, "/api/model-sources", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"antigravity":{"email":"agent@example.test","project":"onboarded-project","tier":"free-tier"}`) ||
		!strings.Contains(listed.Body.String(), `"accountStatus":"active"`) {
		t.Fatalf("listed sources: %s", listed.Body.String())
	}
	// The flow was used once.
	again := f.request(http.MethodPost, "/api/antigravity/sign-in/"+start.FlowID, `{"code":"4/synthetic-code"}`)
	if again.Code != http.StatusNotFound || !strings.Contains(again.Body.String(), "antigravity_sign_in_not_found") {
		t.Fatalf("reused flow: %d %s", again.Code, again.Body.String())
	}
	f.noSecrets(t)
}

func TestAntigravitySignInRefusalsThroughTheAdminAPI(t *testing.T) {
	f := newAntigravityAdminFixture(t)
	start := f.start(t, "{}")
	refused := f.request(http.MethodPost, "/api/antigravity/sign-in/"+start.FlowID, `{"code":"4/mistyped"}`)
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "antigravity_code_rejected") || strings.Contains(refused.Body.String(), "4/mistyped") {
		t.Fatalf("refused code: %d %s", refused.Code, refused.Body.String())
	}
	unknown := f.request(http.MethodPost, "/api/antigravity/sign-in/agsign_unknown", `{"code":"4/synthetic-code"}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown flow: %d %s", unknown.Code, unknown.Body.String())
	}
	missing := f.request(http.MethodPost, "/api/antigravity/sign-in", `{"sourceId":"src_missing"}`)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "antigravity_source_not_found") {
		t.Fatalf("re-authorizing an unknown source: %d %s", missing.Code, missing.Body.String())
	}
	f.cookie = nil
	if w := f.request(http.MethodPost, "/api/antigravity/sign-in", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated sign-in started: %d", w.Code)
	}
	sources, _ := f.store.ListModelSources(context.Background())
	if len(sources) != 0 {
		t.Fatalf("a refused sign-in saved a source: %+v", sources)
	}
	f.noSecrets(t)
}

func TestAntigravityReauthorizationAndModelRefreshThroughTheAdminAPI(t *testing.T) {
	f := newAntigravityAdminFixture(t)
	source := f.complete(t, f.start(t, "").FlowID)
	edit := f.request(http.MethodPatch, "/api/model-sources/"+source.ID,
		`{"models":[{"model":"gemini-3.8-flash-high","inputPer1M":1.25,"outputPer1M":5},{"model":"claude-opus-5-5-high","isEnabled":false}]}`)
	if edit.Code != http.StatusOK {
		t.Fatalf("price edit: %d %s", edit.Code, edit.Body.String())
	}
	if key := f.request(http.MethodPatch, "/api/model-sources/"+source.ID, `{"apiKey":"sk-replacement"}`); key.Code != http.StatusUnprocessableEntity && key.Code != http.StatusBadRequest {
		t.Fatalf("an API key replaced the refresh token: %d %s", key.Code, key.Body.String())
	}
	if err := f.store.SetAntigravityAccountStatus(context.Background(), source.ID, domain.AccountReauthRequired, "", nil); err != nil {
		t.Fatal(err)
	}
	again := f.complete(t, f.start(t, `{"sourceId":"`+source.ID+`"}`).FlowID)
	byID := map[string]domain.ModelSourceModel{}
	for _, model := range again.Models {
		byID[model.Model] = model
	}
	if again.ID != source.ID || again.AccountStatus != domain.AccountActive || len(again.Models) != 2 ||
		*byID["gemini-3.8-flash-high"].InputPerMillion != 1.25 || byID["claude-opus-5-5-high"].Enabled {
		t.Fatalf("re-authorized source: %+v", again)
	}
	// A refresh keeps known models as edited, adds new ones free and disables
	// the ones Google stopped listing.
	f.google.mu.Lock()
	f.google.models = `{"models":{"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash","supportsThinking":true},"gemini-4-neon-xhigh":{"displayName":"Gemini 4 Neon","supportsThinking":true}}}`
	f.google.mu.Unlock()
	w := f.request(http.MethodPost, "/api/model-sources/"+source.ID+"/antigravity/refresh-models", "")
	if w.Code != http.StatusOK {
		t.Fatalf("refresh models: %d %s", w.Code, w.Body.String())
	}
	var refreshed domain.ModelSource
	if err := json.Unmarshal(w.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	byID = map[string]domain.ModelSourceModel{}
	for _, model := range refreshed.Models {
		byID[model.Model] = model
	}
	flash, opus, neon := byID["gemini-3.8-flash-high"], byID["claude-opus-5-5-high"], byID["gemini-4-neon-xhigh"]
	if len(refreshed.Models) != 3 || !flash.Enabled || *flash.InputPerMillion != 1.25 || opus.Enabled || !neon.Enabled || *neon.OutputPerMillion != 0 {
		t.Fatalf("refreshed models: %+v", refreshed.Models)
	}
	if missing := f.request(http.MethodPost, "/api/model-sources/src_missing/antigravity/refresh-models", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("refresh of an unknown source: %d %s", missing.Code, missing.Body.String())
	}
	f.noSecrets(t)
}

func TestAntigravityAccountWithAnotherEmailCannotReauthorize(t *testing.T) {
	f := newAntigravityAdminFixture(t)
	source := f.complete(t, f.start(t, "").FlowID)
	f.google.mu.Lock()
	f.google.email = "other@example.test"
	f.google.mu.Unlock()
	start := f.start(t, `{"sourceId":"`+source.ID+`"}`)
	w := f.request(http.MethodPost, "/api/antigravity/sign-in/"+start.FlowID, `{"code":"4/synthetic-code"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "antigravity_account_mismatch") {
		t.Fatalf("mismatched re-authorization: %d %s", w.Code, w.Body.String())
	}
	// Without a named source the other account becomes a second source.
	second := f.complete(t, f.start(t, "").FlowID)
	if second.ID == source.ID || second.Name != "other@example.test" {
		t.Fatalf("second account: %+v", second)
	}
}
