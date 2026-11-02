package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"codex-lb/internal/adapters/xai"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// grokLanguageModels is the shape of xAI's GET /v1/language-models, prices in
// 1e-4 USD per 1M tokens.
const grokLanguageModels = `{"models":[
{"id":"grok-4.7","aliases":[],"input_modalities":["text","image"],"prompt_text_token_price":20000,"cached_prompt_text_token_price":5000,"completion_text_token_price":60000},
{"id":"grok-build-0.1","aliases":["grok-code-fast-1","grok-code-fast"],"input_modalities":["text"],"prompt_text_token_price":10000,"cached_prompt_text_token_price":2000,"completion_text_token_price":20000}]}`

// grokTextStream is what xAI streamed for a Codex-shaped request on
// 2026-11-02: a reasoning item, then a function call.
var grokTextStream = strings.Join([]string{
	`event: response.created` + "\n" + `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_grok","object":"response","status":"in_progress","model":"grok-4.7","output":[]}}`,
	`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"id":"rs_grok","type":"reasoning","status":"completed","summary":[],"encrypted_content":"synthetic-reasoning"}}`,
	`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","sequence_number":2,"output_index":1,"item":{"id":"fc_grok","type":"function_call","status":"completed","call_id":"call_grok","name":"shell_command","arguments":"{\"command\":\"echo pong\"}"}}`,
	`event: response.completed` + "\n" + `data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_grok","object":"response","status":"completed","model":"grok-4.7","output":[{"id":"rs_grok","type":"reasoning","status":"completed","summary":[],"encrypted_content":"synthetic-reasoning"},{"id":"fc_grok","type":"function_call","status":"completed","call_id":"call_grok","name":"shell_command","arguments":"{\"command\":\"echo pong\"}"}],"usage":{"input_tokens":1358,"input_tokens_details":{"cached_tokens":1152},"output_tokens":33,"output_tokens_details":{"reasoning_tokens":20},"total_tokens":1391,"cost_in_usd_ticks":4032400}}}`,
}, "\n\n") + "\n\n"

const grokCodexRequest = `{"model":"grok-4.7","instructions":"You are a coding agent.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Run the command: echo pong"}]}],"tools":[{"type":"function","name":"shell_command","description":"Runs a shell command.","strict":false,"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}}],"tool_choice":"auto","parallel_tool_calls":true,"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"synthetic-conversation"}`

// xaiRefusal answers as xAI did on 2026-11-02 to the request fields it does
// not take, so tests fail where production would.
func xaiRefusal(body []byte) (int, string) {
	var request struct {
		Model     string `json:"model"`
		Tools     []map[string]json.RawMessage
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return http.StatusUnprocessableEntity, `{"error":"Failed to deserialize the JSON body"}`
	}
	for i, tool := range request.Tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind == "namespace" {
			return http.StatusUnprocessableEntity, fmt.Sprintf(`{"error":"tools[%d].type: unknown variant namespace"}`, i)
		}
		if _, ok := tool["external_web_access"]; ok && kind == "web_search" {
			return http.StatusBadRequest, `{"code":"400","error":"Argument not supported: external_web_access"}`
		}
	}
	if request.Reasoning != nil && request.Reasoning.Effort != "" {
		levels := map[string]string{"grok-4.7": "low medium high xhigh", "grok-4.6": "low medium high xhigh", "grok-4.5": "low medium high xhigh"}[request.Model]
		if levels == "" {
			return http.StatusBadRequest, fmt.Sprintf(`{"code":"invalid-argument","error":"Model %s does not support parameter reasoningEffort."}`, request.Model)
		}
		if !strings.Contains(" "+levels+" ", " "+request.Reasoning.Effort+" ") {
			return http.StatusBadRequest, `{"code":"invalid-argument","error":"Invalid reasoning effort."}`
		}
	}
	return 0, ""
}

type grokTextFixture struct {
	video  videoFixture
	server *httptest.Server
	admin  http.Handler
	cookie *http.Cookie
	vault  application.SecretCipher
}

