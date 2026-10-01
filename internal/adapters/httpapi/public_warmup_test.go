package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type publicWarmupAdmission struct{}

func (publicWarmupAdmission) Acquire(context.Context) (func(), error) { return func() {}, nil }

func publicWarmupFixture(t *testing.T, upstream *httptest.Server) (*httptest.Server, *sqlite.Store) {
	t.Helper()
	_, store, _ := wireFixtureWithProxy(t, nil)
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	t.Cleanup(func() { adapter.Close() })
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(publicWarmupAdmission{})
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, store
}

func publicWarmupPost(t *testing.T, server *httptest.Server, path, body, key string, headers http.Header) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(payload)
}

func publicWarmupQuota(t *testing.T, store *sqlite.Store, accountID string, used float64) {
	t.Helper()
	minutes := 300
	if err := store.SaveAccountQuota(context.Background(), domain.AccountQuota{AccountID: accountID, Window: "primary", UsedPercent: used, WindowMinutes: &minutes, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func publicWarmupAddAccount(t *testing.T, store *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ID, account.ChatGPTAccountID, account.Email = id, id, id+"@example.invalid"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential.AccountID = id
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
}

func TestPublicWarmupRoutesModelPolicyAndKeyBudget(t *testing.T) {
	var calls atomic.Int64
	var modelsMu sync.Mutex
	models := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model        string          `json:"model"`
			Instructions string          `json:"instructions"`
			Input        json.RawMessage `json:"input"`
			Store        bool            `json:"store"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Instructions != "Warmup request." || string(request.Input) != `[{"role":"user","content":"warmup"},{"type":"compaction_trigger"}]` || request.Store || r.URL.Path != "/codex/responses" {
			t.Errorf("wrong compact warmup payload: %+v", request)
		}
		modelsMu.Lock()
		models = append(models, request.Model)
		modelsMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", calls.Add(1))
	}))
	defer upstream.Close()
	server, store := publicWarmupFixture(t, upstream)
	ctx := context.Background()
	publicWarmupQuota(t, store, "wire-account", 0)
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.WarmupModel = "gpt-6-sol"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []struct{ path, body string }{
		{"/v1/warmup", `{}`}, {"/v1/warmup/", `{"mode":" NORMAL "}`},
		{"/v1/warmup/normal", ""}, {"/v1/warmup/normal/", ""},
	} {
		status, body := publicWarmupPost(t, server, variant.path, variant.body, "synthetic-key", nil)
		var summary application.PublicWarmupSummary
		if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || summary.Mode != "normal" || summary.TotalAccounts != 1 ||
			len(summary.Submitted) != 1 || summary.Submitted[0].Model != "gpt-6-sol" || summary.Submitted[0].RequestID == "" || len(summary.Failed) != 0 {
			t.Fatalf("%s warmup = %d %s", variant.path, status, body)
		}
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 12 {
		t.Fatalf("warmup key usage was not settled: %+v %v", key, err)
	}
	enforced := "gpt-5.4-mini"
	key.EnforcedModel = &enforced
	key.AllowedModels = []string{enforced}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, body := publicWarmupPost(t, server, "/v1/warmup", `{}`, "synthetic-key", nil); status != 200 || !strings.Contains(body, `"model":"gpt-5.4-mini"`) {
		t.Fatalf("enforced warmup model = %d %s", status, body)
	}
	modelsMu.Lock()
	gotModels := append([]string(nil), models...)
	modelsMu.Unlock()
	if len(gotModels) != 5 || gotModels[4] != enforced {
		t.Fatalf("upstream models = %v", gotModels)
	}
	key.EnforcedModel = nil
	key.AllowedModels = []string{"gpt-4.1"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, _ := publicWarmupPost(t, server, "/v1/warmup", `{}`, "synthetic-key", nil); status != 403 || calls.Load() != 5 {
		t.Fatalf("disallowed model dispatched: %d calls=%d", status, calls.Load())
	}
	for _, invalid := range []struct{ path, body string }{
		{"/v1/warmup", `{"mode":"invalid"}`}, {"/v1/warmup", `{"mode":null}`},
		{"/v1/warmup", `{"model":"gpt-6-sol"}`}, {"/v1/warmup/invalid", ""},
	} {
		if status, _ := publicWarmupPost(t, server, invalid.path, invalid.body, "synthetic-key", nil); status != 400 {
			t.Fatalf("invalid mode/body accepted: %s %d", invalid.path, status)
		}
	}
	if status, _ := publicWarmupPost(t, server, "/v1/warmup", `{}`, "", nil); status != 401 {
		t.Fatalf("keyless warmup accepted: %d", status)
	}
	if status, _ := publicWarmupPost(t, server, "/v1/warmup", `{}`, "synthetic-key", http.Header{domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability}}); status != 400 {
		t.Fatalf("capability warmup accepted: %d", status)
	}
}

func TestPublicWarmupNormalStrictForceAndCurrentGroupScope(t *testing.T) {
	var calls atomic.Int64
	var accountsMu sync.Mutex
	accounts := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountsMu.Lock()
		accounts = append(accounts, r.Header.Get("ChatGPT-Account-ID"))
		accountsMu.Unlock()
		calls.Add(1)
		fmt.Fprint(w, `{"id":"resp_warmup","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	server, store := publicWarmupFixture(t, upstream)
	ctx := context.Background()
	publicWarmupAddAccount(t, store, "second")
	publicWarmupQuota(t, store, "wire-account", 0)
	publicWarmupQuota(t, store, "second", 5)
	groupID := "warmup-group"
	group := domain.AccountGroup{ID: groupID, Name: "Warmup", AccountIDs: []string{"wire-account", "second"}, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}
	if err := store.SaveGroup(ctx, group, time.Now()); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.GroupID = &groupID
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body := publicWarmupPost(t, server, "/v1/warmup", `{"mode":"normal"}`, "synthetic-key", nil)
	var normal application.PublicWarmupSummary
	if status != 200 || json.Unmarshal([]byte(body), &normal) != nil || normal.TotalAccounts != 2 || len(normal.Submitted) != 1 || len(normal.Skipped) != 1 || normal.Skipped[0].AccountID != "second" || normal.Skipped[0].Reason != "ineligible_primary_usage" {
		t.Fatalf("normal = %d %s", status, body)
	}
	if status, _ := publicWarmupPost(t, server, "/v1/warmup/strict", "", "synthetic-key", nil); status != 400 || calls.Load() != 1 {
		t.Fatalf("strict dispatched partial batch: %d calls=%d", status, calls.Load())
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	var force application.PublicWarmupSummary
	if status != 200 || json.Unmarshal([]byte(body), &force) != nil || force.TotalAccounts != 2 || len(force.Submitted) != 2 || len(force.Skipped) != 0 || calls.Load() != 3 {
		t.Fatalf("force = %d %s calls=%d", status, body, calls.Load())
	}
	group.AccountIDs = []string{"second"}
	if err := store.SaveGroup(ctx, group, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force/", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &force) != nil || force.TotalAccounts != 1 || len(force.Submitted) != 1 || calls.Load() != 4 {
		t.Fatalf("group scope change ignored: %d %s calls=%d", status, body, calls.Load())
	}
	accountsMu.Lock()
	gotAccounts := append([]string(nil), accounts...)
	accountsMu.Unlock()
	if len(gotAccounts) != 4 || gotAccounts[3] != "second" {
		t.Fatalf("upstream account targets = %v", gotAccounts)
	}
	blocked, err := store.GetAccount(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	blocked.Status = domain.AccountPaused
	if err := store.SaveAccount(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &force) != nil || force.TotalAccounts != 0 || calls.Load() != 4 {
		t.Fatalf("force bypassed operator pause: %d %s calls=%d", status, body, calls.Load())
	}
	blocked.Status = domain.AccountActive
	blocked.RequiresEgressDecision = true
	if err := store.SaveAccount(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &force) != nil || force.TotalAccounts != 0 || calls.Load() != 4 {
		t.Fatalf("force bypassed egress decision: %d %s calls=%d", status, body, calls.Load())
	}
}
