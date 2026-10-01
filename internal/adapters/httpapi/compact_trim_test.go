package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
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

func TestCompactRoutesTrimBeforeFileOwnershipAndAccounting(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "[compact trim]") || strings.Contains(string(body), "old-file") || !strings.Contains(string(body), "latest instruction") {
			t.Error("upstream did not receive the prepared history")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	responses, store, proxy := wireFixtureWithProxy(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		t.Error("compact trigger reached the Responses provider")
		return application.ResponseResult{}, nil
	}))
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	standalone := httptest.NewServer(mux)
	defer standalone.Close()
	old := `{"role":"assistant","content":[{"type":"input_text","text":"` + strings.Repeat("x", 500_000) + `"},{"type":"input_file","file_id":"old-file"}]}`
	for _, path := range []string{"/backend-api/codex/responses/compact", "/v1/responses/compact/", "/backend-api/codex/responses"} {
		base, trigger := standalone.URL, ""
		if path == "/backend-api/codex/responses" {
			base, trigger = responses.URL, `,{"type":"compaction_trigger"}`
		}
		for _, pinned := range []bool{false, true} {
			latest := `{"role":"user","content":"latest instruction"}`
			if pinned {
				latest = `{"role":"user","content":[{"type":"input_text","text":"latest instruction"},{"type":"input_file","file_id":"unknown-required-file"}]}`
			}
			before := calls.Load()
			request, _ := http.NewRequest("POST", base+path, strings.NewReader(`{"model":"gpt-6-sol","stream":true,"input":[`+old+`,`+latest+trigger+`]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-key")
			response, err := standalone.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if pinned {
				if response.StatusCode != 409 || calls.Load() != before {
					t.Fatalf("required file pin lost: %d %.200s", response.StatusCode, body)
				}
			} else if response.StatusCode != 200 || calls.Load() != before+1 {
				t.Fatalf("discarded history still pinned compact: %s %d %.200s", path, response.StatusCode, body)
			}
		}
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "wire-account")
	if err != nil || totals.RequestCount != 3 || totals.Usage.InputTokens != 6 || totals.Usage.OutputTokens != 3 {
		t.Fatalf("compact accounting: %+v %v", totals, err)
	}
}