// newGrokTextFixture wires the proxy, a real provider and the admin API to
// one loopback xAI with signed-in Grok accounts. The ChatGPT account of the
// wire fixture is paused, so text can only reach a Grok source.
func newGrokTextFixture(t *testing.T, accounts ...string) grokTextFixture {
	t.Helper()
	stub := newXAIStub(t)
	var adapter *provider.Adapter
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "grok-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewVideoService(store, vault, xai.New(xai.Config{HTTPClient: stub.server.Client(), APIBaseURL: stub.server.URL, AuthBaseURL: stub.server.URL, BillingBaseURL: stub.server.URL, ClientID: "synthetic-cli-client"}))
	service.ConfigureAdmission(proxy)
	adapter = provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: stub.server.Client(), GrokTokens: service, GrokBaseURL: stub.server.URL + "/v1"})
	t.Cleanup(func() { _ = adapter.Close() })
	sources := application.NewModelSourceService(store, vault)
	proxy.ResolvePrice = sources.Price
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountPaused
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	f := grokTextFixture{video: videoFixture{stub: stub, store: store, service: service}, server: server, vault: vault}
	for _, name := range accounts {
		signInVideoAccount(t, f.video, name)
	}
	api := httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil)
	api.ConfigureVideo(service)
	f.admin = api.Handler(nil, nil)
	setup := f.adminRequest(http.MethodPost, "/api/dashboard-auth/password/setup", `{"password":"synthetic-grok-password","bootstrapToken":"synthetic-bootstrap-token-0123456789abcdef"}`)
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) == 0 {
		t.Fatalf("admin setup: %d", setup.Code)
	}
	f.cookie = setup.Result().Cookies()[0]
	return f
}

func (f *grokTextFixture) adminRequest(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5678"
	r.Header.Set("Content-Type", "application/json")
	if f.cookie != nil {
		r.AddCookie(f.cookie)
	}
	w := httptest.NewRecorder()
	f.admin.ServeHTTP(w, r)
	return w
}

func (f *grokTextFixture) createSource(t *testing.T, accountID string) domain.ModelSource {
	t.Helper()
	w := f.adminRequest(http.MethodPost, "/api/model-sources", fmt.Sprintf(`{"kind":"grok_subscription","name":"Grok","providerConfig":{"grokAccountId":%q}}`, accountID))
	if w.Code != http.StatusOK {
		t.Fatalf("create Grok source: %d %s", w.Code, w.Body.String())
	}
	var source domain.ModelSource
	if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	return source
}

func (f *grokTextFixture) codexRequest(t *testing.T, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, f.server.URL+"/backend-api/codex/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer synthetic-key")
	r.Header.Set("Content-Type", "application/json")
	client := f.server.Client()
	client.Timeout = 10 * time.Second
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(data)
}

func (x *xaiStub) responseCalls() []videoCreate {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]videoCreate(nil), x.responses...)
}

func (x *xaiStub) refreshCount(account string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.refreshes[account]
}

func TestGrokSourceTakesItsModelsFromXAI(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	accountID := f.video.accountStatus(t, "alpha").ID
	source := f.createSource(t, accountID)
	if source.Kind != domain.ModelSourceGrokSubscription || source.BaseURL != domain.GrokTextBaseURL || !source.Responses || source.Chat ||
		source.ProviderConfig.GrokAccountID != accountID || len(source.Models) != 2 {
		t.Fatalf("source: %+v", source)
	}
	byName := map[string]domain.ModelSourceModel{}
	for _, model := range source.Models {
		byName[model.Model] = model
	}
	latest, build := byName["grok-4.7"], byName["grok-build-0.1"]
	if latest.InputPerMillion == nil || *latest.InputPerMillion != 2 || *latest.CachedPerMillion != 0.5 || *latest.OutputPerMillion != 6 ||
		!latest.Vision || !latest.Tools || !latest.Streaming || !latest.Enabled || latest.ContextWindow != 256000 {
		t.Fatalf("grok-4.7: %+v", latest)
	}
	if *build.InputPerMillion != 1 || *build.CachedPerMillion != 0.2 || *build.OutputPerMillion != 2 || build.Vision ||
		strings.Join(build.Aliases, ",") != "grok-code-fast-1,grok-code-fast" {
		t.Fatalf("grok-build-0.1: %+v", build)
	}
	credential, err := f.video.store.GetAccountCredential(context.Background(), source.ID)
	if err != nil || len(credential.ExternalKeyEncrypted) != 0 {
		t.Fatalf("a Grok source stored a key: %v", err)
	}
}

