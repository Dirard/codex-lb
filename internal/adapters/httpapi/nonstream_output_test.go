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

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
)

func TestSubscriptionNonstreamOutputAtPublicRoutes(t *testing.T) {
	const message = `{"type":"message","id":"message_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic reply"}]}`
	const tool = `{"type":"function_call","id":"tool_1","call_id":"call_1","name":"lookup","arguments":"{\"value\":7}","status":"completed"}`
	for _, path := range []string{"/v1/responses", "/v1/chat/completions/"} {
		for _, output := range []string{message, tool} {
			t.Run(path+output[:26], func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", output)
					fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_kept","model":"gpt-6-luna","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},"future_metadata":{"kept":true}}}`+"\n\n")
				}))
				defer upstream.Close()
				var adapter *provider.Adapter
				responses, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
					return adapter.Respond(ctx, target, body, emit)
				}))
				adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
				defer adapter.Close()
				mux := http.NewServeMux()
				native := application.NewNativeAPIService(store, adapter, adapter, proxy)
				native.ConfigureAdmission(proxy)
				httpapi.RegisterNativeAPIRoutes(mux, store, native, nil)
				mux.Handle("/", responses.Config.Handler)
				server := httptest.NewServer(mux)
				defer server.Close()
				body := `{"model":"gpt-6-luna","input":"hello","stream":false}`
				if strings.Contains(path, "chat/completions") {
					body = `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"}],"stream":false}`
				}
				status, reply := liteHTTPPost(t, server.URL+path, body)
				want := "synthetic reply"
				if output == tool {
					want = `"name":"lookup"`
				}
				if status != 200 || !strings.Contains(string(reply), want) || calls.Load() != 1 {
					t.Fatalf("nonstream output lost: status=%d calls=%d body=%s", status, calls.Load(), reply)
				}
				if path == "/v1/responses" && !strings.Contains(string(reply), `"future_metadata":{"kept":true}`) {
					t.Fatal("terminal metadata lost")
				}
				totals, err := store.UsageTotals(context.Background(), "wire-key", "")
				if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 2 || totals.Usage.OutputTokens != 1 {
					t.Fatalf("settlement changed: %+v %v", totals, err)
				}
				pending, err := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
				if err != nil || len(pending) != 0 {
					t.Fatal("known usage left an uncertain reservation")
				}
			})
		}
	}
}

func TestSubscriptionNonstreamCollectionFailureAccounting(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprintf("known=%v", known), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"already generated"}]}}`+"\n\n")
				if known {
					fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_billed","output":"invalid","usage":{"input_tokens":2,"output_tokens":1}}}`+"\n\n")
				}
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			status, _ := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-luna","input":"hello","stream":false}`)
			if status < 400 || calls.Load() != 1 {
				t.Fatal("incomplete/invalid output succeeded or retried")
			}
			ctx := context.Background()
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			want := 1
			if known {
				want = 0
			}
			if err != nil || len(pending) != want {
				t.Fatalf("wrong uncertainty after assembly failure: %d %v", len(pending), err)
			}
			if known {
				totals, err := store.UsageTotals(ctx, "wire-key", "")
				if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 2 || totals.Usage.OutputTokens != 1 {
					t.Fatal("known usage was lost after assembly failure")
				}
			}
		})
	}
}
