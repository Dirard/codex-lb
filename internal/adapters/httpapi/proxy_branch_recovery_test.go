package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func TestCodexBranchWithAdvancedAnchorUsesOnlySafeSameOwnerReplay(t *testing.T) {
	for _, unansweredTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "safe_history", true: "unanswered_tool"}[unansweredTool], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer connection.CloseNow()
				for {
					_, body, err := connection.Read(ctx)
					if err != nil {
						return
					}
					var request struct {
						Previous string          `json:"previous_response_id"`
						Input    json.RawMessage `json:"input"`
					}
					if json.Unmarshal(body, &request) != nil {
						t.Error("invalid upstream request")
						return
					}
					attempt := calls.Add(1)
					if attempt == 2 && request.Previous != "seed-response" {
						t.Error("sequential continuation lost its anchor")
						return
					}
					if attempt > 2 {
						if unansweredTool || attempt != 3 || request.Previous != "" {
							t.Error("stale or unsafe branch was dispatched")
							return
						}
						input := string(request.Input)
						if !strings.Contains(input, "seed-input") || !strings.Contains(input, "seed-answer") || !strings.Contains(input, "branch-input") || strings.Contains(input, "advance-input") || strings.Contains(input, "advance-answer") {
							t.Error("branch replay mixed sibling history")
							return
						}
					}
					name := map[int64]string{1: "seed", 2: "advance", 3: "branch"}[attempt]
					output := fmt.Sprintf(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]`, name+"-answer")
					if attempt == 1 && unansweredTool {
						output = `[{"type":"function_call","name":"unanswered","call_id":"call_synthetic","arguments":"{}"}]`
					}
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":%s,"usage":{"input_tokens":10,"output_tokens":10,"total_tokens":20}}}`, name+"-response", output)
					if connection.Write(ctx, websocket.MessageText, []byte(event)) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.UpstreamStreamTransport = "websocket"
			settings.HTTPTransportPolicy = "always_websocket"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			request := func(input, previous string) (int, string) {
				t.Helper()
				payload := map[string]any{"model": "gpt-6-sol", "input": input, "stream": true}
				if previous != "" {
					payload["previous_response_id"] = previous
				}
				encoded, _ := json.Marshal(payload)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(string(encoded)))
				req.Header.Set("Authorization", "Bearer synthetic-key")
				req.Header.Set("Session_id", "shared-client")
				req.Header.Set("Thread-Id", "shared-parent")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, string(body)
			}
			if status, body := request("seed-input", ""); status != 200 || !strings.Contains(body, "response.completed") {
				t.Fatalf("seed failed: %d %s", status, body)
			}
			if status, body := request("advance-input", "seed-response"); status != 200 || !strings.Contains(body, "response.completed") {
				t.Fatalf("advance failed: %d %s", status, body)
			}
			status, body := request("branch-input", "seed-response")
			wantCalls := int64(3)
			if unansweredTool {
				wantCalls = 2
				if status == 200 || !strings.Contains(body, "continuation_not_found") {
					t.Fatalf("unsafe continuation did not fail closed: %d %s", status, body)
				}
			} else {
				if status != 200 || !strings.Contains(body, "branch-response") || strings.Contains(body, "continuation_not_found") {
					t.Fatalf("safe branch was not recovered: %d %s", status, body)
				}
				owner, err := store.GetContinuation(ctx, "wire-key", "branch-response", time.Now())
				if err != nil || owner.AccountID != "wire-account" {
					t.Fatal("replay changed the account owner", err)
				}
			}
			if calls.Load() != wantCalls {
				t.Fatalf("unexpected provider attempts: %d want %d", calls.Load(), wantCalls)
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil || key.Limits[0].CurrentValue != wantCalls*20 {
				t.Fatalf("reservation settled incorrectly: %+v %v", key.Limits, err)
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("pre-dispatch recovery kept unknown usage: %d %v", len(pending), err)
			}
		})
	}
}
