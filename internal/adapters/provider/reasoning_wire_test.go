package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestExternalReasoningWireAlias(t *testing.T) {
	for _, effort := range []string{"ultra", "minimal"} {
		for _, mode := range []string{"responses", "responses-stream", "chat", "chat-stream", "native", "native-stream"} {
			t.Run(mode+"/"+effort, func(t *testing.T) {
				wireEffort := "max"
				if effort == "minimal" {
					wireEffort = effort
				}
				stream := strings.HasSuffix(mode, "-stream")
				responses := strings.HasPrefix(mode, "responses")
				native := strings.HasPrefix(mode, "native")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid upstream JSON")
					}
					if responses {
						if string(body["reasoning"]) != fmt.Sprintf(`{"effort":%q,"summary":"auto"}`, wireEffort) {
							t.Errorf("Responses effort was not aliased: %s", body["reasoning"])
						}
					} else if string(body["reasoning_effort"]) != fmt.Sprintf(`%q`, wireEffort) {
						t.Errorf("Chat effort was not aliased: %s", body["reasoning_effort"])
					}
					if native && string(body["keep"]) != `{"effort":"ultra"}` {
						t.Error("unrelated native field was changed")
					}
					if responses {
						payload := `{"id":"resp_wire","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
						} else {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, payload)
						}
					} else if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"chat_wire","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
					}
				}))
				defer server.Close()
				source, credential := zaiSource(server.URL + "/v1")
				source.Kind, source.Responses = domain.ModelSourceOpenAICompatible, responses
				source.Models[0].RawMetadataJSON = `{"supports_reasoning":true,"supported_reasoning_levels":["max"]}`
				if stream {
					source.Models[0].RawMetadataJSON = `{"supports_reasoning":true,"supported_reasoning_levels":["ultra"]}`
				}
				if effort == "minimal" {
					source.Models[0].RawMetadataJSON = `{"supports_reasoning":true,"supported_reasoning_levels":["minimal"]}`
				}
				adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
				defer adapter.Close()
				body := mustJSON(map[string]any{"model": "gpt-5.6-sol", "input": "hello", "stream": stream, "reasoning": map[string]string{"effort": effort, "summary": "auto"}})
				target := application.ResponseTarget{Account: source.Account(), KeyID: "key"}
				var err error
				if native {
					body = mustJSON(map[string]any{"model": "gpt-5.6-sol", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "stream": stream, "reasoning_effort": effort, "keep": map[string]string{"effort": "ultra"}})
					_, err = adapter.NativeChat(context.Background(), target, body, stream, func(application.NativeAPIEvent) error { return nil })
				} else {
					_, err = adapter.Respond(context.Background(), target, body, func(application.ResponseEvent) error { return nil })
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), effort) || strings.Contains(string(body), `"max"`) {
					t.Fatal("caller request was overwritten with wire alias")
				}
			})
		}
	}
}

func TestSubscriptionMinimalUsesWireFallbackOnResponsesAndCompact(t *testing.T) {
	for _, mode := range []string{"http", "websocket", "compact"} {
		for _, fallback := range []string{"", "medium"} {
			t.Run(mode+"/"+fallback, func(t *testing.T) {
				want := fallback
				if want == "" {
					want = "low"
				}
				check := func(body []byte) {
					var request struct {
						Reasoning map[string]string `json:"reasoning"`
					}
					if json.Unmarshal(body, &request) != nil || request.Reasoning["effort"] != want || request.Reasoning["summary"] != "auto" {
						t.Errorf("subscription effort/summary mismatch: %s", body)
					}
				}
				terminal := []byte(`{"type":"response.completed","response":{"id":"minimal","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "websocket" {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						_, body, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						check(body)
						_ = conn.Write(r.Context(), websocket.MessageText, terminal)
						return
					}
					var body json.RawMessage
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid upstream JSON")
					}
					check(body)
					if mode == "compact" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":0,"output_tokens":0}}}`+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", terminal)
				}))
				defer server.Close()
				config := Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL}
				if fallback != "" {
					config.ChatGPTReasoningFallback = func(model string) string {
						if model != "gpt-6-sol" {
							t.Errorf("wrong catalog model: %s", model)
						}
						return fallback
					}
				}
				adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokenSource{}, testCipher{}, config)
				defer adapter.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				body := []byte(`{"model":"gpt-6-sol","input":"hello","reasoning":{"effort":"minimal","summary":"auto"}}`)
				account := domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}
				var err error
				if mode == "compact" {
					_, err = adapter.Compact(ctx, application.CodexOperationTarget{Account: account, KeyID: "key"}, body)
				} else {
					_, err = adapter.Respond(ctx, application.ResponseTarget{Account: account, KeyID: "key", UseWebSocket: mode == "websocket"}, body, nil)
				}
				if err != nil || !strings.Contains(string(body), `"effort":"minimal"`) {
					t.Fatalf("minimal fallback failed or mutated caller: %s %v", body, err)
				}
			})
		}
	}
}
