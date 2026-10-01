package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
)

func TestCompactRoutesShareKeyReasoningAndFastModePolicy(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		var body struct {
			Model     string            `json:"model"`
			Tier      string            `json:"service_tier"`
			Input     []json.RawMessage `json:"input"`
			MaxOutput json.RawMessage   `json:"max_output_tokens"`
			Reasoning map[string]string `json:"reasoning"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "gpt-6-sol" || body.Tier != "default" ||
			body.Reasoning["effort"] != "max" || body.Reasoning["summary"] != "auto" || len(body.Input) == 0 || body.MaxOutput != nil {
			t.Error("compact bypassed common key policy or scalar input normalization")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"compact_policy_%d","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, count)
	}))
	defer upstream.Close()
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		t.Error("trigger bypassed compact")
		return application.ResponseResult{}, nil
	}))
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	model, tier, effort := "gpt-6-sol", "priority", "ultra"
	key.EnforcedModel, key.ApplyToCodexModel = &model, true
	key.EnforcedServiceTier, key.EnforcedReasoningEffort = &tier, &effort
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ProhibitFastMode = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	compact := httptest.NewServer(mux)
	defer compact.Close()
	for _, path := range []string{"/v1/responses/compact", "/v1/responses/compact/", "/backend-api/codex/responses/compact", "/backend-api/codex/responses/compact/", "/backend-api/codex/responses"} {
		base, input, ignored := compact.URL, `"summarize"`, `,"max_output_tokens":0`
		if path == "/backend-api/codex/responses" {
			base, input, ignored = server.URL, `[{"role":"user","content":"summarize"},{"type":"compaction_trigger"}]`, ""
		}
		request := `{"model":"client-model","stream":true,"input":` + input + `,"reasoning":{"effort":"low","summary":"auto"}}`
		request = strings.TrimSuffix(request, "}") + ignored + "}"
		if status, response := liteHTTPPost(t, base+path, request); status != 200 {
			t.Fatalf("%s: %d %s", path, status, response)
		}
	}
	key.EnforcedReasoningEffort = nil
	key.AllowedReasoningEfforts = []string{"high"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	for _, path := range []string{"/v1/responses/compact", "/backend-api/codex/responses/compact/"} {
		status, response := liteHTTPPost(t, compact.URL+path, `{"model":"gpt-6-sol","input":"summarize","reasoning":{"effort":"low"}}`)
		if status != 403 || !strings.Contains(response, "reasoning_effort_not_allowed") || calls.Load() != before {
			t.Fatalf("compact reasoning restriction bypassed: %d %s", status, response)
		}
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 5 || totals.Usage.InputTokens != 10 || totals.Usage.OutputTokens != 5 {
		t.Fatalf("policy refusals were billed: %+v %v", totals, err)
	}
}
