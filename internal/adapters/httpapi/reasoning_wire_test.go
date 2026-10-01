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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestReasoningWireAliasPreservesKeyPolicyAndReports(t *testing.T) {
	for _, contract := range []struct{ effort, wire string }{{"ultra", "max"}, {"minimal", "medium"}} {
		t.Run(contract.effort, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := calls.Add(1)
				var body struct {
					Reasoning map[string]string `json:"reasoning"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Reasoning["effort"] != contract.wire || body.Reasoning["summary"] != "auto" {
					t.Error("upstream did not receive wire-safe effort with original summary")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_%d\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", id)
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if !strings.Contains(string(body), `"effort":"`+contract.effort+`"`) {
					t.Error("application policy was changed to wire alias")
				}
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex", ChatGPTReasoningFallback: func(string) string { return "medium" }})
			defer adapter.Close()
			ctx := context.Background()
			settings, _ := store.LoadSettings(ctx)
			settings.UpstreamStreamTransport = "http"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			key, _ := store.GetAPIKey(ctx, "wire-key")
			effort := contract.effort
			key.EnforcedReasoningEffort = &effort
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			if status, body := liteHTTPPost(t, server.URL+"/backend-api/codex/responses", `{"model":"gpt-6-sol","input":"hello","reasoning":{"effort":"low","summary":"auto"}}`); status != 200 {
				t.Fatalf("forced ultra: %d %s", status, body)
			}
			key.EnforcedReasoningEffort = nil
			key.AllowedReasoningEfforts = []string{contract.effort}
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			if status, body := liteHTTPPost(t, server.URL+"/v1/responses", fmt.Sprintf(`{"model":"gpt-6-sol","input":"hello","reasoning":{"effort":%q,"summary":"auto"}}`, contract.effort)); status != 200 {
				t.Fatalf("allowed ultra: %d %s", status, body)
			}
			if status, _ := liteHTTPPost(t, server.URL+"/v1/responses", fmt.Sprintf(`{"model":"gpt-6-sol","input":"hello","reasoning":{"effort":%q}}`, contract.wire)); status != 403 || calls.Load() != 2 {
				t.Fatal("wire equivalence bypassed client-plane key restriction")
			}
			logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
			if err != nil || len(logs.Requests) != 2 {
				t.Fatalf("request log: %+v %v", logs, err)
			}
			for _, row := range logs.Requests {
				if row.ReasoningEffort == nil || *row.ReasoningEffort != contract.effort {
					t.Fatal("report lost effective client-plane reasoning")
				}
			}
		})
	}
}
