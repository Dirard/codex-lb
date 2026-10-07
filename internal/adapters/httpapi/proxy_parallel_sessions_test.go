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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func TestParallelCodexBranchesSharingSessionKeepOutputsAndAccounting(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			const branches = 4
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			opened := make(chan struct{}, branches)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var calls, sockets atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer connection.CloseNow()
				sockets.Add(1)
				latest := ""
				for {
					_, body, err := connection.Read(ctx)
					if err != nil {
						return
					}
					var request struct {
						Input []struct {
							Content string `json:"content"`
						} `json:"input"`
						Previous string `json:"previous_response_id"`
					}
					if json.Unmarshal(body, &request) != nil {
						t.Error("invalid upstream request")
						return
					}
					calls.Add(1)
					if len(request.Input) != 1 {
						t.Error("unexpected history replay of a live independent branch")
						return
					}
					branch := request.Input[0].Content
					if request.Previous != "" && request.Previous != latest {
						t.Error("previous_response_id routed to a sibling socket")
						return
					}
					id := branch + "-response"
					if request.Previous == "" {
						created := fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress"}}`, id)
						if connection.Write(ctx, websocket.MessageText, []byte(created)) != nil {
							return
						}
						opened <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return
						}
					}
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":10,"output_tokens":10,"total_tokens":20}}}`, id, branch)
					if connection.Write(ctx, websocket.MessageText, []byte(event)) != nil {
						return
					}
					latest = id
				}
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxyConfig(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}), application.ProxyConfig{MaxStreams: branches, MaxQueued: branches})
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.HTTPTransportPolicy = "always_websocket"
			settings.UpstreamStreamTransport = "websocket"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			defer unblock()
			run := func(branch, previous string) error {
				payload := map[string]any{"model": "gpt-6-sol", "input": branch, "stream": true}
				if previous != "" {
					payload["previous_response_id"] = previous
				}
				headers := http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"shared-client"}, "Thread-Id": {"shared-parent"}, "User-Agent": {"codex_cli_rs/0.146.0"}}
				var result []byte
				if transport == "http" {
					encoded, _ := json.Marshal(payload)
					req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(string(encoded)))
					req.Header = headers
					response, err := server.Client().Do(req)
					if err != nil {
						return err
					}
					defer response.Body.Close()
					result, err = io.ReadAll(response.Body)
					if err != nil || response.StatusCode != http.StatusOK {
						return fmt.Errorf("HTTP status=%d error=%v body=%s", response.StatusCode, err, result)
					}
				} else {
					connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: headers})
					if err != nil {
						return err
					}
					defer connection.CloseNow()
					payload["type"] = "response.create"
					encoded, _ := json.Marshal(payload)
					if err := connection.Write(ctx, websocket.MessageText, encoded); err != nil {
						return err
					}
					for {
						_, frame, err := connection.Read(ctx)
						if err != nil {
							return err
						}
						result = append(result, frame...)
						if strings.Contains(string(frame), `"type":"error"`) || strings.Contains(string(frame), `"type":"response.failed"`) {
							return fmt.Errorf("WebSocket failed: %s", frame)
						}
						if strings.Contains(string(frame), `"type":"response.completed"`) {
							break
						}
					}
				}
				if !strings.Contains(string(result), `"id":"`+branch+`-response"`) || strings.Count(string(result), `"status":"completed"`) != 1 {
					return fmt.Errorf("wrong branch result: %s", result)
				}
				return nil
			}
			finished := make(chan error, branches)
			var workers sync.WaitGroup
			workers.Add(branches)
			defer func() { cancel(); unblock(); workers.Wait() }()
			for i := range branches {
				go func() {
					defer workers.Done()
					finished <- run(fmt.Sprintf("branch-%d", i), "")
				}()
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for i := range branches {
				select {
				case <-opened:
				case err := <-finished:
					t.Fatalf("branch returned before all peers started: %v", err)
				case <-deadline.C:
					t.Fatalf("only %d/%d independent branches reached upstream", i, branches)
				}
			}
			unblock()
			for range branches {
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
			}
			for i := range branches {
				if err := run(fmt.Sprintf("follow-%d", i), fmt.Sprintf("branch-%d-response", i)); err != nil {
					t.Fatal(err)
				}
				owner, err := store.GetContinuation(ctx, "wire-key", fmt.Sprintf("follow-%d-response", i), time.Now())
				if err != nil || owner.AccountID != "wire-account" {
					t.Fatal("branch lost its account owner", err)
				}
			}
			if calls.Load() != 2*branches || sockets.Load() != branches {
				t.Fatalf("extra dispatch or failed reuse: calls=%d sockets=%d", calls.Load(), sockets.Load())
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil || key.Limits[0].CurrentValue != 2*branches*20 {
				t.Fatalf("accounting mismatch: %+v %v", key.Limits, err)
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 20)
			if err != nil || len(pending) != 0 {
				t.Fatalf("unexpected held reservations: %d %v", len(pending), err)
			}
		})
	}
}
