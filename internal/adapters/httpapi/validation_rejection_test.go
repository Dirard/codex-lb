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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestDefinitiveValidationRejectionReleasesOnlyUnusedReserve(t *testing.T) {
	for _, tc := range []struct {
		name, billing string
		stream, known bool
	}{
		{"HTTP validation", "", false, true},
		{"partial billing", `,"usage":{"input_tokens":4}`, false, false},
		{"reported output", `,"output":[{"type":"message","text":"already generated"}]`, false, false},
		{"reported duration", `,"duration":4`, false, false},
		{"in-stream refusal", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					body := `{"error":{"type":"invalid_request_error","code":"unknown_parameter"}` + tc.billing + `}`
					if tc.stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"type\":\"error\",\"status\":400,%s\n\n", strings.TrimPrefix(body, "{"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(400)
						fmt.Fprint(w, body)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"next","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`+"\n\n")
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			status, _ := liteHTTPPost(t, server.URL+"/backend-api/codex/responses", `{"model":"gpt-6-luna","input":"hi","stream":true}`)
			if status != 400 || calls.Load() != 1 {
				t.Fatalf("refusal hidden or repeated: status=%d calls=%d", status, calls.Load())
			}
			ctx := context.Background()
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			wantPending := 1
			if tc.known {
				wantPending = 0
			}
			if err != nil || len(pending) != wantPending {
				t.Fatalf("rejection reserve: pending=%d want=%d err=%v", len(pending), wantPending, err)
			}
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil || account.Status != domain.AccountActive {
				t.Fatal("validation error changed account health")
			}
			if tc.known {
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil || key.Limits[0].CurrentValue != 0 {
					t.Fatal("unused reserve not released")
				}
				if status, body := liteHTTPPost(t, server.URL+"/backend-api/codex/responses", `{"model":"gpt-6-luna","input":"hi","stream":true}`); status != 200 {
					t.Fatalf("next request failed: %d %s", status, body)
				}
				totals, err := store.UsageTotals(ctx, "wire-key", "")
				if err != nil || totals.RequestCount != 2 || totals.Usage.InputTokens != 2 || totals.Usage.OutputTokens != 1 {
					t.Fatalf("incorrect settlement: %+v %v", totals, err)
				}
			}
		})
	}
}
