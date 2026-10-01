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
	"codex-lb/internal/domain"
)

type panicAfterCompact struct {
	application.CodexOperationProvider
}

func (p panicAfterCompact) Compact(ctx context.Context, target application.CodexOperationTarget, body json.RawMessage) (application.CodexOperationResult, error) {
	_, _ = p.CodexOperationProvider.Compact(ctx, target, body)
	panic("synthetic-private-provider-panic")
}

func TestCompactUncertainUsageIsHeldWithoutSuccessOrReprobe(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status         int
		trigger        bool
	}{
		{"missing standalone", `{"id":"not-accounted","object":"response.compact","output":[]}`, 200, false},
		{"missing trigger", `{"id":"not-accounted","object":"response.compact","output":[{"type":"compaction","encrypted_content":"summary"}]}`, 200, true},
		{"partial counts", `{"object":"response.compact","usage":{"input_tokens":1}}`, 200, false},
		{"invalid counts", `{"object":"response.compact","usage":{"input_tokens":1,"output_tokens":-1}}`, 200, false},
		{"overflow", `{"object":"response.compact","usage":{"input_tokens":18446744073709551615,"output_tokens":1}}`, 200, false},
		{"malformed JSON", `{"object":`, 200, false},
		{"server error", `{"error":{"code":"server_error"}}`, 503, false},
		{"timeout status", `{"error":{"code":"request_timeout"}}`, 408, false},
		{"disconnect", "", 0, false},
		{"adapter panic", `{"object":"response.compact","usage":{"input_tokens":2,"output_tokens":1}}`, 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if test.status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					} else {
						t.Error(err)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			}))
			defer upstream.Close()
			responses, store, proxy := wireFixtureWithProxy(t, nil)
			ctx := context.Background()
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.Limits[0].MaxValue = 1
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			var operationProvider application.CodexOperationProvider = adapter
			if test.name == "adapter panic" {
				operationProvider = panicAfterCompact{adapter}
			}
			operations := application.NewCodexOperations(store, store, operationProvider, time.Hour)
			operations.ConfigureAdmission(proxy)
			operations.ConfigureAccountSelection(proxy)
			proxy.ConfigureCompaction(operations)
			mux := http.NewServeMux()
			httpapi.RegisterCodexOperationRoutes(mux, store, operations)
			standalone := httptest.NewServer(mux)
			defer standalone.Close()
			url, input := standalone.URL+"/v1/responses/compact/", `[{"role":"user","content":"summarize"}]`
			if test.trigger {
				url, input = responses.URL+"/backend-api/codex/responses", `[{"role":"user","content":"summarize"},{"type":"compaction_trigger"}]`
			}
			body := `{"model":"gpt-6-sol","input":` + input + `,"stream":true}`
			status, response := liteHTTPPost(t, url, body)
			if status < 400 || strings.Contains(response, "response.completed") || strings.Contains(response, "synthetic-private-provider-panic") || calls.Load() != 1 {
				t.Fatalf("unknown usage was treated as success/retry: %d calls=%d %s", status, calls.Load(), response)
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || len(pending) != 1 || pending[0].Status != "reserved" || !pending[0].NeedsReconciliation {
				t.Fatalf("missing durable reconciliation flag: %+v %v", pending, err)
			}
			if n, err := store.ReleaseStaleReservations(ctx, time.Now().Add(24*time.Hour)); err != nil || n != 0 {
				t.Fatalf("stale cleanup forgave usage: %d %v", n, err)
			}
			if status, response := liteHTTPPost(t, url, body); status != 429 || calls.Load() != 1 {
				t.Fatalf("held key budget was bypassed: %d calls=%d %s", status, calls.Load(), response)
			}
			totals, err := store.UsageTotals(ctx, key.ID, "")
			if err != nil || totals.RequestCount != 0 || totals.Usage != (domain.UsageAmount{}) {
				t.Fatalf("unknown usage was fabricated as a settled report: %+v %v", totals, err)
			}
			if _, err := store.GetContinuation(ctx, key.ID, "not-accounted", time.Now()); err == nil {
				t.Fatal("unknown-usage compact published a new continuation")
			}
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil || account.Status != domain.AccountActive {
				t.Fatal("uncertain operation changed quota health")
			}
		})
	}
}

func TestCompactExplicitZeroAndValidErrorUsageAreNotUncertain(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status         int
		input, output  int64
	}{
		{"explicit zero", `{"id":"known-zero","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":0,"output_tokens":0}}`, 200, 0, 0},
		{"definitive rejection", `{"error":{"code":"invalid_request"}}`, 400, 0, 0},
		{"charged failed operation", `{"error":{"code":"invalid_request"},"usage":{"input_tokens":2,"output_tokens":1}}`, 400, 2, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			operations := application.NewCodexOperations(store, store, adapter, time.Hour)
			operations.ConfigureAdmission(proxy)
			operations.ConfigureAccountSelection(proxy)
			mux := http.NewServeMux()
			httpapi.RegisterCodexOperationRoutes(mux, store, operations)
			server := httptest.NewServer(mux)
			defer server.Close()
			status, response := liteHTTPPost(t, server.URL+"/backend-api/codex/responses/compact", `{"model":"gpt-6-sol","input":"summarize"}`)
			if status != test.status {
				t.Fatalf("known usage status changed: %d %s", status, response)
			}
			ctx := context.Background()
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || len(pending) != 0 {
				t.Fatal("confirmed usage unnecessarily held")
			}
			totals, err := store.UsageTotals(ctx, "wire-key", "")
			if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != test.input || totals.Usage.OutputTokens != test.output {
				t.Fatalf("known usage was not settled once: %+v %v", totals, err)
			}
		})
	}
}
