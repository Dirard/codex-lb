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

func TestSubscriptionCodexMetadataIsRequestScoped(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			type observed struct {
				metadata map[string]json.RawMessage
				headers  http.Header
			}
			seen := make(chan observed, 3)
			var handshakes, calls atomic.Int64
			completed := func(body []byte, header http.Header) []byte {
				var request struct {
					Metadata map[string]json.RawMessage `json:"client_metadata"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
				}
				seen <- observed{request.Metadata, header.Clone()}
				return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"metadata_%d","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, calls.Add(1)))
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					handshakes.Add(1)
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer conn.CloseNow()
					for {
						_, body, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						if err := conn.Write(r.Context(), websocket.MessageText, completed(body, r.Header)); err != nil {
							return
						}
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", completed(body, r.Header))
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			settings, err := store.LoadSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			settings.UpstreamStreamTransport = transport
			if err := store.SaveSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			for turn := 0; turn < 3; turn++ {
				metadata := `{"custom":7}`
				if turn == 1 {
					metadata = `{"custom":7,"x-codex-window-id":"body-window"}`
				}
				req, _ := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true,"client_metadata":`+metadata+`}`))
				req.Header.Set("Authorization", "Bearer synthetic-key")
				req.Header.Set("Session-Id", "reused-metadata-session")
				req.Header.Set("X-Unrelated-Secret", "must-not-forward")
				if turn < 2 {
					req.Header.Set("X-CODEX-TURN-METADATA", fmt.Sprintf("turn-%d", turn))
					req.Header.Set("x-codex-window-ID", fmt.Sprintf("header-window-%d", turn))
				}
				if turn == 1 {
					req.Header.Set("x-OPENAI-subagent", "child")
					req.Header.Set("x-codex-PARENT-thread-id", "parent")
				}
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 || !strings.Contains(string(body), "response.completed") {
					t.Fatalf("request failed: %d %v %.200s", res.StatusCode, err, body)
				}
				got := <-seen
				if got.headers.Get("X-Unrelated-Secret") != "" || string(got.metadata["custom"]) != "7" {
					t.Fatal("arbitrary header forwarded or body metadata lost")
				}
				if got.headers.Get("Session_id") == "" {
					t.Fatal("provider's scoped session header was lost in transport")
				}
				for _, name := range []string{"x-codex-turn-metadata", "x-openai-subagent", "x-codex-parent-thread-id", "x-codex-window-id"} {
					want := req.Header.Get(name)
					if transport == "http" {
						if got.headers.Get(name) != want {
							t.Errorf("HTTP header %s = %q, want %q", name, got.headers.Get(name), want)
						}
						want = ""
					}
					if name == "x-codex-window-id" && turn == 1 {
						want = "body-window"
					}
					var actual string
					_ = json.Unmarshal(got.metadata[name], &actual)
					if actual != want {
						t.Errorf("turn %d metadata %s = %q, want %q", turn, name, actual, want)
					}
				}
			}
			if calls.Load() != 3 || transport == "websocket" && handshakes.Load() != 1 || transport == "http" && handshakes.Load() != 0 {
				t.Fatalf("unexpected calls/sockets: %d/%d", calls.Load(), handshakes.Load())
			}
		})
	}
}
