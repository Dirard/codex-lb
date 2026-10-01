package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/streambuffer"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
	"github.com/coder/websocket"
)

// Exercise the actual provider/parser -> prelude -> client route -> ledger path.
func TestLargeResponsePreludeHTTPAndWebSocket(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-luna"} {
		for _, transport := range []string{"http", "websocket"} {
			t.Run(model+"/"+transport, func(t *testing.T) {
				var calls atomic.Int32
				events := largePreludeEvents("response-owned")
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if transport == "websocket" {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						conn.SetReadLimit(1 << 20)
						if _, _, err := conn.Read(r.Context()); err != nil {
							return
						}
						for _, event := range events {
							if err := conn.Write(r.Context(), websocket.MessageText, event.Data); err != nil {
								return
							}
						}
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range events {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", event.Data)
					}
				}))
				defer upstream.Close()
				var adapter *provider.Adapter
				server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
					return adapter.Respond(ctx, target, body, emit)
				}))
				vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
				if err != nil {
					t.Fatal(err)
				}
				opened, closed := atomic.Int32{}, atomic.Int32{}
				directory := t.TempDir()
				proxy.OpenResponsePrelude = func() (application.ResponsePrelude, error) {
					buffer, err := streambuffer.New(directory, vault)
					if err != nil {
						return nil, err
					}
					opened.Add(1)
					return &countedPrelude{ResponsePrelude: buffer, closed: &closed}, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				settings, err := store.LoadSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				settings.UpstreamStreamTransport = transport
				if err := store.SaveSettings(ctx, settings); err != nil {
					t.Fatal(err)
				}
				prices := application.NewModelPricingService(store, nil)
				if _, err := prices.Save(ctx, model, pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 4000000}}); err != nil {
					t.Fatal(err)
				}
				proxy.ResolvePrice = func(ctx context.Context, _ domain.Account, model string) (pricing.Price, error) {
					return prices.ResolveCodex(ctx, model)
				}
				adapter = provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
				defer adapter.Close()
				var received []application.ResponseEvent
				body, _ := json.Marshal(map[string]any{"model": model, "stream": true, "input": "synthetic prompt", "instructions": strings.Repeat("x", 140<<10)})
				if transport == "websocket" {
					conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
					if err != nil {
						t.Fatal(err)
					}
					defer conn.CloseNow()
					conn.SetReadLimit(1 << 20)
					var object map[string]json.RawMessage
					_ = json.Unmarshal(body, &object)
					object["type"] = json.RawMessage(`"response.create"`)
					body, _ = json.Marshal(object)
					if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
						t.Fatal(err)
					}
					for range events {
						_, data, err := conn.Read(ctx)
						if err != nil {
							t.Fatal(err)
						}
						var shape struct {
							Type string `json:"type"`
						}
						if err := json.Unmarshal(data, &shape); err != nil {
							t.Fatal(err)
						}
						received = append(received, application.ResponseEvent{Type: shape.Type, Data: data})
					}
				} else {
					request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/backend-api/codex/responses", strings.NewReader(string(body)))
					request.Header.Set("Authorization", "Bearer synthetic-key")
					response, err := server.Client().Do(request)
					if err != nil {
						t.Fatal(err)
					}
					data, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					if readErr != nil || response.StatusCode != 200 {
						t.Fatalf("response status=%d read=%v", response.StatusCode, readErr)
					}
					for _, line := range strings.Split(string(data), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						var shape struct {
							Type string `json:"type"`
						}
						data := []byte(strings.TrimPrefix(line, "data: "))
						if json.Unmarshal(data, &shape) != nil {
							t.Fatal("invalid SSE")
						}
						received = append(received, application.ResponseEvent{Type: shape.Type, Data: data})
					}
				}
				if len(received) != len(events) {
					t.Fatalf("events lost: %d", len(received))
				}
				for i := range events {
					if received[i].Type != events[i].Type || string(received[i].Data) != string(events[i].Data) {
						t.Fatalf("event %d changed", i)
					}
				}
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil || key.Limits[0].CurrentValue != 12 {
					t.Fatal("actual usage did not replace reservation", err)
				}
				pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
				if err != nil || len(pending) != 0 || calls.Load() != 1 || opened.Load() != 1 || closed.Load() != 1 {
					t.Fatalf("leaked accounting/storage or retried: pending=%d calls=%d open=%d close=%d err=%v", len(pending), calls.Load(), opened.Load(), closed.Load(), err)
				}
			})
		}
	}
}

type countedPrelude struct {
	application.ResponsePrelude
	closed *atomic.Int32
}

func (p *countedPrelude) Close() error { p.closed.Add(1); return p.ResponsePrelude.Close() }

func largePreludeEvents(id string) []application.ResponseEvent {
	events := []application.ResponseEvent{}
	for _, kind := range []string{"response.created", "response.in_progress"} {
		data, _ := json.Marshal(map[string]any{"type": kind, "response": map[string]any{"id": id, "status": "in_progress", "instructions": strings.Repeat("synthetic context", 8192)}})
		events = append(events, application.ResponseEvent{Type: kind, Data: data})
	}
	data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
	return append(events, application.ResponseEvent{Type: "response.completed", Data: data})
}