func TestGrokSourceRejectsKeysOtherEndpointsAndUnknownAccounts(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	accountID := f.video.accountStatus(t, "alpha").ID
	for name, body := range map[string]string{
		"API key":                 fmt.Sprintf(`{"kind":"grok_subscription","name":"Grok","apiKey":"synthetic-key","providerConfig":{"grokAccountId":%q}}`, accountID),
		"other endpoint":          fmt.Sprintf(`{"kind":"grok_subscription","name":"Grok","baseUrl":"https://proxy.example.invalid/v1","providerConfig":{"grokAccountId":%q}}`, accountID),
		"unknown account":         `{"kind":"grok_subscription","name":"Grok","providerConfig":{"grokAccountId":"vacc_missing"}}`,
		"no account":              `{"kind":"grok_subscription","name":"Grok"}`,
		"account on another kind": fmt.Sprintf(`{"name":"Other","baseUrl":"https://api.example.invalid/v1","providerConfig":{"grokAccountId":%q},"models":[{"model":"m"}]}`, accountID),
	} {
		if w := f.adminRequest(http.MethodPost, "/api/model-sources", body); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	f.video.stub.mu.Lock()
	f.video.stub.models = func() (int, string) { return 500, `{"error":"unavailable"}` }
	f.video.stub.mu.Unlock()
	w := f.adminRequest(http.MethodPost, "/api/model-sources", fmt.Sprintf(`{"kind":"grok_subscription","name":"Grok","providerConfig":{"grokAccountId":%q}}`, accountID))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "grok_models_unavailable") {
		t.Fatalf("failed model read: %d %s", w.Code, w.Body.String())
	}
	sources, err := f.video.store.ListModelSources(context.Background())
	if err != nil || len(sources) != 0 {
		t.Fatalf("rejected requests saved sources: %+v %v", sources, err)
	}
	f.video.stub.mu.Lock()
	f.video.stub.models = func() (int, string) { return 200, grokLanguageModels }
	f.video.stub.mu.Unlock()
	source := f.createSource(t, accountID)
	for name, body := range map[string]string{
		"API key":        `{"apiKey":"synthetic-key"}`,
		"other endpoint": `{"baseUrl":"https://proxy.example.invalid/v1"}`,
		"other account":  `{"providerConfig":{"grokAccountId":"vacc_missing"}}`,
	} {
		if w := f.adminRequest(http.MethodPatch, "/api/model-sources/"+source.ID, body); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("update with %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := f.adminRequest(http.MethodPatch, "/api/model-sources/"+source.ID, `{"name":"Grok text"}`); w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
}

func TestGrokSourceServesCodexWithTheAccountToken(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	ctx := context.Background()
	source := f.createSource(t, f.video.accountStatus(t, "alpha").ID)
	status, body := f.codexRequest(t, grokCodexRequest)
	if status != http.StatusOK || !strings.Contains(body, "event: response.completed") || !strings.Contains(body, `"name":"shell_command"`) {
		t.Fatalf("Codex request: %d %s", status, body)
	}
	calls := f.video.stub.responseCalls()
	if len(calls) != 1 || calls[0].account != "alpha" {
		t.Fatalf("xAI calls: %+v", calls)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal([]byte(calls[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["model"]) != `"grok-4.7"` || sent["tools"] == nil || string(sent["store"]) != "false" {
		t.Fatalf("xAI body: %s", calls[0].body)
	}
	totals, err := f.video.store.UsageTotals(ctx, "wire-key", source.ID)
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 1358 || totals.Usage.OutputTokens != 33 {
		t.Fatalf("usage: %+v %v", totals, err)
	}
	account, err := f.video.store.GetAccount(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	price, err := application.NewModelSourceService(f.video.store, f.vault).Price(ctx, account, "grok-4.7")
	if err != nil || price.Standard.Input != 2_000_000 || price.Standard.Cached != 500_000 || price.Standard.Output != 6_000_000 {
		t.Fatalf("settlement price: %+v %v", price, err)
	}
}

func TestGrokSourceRenewsARejectedTokenOnce(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	f.createSource(t, f.video.accountStatus(t, "alpha").ID)
	f.video.stub.mu.Lock()
	f.video.stub.reject401["alpha-0"] = true
	f.video.stub.mu.Unlock()
	status, body := f.codexRequest(t, grokCodexRequest)
	if status != http.StatusOK || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("request after one rejection: %d %s", status, body)
	}
	if got := f.video.stub.refreshCount("alpha"); got != 1 {
		t.Fatalf("refreshes: %d", got)
	}
	if calls := f.video.stub.responseCalls(); len(calls) != 1 {
		t.Fatalf("accepted calls: %+v", calls)
	}
	f.video.stub.mu.Lock()
	f.video.stub.reject401["alpha-1"], f.video.stub.reject401["alpha-2"] = true, true
	f.video.stub.mu.Unlock()
	if status, body := f.codexRequest(t, grokCodexRequest); status < 400 {
		t.Fatalf("request after two rejections: %d %s", status, body)
	}
	if got := f.video.stub.refreshCount("alpha"); got != 2 {
		t.Fatalf("a third attempt refreshed again: %d refreshes", got)
	}
}

func TestGrokSourceNeedsAnAccountThatCanLendItsToken(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	ctx := context.Background()
	accountID := f.video.accountStatus(t, "alpha").ID
	f.createSource(t, accountID)
	if err := f.video.service.UpdateAccount(ctx, accountID, nil, videoStatusPtr(domain.VideoAccountPaused)); err != nil {
		t.Fatal(err)
	}
	if status, body := f.codexRequest(t, grokCodexRequest); status != http.StatusOK {
		t.Fatalf("a video pause stopped text: %d %s", status, body)
	}
	if err := f.video.store.SetVideoAccountStatus(ctx, accountID, domain.VideoAccountReauthRequired, "refresh_refused", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	status, body := f.codexRequest(t, grokCodexRequest)
	if status < 400 || !strings.Contains(body, "grok_account_unavailable") {
		t.Fatalf("account without a session: %d %s", status, body)
	}
	if calls := f.video.stub.responseCalls(); len(calls) != 1 {
		t.Fatalf("a request without a token reached xAI: %+v", calls)
	}
	if got := f.video.stub.refreshCount("alpha"); got != 0 {
		t.Fatalf("refreshes: %d", got)
	}
}

// grokCLIRequest is what Codex CLI 0.165.4 sends without catalog metadata:
// its own reasoning settings, MCP tools as a namespace and a cached-only search.
const grokCLIRequest = `{"model":"grok-4.7","stream":true,"store":false,"include":["reasoning.encrypted_content"],"prompt_cache_key":"thread","client_metadata":{"session_id":"thread"},"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"max","summary":"detailed"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"List the agents."}]},{"type":"function_call","call_id":"call_0","name":"list_agents","namespace":"agents","arguments":"{}"},{"type":"function_call_output","call_id":"call_0","output":"[]"}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},{"type":"namespace","name":"agents","description":"Agents","tools":[{"type":"function","name":"list_agents","description":"List agents","parameters":{"type":"object","properties":{}}}]},{"type":"web_search","external_web_access":false}]}`

func TestGrokSourceTakesTheCodexCLIRequestShape(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	f.createSource(t, f.video.accountStatus(t, "alpha").ID)
	call := `{"id":"fc_grok","type":"function_call","status":"completed","call_id":"call_1","name":"agents-list_agents","arguments":"{}"}`
	f.video.stub.mu.Lock()
	f.video.stub.respond = func(string, int) (int, string) {
		return 200, strings.Join([]string{
			`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"resp_ns","object":"response","status":"in_progress","model":"grok-4.7","output":[]}}`,
			`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":` + call + `}`,
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_ns","object":"response","status":"completed","model":"grok-4.7","output":[` + call + `],"usage":{"input_tokens":50,"output_tokens":5,"total_tokens":55}}}`,
		}, "\n\n") + "\n\n"
	}
	f.video.stub.mu.Unlock()
	status, body := f.codexRequest(t, grokCLIRequest)
	if status != http.StatusOK {
		t.Fatalf("Codex CLI request: %d %s", status, body)
	}
	for _, want := range []string{`"name":"list_agents"`, `"namespace":"agents"`} {
		if strings.Count(body, want) < 2 {
			t.Fatalf("the namespaced call did not come back in both events (%s): %s", want, body)
		}
	}
	if strings.Contains(body, "agents-list_agents") {
		t.Fatalf("the flat name reached the client: %s", body)
	}
	calls := f.video.stub.responseCalls()
	if len(calls) != 1 {
		t.Fatalf("xAI calls: %d", len(calls))
	}
	var sent struct {
		Tools     []map[string]any `json:"tools"`
		Input     []map[string]any `json:"input"`
		Reasoning map[string]any   `json:"reasoning"`
		Metadata  map[string]any   `json:"client_metadata"`
	}
	if err := json.Unmarshal([]byte(calls[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 2 || sent.Tools[1]["name"] != "agents-list_agents" || sent.Input[1]["name"] != "agents-list_agents" ||
		sent.Reasoning["effort"] != "xhigh" || sent.Reasoning["summary"] != "detailed" || sent.Metadata == nil {
		t.Fatalf("xAI body: %s", calls[0].body)
	}
}

func TestGrokSourceAdvertisesTheEffortsXAITakes(t *testing.T) {
	f := newGrokTextFixture(t, "alpha")
	source := f.createSource(t, f.video.accountStatus(t, "alpha").ID)
	for _, model := range source.Models {
		switch model.Model {
		case "grok-4.7":
			if model.RawMetadataJSON != `{"default_reasoning_level":"high","supported_reasoning_levels":["low","medium","high","xhigh"],"supports_reasoning":true}` {
				t.Fatalf("grok-4.7 metadata: %s", model.RawMetadataJSON)
			}
		default:
			if model.RawMetadataJSON != "" {
				t.Fatalf("%s advertises efforts: %s", model.Model, model.RawMetadataJSON)
			}
		}
	}
	status, body := f.codexRequest(t, strings.Replace(grokCodexRequest, `"model":"grok-4.7"`, `"model":"grok-code-fast-1","reasoning":{"effort":"high"}`, 1))
	if status != http.StatusOK {
		t.Fatalf("a model without efforts refused the request: %d %s", status, body)
	}
}
