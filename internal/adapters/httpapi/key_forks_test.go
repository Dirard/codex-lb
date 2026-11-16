package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const parentSecret = "synthetic-parent-key"

// forkFixture serves the Responses routes and the key API over one store,
// with two subscription accounts and a parent key.
type forkFixture struct {
	server   *httptest.Server
	store    *sqlite.Store
	admin    *keyAdmin
	mu       sync.Mutex
	accounts []string
}

func newForkFixture(t *testing.T, parent domain.APIKey) *forkFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "forks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	secret, err := vault.Encrypt([]byte("synthetic-credential"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fork-a", "fork-b"} {
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", Email: id + "@example.invalid", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: id, AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
			t.Fatal(err)
		}
	}
	parent.ID, parent.Name, parent.IsActive = "parent-key", "Parent", true
	parent.KeyHash, parent.KeyPrefix = domain.APIKeyHash(parentSecret), "synthetic"
	if err := store.SaveAPIKey(ctx, parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &forkFixture{store: store}
	provider := wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		f.mu.Lock()
		f.accounts = append(f.accounts, target.Account.ID)
		n := len(f.accounts)
		f.mu.Unlock()
		return wireComplete(fmt.Sprintf("fork_resp_%d", n), emit)
	})
	proxy := application.NewProxy(store, provider, vault, application.ProxyConfig{MaxStreams: 4, MaxQueued: 4, QueueTimeout: time.Second})
	proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) {
		return pricing.Price{Standard: pricing.Rates{Input: 100_000_000, Output: 100_000_000}}, nil
	}
	mux := http.NewServeMux()
	responses := httpapi.NewProxyHandler(store, proxy, nil)
	responses.ConfigureRequestPools(application.NewRequestPools())
	responses.RegisterRoutes(mux)
	httpapi.NewKeyForksHandler(store, nil).RegisterRoutes(mux)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.admin = &keyAdmin{handler: httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil).Handler(nil, nil)}
	setup := f.admin.do(t, "POST", "/api/dashboard-auth/password/setup", `{"password":"synthetic-admin-password","bootstrapToken":"synthetic-bootstrap-token-0123456789abcdef"}`)
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) != 1 {
		t.Fatalf("admin setup %d", setup.Code)
	}
	f.admin.cookie = setup.Result().Cookies()[0]
	return f
}

type forkAnswer struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (f *forkFixture) call(t *testing.T, method, path, secret, body string) forkAnswer {
	t.Helper()
	req, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	answer := forkAnswer{status: res.StatusCode, header: res.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &answer.body)
	return answer
}

func (f *forkFixture) respond(t *testing.T, secret, model string) forkAnswer {
	t.Helper()
	return f.call(t, http.MethodPost, "/v1/responses", secret, fmt.Sprintf(`{"model":%q,"input":"hi"}`, model))
}

func (f *forkFixture) createFork(t *testing.T, body string) (id, secret string) {
	t.Helper()
	answer := f.call(t, http.MethodPost, "/v1/key/forks", parentSecret, body)
	if answer.status != http.StatusCreated {
		t.Fatalf("create fork %s: %d %s", body, answer.status, answer.raw)
	}
	return answer.body["id"].(string), answer.body["key"].(string)
}

func errorField(answer forkAnswer, field string) string {
	detail, _ := answer.body["error"].(map[string]any)
	value, _ := detail[field].(string)
	return value
}

func TestKeyForkLifecycleThroughTheKeyAPI(t *testing.T) {
	f := newForkFixture(t, domain.APIKey{AllowedModels: []string{"gpt-6-sol", "gpt-6-luna"}})
	id, secret := f.createFork(t, `{"name":"ci","allowedModels":["gpt-6-luna"],"limits":[{"limitType":"total_tokens","limitWindow":"daily","maxValue":1000}]}`)
	if !strings.HasPrefix(secret, domain.ForkSecretPrefix) {
		t.Fatalf("fork secret %q", secret)
	}
	list := f.call(t, http.MethodGet, "/v1/key/forks", parentSecret, "")
	data, _ := list.body["data"].([]any)
	if list.status != http.StatusOK || list.body["object"] != "list" || len(data) != 1 || strings.Contains(list.raw, secret) {
		t.Fatalf("list %d %s", list.status, list.raw)
	}
	read := f.call(t, http.MethodGet, "/v1/key/forks/"+id, parentSecret, "")
	if read.status != http.StatusOK || read.body["createdBy"] != "owner" || read.body["object"] != "api_key.fork" || read.body["key"] != nil {
		t.Fatalf("read %d %s", read.status, read.raw)
	}
	// The fork serves its own narrower models on its parent's accounts.
	if answer := f.respond(t, secret, "gpt-6-luna"); answer.status != http.StatusOK {
		t.Fatalf("fork request %d %s", answer.status, answer.raw)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusForbidden || errorField(answer, "code") != "model_not_allowed" {
		t.Fatalf("model the fork does not allow: %d %s", answer.status, answer.raw)
	}
	updated := f.call(t, http.MethodPatch, "/v1/key/forks/"+id, parentSecret, `{"name":"ci-2","allowedModels":["gpt-6-sol"]}`)
	if updated.status != http.StatusOK || updated.body["name"] != "ci-2" || updated.body["updatedBy"] != "owner" {
		t.Fatalf("update %d %s", updated.status, updated.raw)
	}
	if limits, _ := updated.body["limits"].([]any); len(limits) != 1 {
		t.Fatalf("update dropped the fork's limits: %s", updated.raw)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusOK {
		t.Fatalf("updated fork %d %s", answer.status, answer.raw)
	}
	// A fork manages nothing, and a key sees only its own forks.
	if answer := f.call(t, http.MethodGet, "/v1/key/forks", secret, ""); answer.status != http.StatusForbidden || errorField(answer, "code") != "fork_management_forbidden" {
		t.Fatalf("fork managing forks: %d %s", answer.status, answer.raw)
	}
	other := domain.APIKey{ID: "other-key", Name: "Other", KeyHash: domain.APIKeyHash("synthetic-other"), KeyPrefix: "synthetic", IsActive: true}
	if err := f.store.SaveAPIKey(context.Background(), other, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		body := ""
		if method == http.MethodPatch {
			body = `{"name":"taken"}`
		}
		if answer := f.call(t, method, "/v1/key/forks/"+id, "synthetic-other", body); answer.status != http.StatusNotFound || errorField(answer, "code") != "fork_not_found" {
			t.Fatalf("%s another key's fork: %d %s", method, answer.status, answer.raw)
		}
	}
	if answer := f.call(t, http.MethodGet, "/v1/key/forks", "", ""); answer.status != http.StatusUnauthorized {
		t.Fatalf("no key: %d", answer.status)
	}
	if answer := f.call(t, http.MethodDelete, "/v1/key/forks/"+id, parentSecret, ""); answer.status != http.StatusNoContent {
		t.Fatalf("delete %d %s", answer.status, answer.raw)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusUnauthorized {
		t.Fatalf("deleted fork served: %d", answer.status)
	}
	// Deleting a parent deletes its forks.
	secondID, second := f.createFork(t, `{"name":"second"}`)
	if err := f.store.DeleteAPIKey(context.Background(), "parent-key"); err != nil {
		t.Fatal(err)
	}
	if answer := f.respond(t, second, "gpt-6-sol"); answer.status != http.StatusUnauthorized {
		t.Fatalf("fork of a deleted parent served: %d", answer.status)
	}
	if _, err := f.store.GetStoredAPIKey(context.Background(), secondID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("fork kept after its parent was deleted: %v", err)
	}
}

func TestKeyForkCannotExceedItsParent(t *testing.T) {
	expiry := time.Now().Add(24 * time.Hour).UTC()
	f := newForkFixture(t, domain.APIKey{AllowedModels: []string{"gpt-6-sol", "gpt-6-luna"}, AllowedReasoningEfforts: []string{"low", "medium"},
		EnforcedServiceTier: ptrTo("flex"), ExpiresAt: &expiry, RequestPool: &domain.RequestPool{RequestsPerMinute: 10, MaxWaitSeconds: 20}})
	later := expiry.Add(time.Hour).Format(time.RFC3339)
	for field, body := range map[string]string{
		"allowedModels":           `{"name":"x","allowedModels":["gpt-6-sol","gpt-6-terra"]}`,
		"enforcedModel":           `{"name":"x","enforcedModel":"gpt-6-terra"}`,
		"allowedReasoningEfforts": `{"name":"x","allowedReasoningEfforts":["low","high"]}`,
		"enforcedReasoningEffort": `{"name":"x","enforcedReasoningEffort":"high","allowedReasoningEfforts":null}`,
		"enforcedServiceTier":     `{"name":"x","enforcedServiceTier":"priority"}`,
		"expiresAt":               `{"name":"x","expiresAt":"` + later + `"}`,
		"requestPool":             `{"name":"x","requestPool":{"requestsPerMinute":11}}`,
	} {
		answer := f.call(t, http.MethodPost, "/v1/key/forks", parentSecret, body)
		if answer.status != http.StatusUnprocessableEntity || errorField(answer, "code") != "fork_exceeds_parent" || errorField(answer, "param") != field {
			t.Errorf("%s: %d %s", field, answer.status, answer.raw)
		}
	}
	for _, body := range []string{`{"allowedModels":["gpt-6-sol"]}`, `{"name":"x","allowedReasoningEfforts":[]}`, `{"name":"x","requestPool":{"requestsPerMinute":0}}`} {
		if answer := f.call(t, http.MethodPost, "/v1/key/forks", parentSecret, body); answer.status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", body, answer.status, answer.raw)
		}
	}
	if answer := f.call(t, http.MethodPost, "/v1/key/forks", parentSecret, `{"name":"x","groupId":"g"}`); answer.status != http.StatusBadRequest {
		t.Errorf("inherited field accepted: %d %s", answer.status, answer.raw)
	}
	// Omitted restrictions are the parent's; a narrower update fits, a wider
	// one is refused and changes nothing.
	id, _ := f.createFork(t, `{"name":"same"}`)
	read := f.call(t, http.MethodGet, "/v1/key/forks/"+id, parentSecret, "")
	if models, _ := read.body["allowedModels"].([]any); len(models) != 2 {
		t.Fatalf("default models %s", read.raw)
	}
	if answer := f.call(t, http.MethodPatch, "/v1/key/forks/"+id, parentSecret, `{"allowedModels":["gpt-6-terra"]}`); errorField(answer, "param") != "allowedModels" {
		t.Fatalf("wider update %d %s", answer.status, answer.raw)
	}
	if read := f.call(t, http.MethodGet, "/v1/key/forks/"+id, parentSecret, ""); !strings.Contains(read.raw, "gpt-6-luna") {
		t.Fatalf("refused update changed the fork: %s", read.raw)
	}
	for i := 1; i < domain.MaxForksPerKey; i++ {
		f.createFork(t, fmt.Sprintf(`{"name":"fork %d"}`, i))
	}
	if answer := f.call(t, http.MethodPost, "/v1/key/forks", parentSecret, `{"name":"one too many"}`); answer.status != http.StatusConflict || errorField(answer, "code") != "fork_limit_reached" {
		t.Fatalf("fork beyond the bound: %d %s", answer.status, answer.raw)
	}
}

func TestKeyForkServesWithinItsParent(t *testing.T) {
	f := newForkFixture(t, domain.APIKey{AccountAssignmentScopeEnabled: true, AssignedAccountIDs: []string{"fork-b"},
		Limits: []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1_000_000}}})
	_, secret := f.createFork(t, `{"name":"ci","limits":[{"limitType":"total_tokens","limitWindow":"weekly","maxValue":100000}]}`)
	for range 2 {
		if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusOK {
			t.Fatalf("fork request %d %s", answer.status, answer.raw)
		}
	}
	f.mu.Lock()
	accounts := append([]string(nil), f.accounts...)
	f.mu.Unlock()
	if len(accounts) != 2 || accounts[0] != "fork-b" || accounts[1] != "fork-b" {
		t.Fatalf("fork served outside its parent's accounts: %v", accounts)
	}
	ctx := context.Background()
	parent, err := f.store.GetAPIKey(ctx, "parent-key")
	if err != nil {
		t.Fatal(err)
	}
	forks, err := f.store.ListKeyForks(ctx, "parent-key")
	if err != nil || len(forks) != 1 || forks[0].Limits[0].CurrentValue != 40 {
		t.Fatalf("fork limits %+v %v", forks, err)
	}
	// The fork's cost is charged to its parent's cost limit.
	totals, err := f.store.UsageTotals(ctx, forks[0].ID, "")
	if err != nil || totals.Usage.CostMicrodollars <= 0 || len(parent.Limits) != 1 || parent.Limits[0].CurrentValue != totals.Usage.CostMicrodollars {
		t.Fatalf("parent cost limit %+v, fork cost %+v %v", parent.Limits, totals, err)
	}
	// A full parent limit refuses the fork.
	parent, _ = f.store.GetStoredAPIKey(ctx, "parent-key")
	parent.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: totals.Usage.CostMicrodollars}}
	if err := f.store.SaveAPIKey(ctx, parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusTooManyRequests || errorField(answer, "code") != "api_key_limit_exceeded" {
		t.Fatalf("fork past its parent's limit: %d %s", answer.status, answer.raw)
	}
	// A disabled parent disables its forks until it is enabled again.
	parent, _ = f.store.GetStoredAPIKey(ctx, "parent-key")
	parent.Limits, parent.IsActive = []domain.LimitRule{}, false
	if err := f.store.SaveAPIKey(ctx, parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusUnauthorized {
		t.Fatalf("fork of a disabled parent: %d", answer.status)
	}
	parent.IsActive = true
	if err := f.store.SaveAPIKey(ctx, parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusOK {
		t.Fatalf("fork of a re-enabled parent: %d %s", answer.status, answer.raw)
	}
}

func TestKeyForksShareTheParentPool(t *testing.T) {
	f := newForkFixture(t, domain.APIKey{RequestPool: &domain.RequestPool{RequestsPerMinute: 3}})
	_, a := f.createFork(t, `{"name":"a"}`)
	_, b := f.createFork(t, `{"name":"b","requestPool":{"requestsPerMinute":1,"maxWaitSeconds":0}}`)
	check := func(name string, answer forkAnswer, status int, remaining, used, limit string) {
		t.Helper()
		got := [4]string{answer.header.Get("X-Ratelimit-Limit-Requests"), answer.header.Get("X-Ratelimit-Remaining-Requests"),
			answer.header.Get("X-Ratelimit-Fork-Used-Requests"), answer.header.Get("X-Ratelimit-Fork-Limit-Requests")}
		if answer.status != status || got != [4]string{"3", remaining, used, limit} {
			t.Fatalf("%s: %d %q %s", name, answer.status, got, answer.raw)
		}
	}
	check("parent", f.respond(t, parentSecret, "gpt-6-sol"), http.StatusOK, "2", "", "")
	check("fork a", f.respond(t, a, "gpt-6-sol"), http.StatusOK, "1", "1", "")
	check("fork b", f.respond(t, b, "gpt-6-sol"), http.StatusOK, "0", "1", "1")
	check("fork a over the shared pool", f.respond(t, a, "gpt-6-sol"), http.StatusTooManyRequests, "0", "1", "")
	check("fork b over its cap", f.respond(t, b, "gpt-6-sol"), http.StatusTooManyRequests, "0", "1", "1")
}

func TestAdminControlsForks(t *testing.T) {
	f := newForkFixture(t, domain.APIKey{AllowedModels: []string{"gpt-6-sol", "gpt-6-luna"}})
	id, secret := f.createFork(t, `{"name":"luna","allowedModels":["gpt-6-luna"]}`)
	keep, _ := f.createFork(t, `{"name":"sol","allowedModels":["gpt-6-sol"]}`)
	// The editor reads a fork's own settings and its parent.
	edit := f.admin.do(t, "GET", "/api/api-keys/"+id, "")
	var stored domain.APIKey
	if edit.Code != http.StatusOK || json.Unmarshal(edit.Body.Bytes(), &stored) != nil || stored.Fork == nil ||
		stored.Fork.ParentID != "parent-key" || stored.Fork.ParentName != "Parent" || len(stored.AllowedModels) != 1 {
		t.Fatalf("admin read %d %s", edit.Code, edit.Body)
	}
	if wider := f.admin.do(t, "PATCH", "/api/api-keys/"+id, `{"allowedModels":["gpt-6-terra"]}`); wider.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(wider.Body.String(), `"param":"allowedModels"`) {
		t.Fatalf("admin widening a fork: %d %s", wider.Code, wider.Body)
	}
	if renamed := f.admin.do(t, "PATCH", "/api/api-keys/"+id, `{"name":"renamed"}`); renamed.Code != http.StatusOK {
		t.Fatalf("admin rename %d %s", renamed.Code, renamed.Body)
	}
	if read := f.call(t, http.MethodGet, "/v1/key/forks/"+id, parentSecret, ""); read.body["name"] != "renamed" || read.body["updatedBy"] != "admin" {
		t.Fatalf("admin edit audit %s", read.raw)
	}
	regenerated := f.admin.do(t, "POST", "/api/api-keys/"+id+"/regenerate", "")
	var fresh struct {
		Key string `json:"key"`
	}
	if regenerated.Code != http.StatusOK || json.Unmarshal(regenerated.Body.Bytes(), &fresh) != nil || !strings.HasPrefix(fresh.Key, domain.ForkSecretPrefix) {
		t.Fatalf("regenerate %d %s", regenerated.Code, regenerated.Body)
	}
	if answer := f.respond(t, secret, "gpt-6-luna"); answer.status != http.StatusUnauthorized {
		t.Fatalf("old fork secret after regeneration: %d", answer.status)
	}
	secret = fresh.Key
	if answer := f.respond(t, secret, "gpt-6-luna"); answer.status != http.StatusOK {
		t.Fatalf("regenerated fork secret: %d %s", answer.status, answer.raw)
	}
	keys := f.admin.do(t, "GET", "/api/keys", "")
	if keys.Code != http.StatusOK || !strings.Contains(keys.Body.String(), `"parentId":"parent-key","parentName":"Parent"`) {
		t.Fatalf("keys list %d %s", keys.Code, keys.Body)
	}
	// The administrator narrows the parent: the fork it no longer fits is
	// disabled, the other keeps serving.
	if narrowed := f.admin.do(t, "PATCH", "/api/api-keys/parent-key", `{"allowedModels":["gpt-6-sol"]}`); narrowed.Code != http.StatusOK {
		t.Fatalf("narrow parent %d %s", narrowed.Code, narrowed.Body)
	}
	read := f.call(t, http.MethodGet, "/v1/key/forks/"+id, parentSecret, "")
	if read.body["isActive"] != false || read.body["updatedBy"] != "admin" {
		t.Fatalf("exceeding fork %s", read.raw)
	}
	if read := f.call(t, http.MethodGet, "/v1/key/forks/"+keep, parentSecret, ""); read.body["isActive"] != true {
		t.Fatalf("fitting fork %s", read.raw)
	}
	if answer := f.call(t, http.MethodPatch, "/v1/key/forks/"+id, parentSecret, `{"isActive":true}`); errorField(answer, "param") != "allowedModels" {
		t.Fatalf("re-enabling an exceeding fork: %d %s", answer.status, answer.raw)
	}
	if answer := f.call(t, http.MethodPatch, "/v1/key/forks/"+id, parentSecret, `{"isActive":true,"allowedModels":["gpt-6-sol"]}`); answer.status != http.StatusOK {
		t.Fatalf("fixed fork %d %s", answer.status, answer.raw)
	}
	if answer := f.respond(t, secret, "gpt-6-sol"); answer.status != http.StatusOK {
		t.Fatalf("fixed fork request %d %s", answer.status, answer.raw)
	}
}

func ptrTo[T any](v T) *T { return &v }
