package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestRefusalBillingReportSurvivesEveryResponsesTransport(t *testing.T) {
	for _, wire := range []struct {
		name                    string
		chat, external, ws, sse bool
		terminal                bool
		status                  int
	}{
		{name: "http rejection", status: 429},
		{name: "sse error", sse: true},
		{name: "sse terminal", sse: true, terminal: true},
		{name: "websocket handshake", ws: true, status: 429},
		{name: "websocket error", ws: true},
		{name: "websocket terminal", ws: true, terminal: true},
		{name: "external responses", external: true, terminal: true},
		{name: "translated chat json", external: true, chat: true},
		{name: "translated chat stream", external: true, chat: true, sse: true},
		{name: "translated chat rejection", external: true, chat: true, sse: true, status: 429},
	} {
		for _, report := range []string{"partial", "invalid", "malformed", "overflow"} {
			t.Run(wire.name+"/"+report, func(t *testing.T) {
				input, output := "input_tokens", "output_tokens"
				if wire.chat {
					input, output = "prompt_tokens", "completion_tokens"
				}
				usage := fmt.Sprintf(`{%q:2}`, input)
				switch report {
				case "invalid":
					usage = fmt.Sprintf(`{%q:-1,%q:0}`, input, output)
				case "malformed":
					usage = `"invalid"`
				case "overflow":
					usage = fmt.Sprintf(`{%q:9223372036854775808,%q:0}`, input, output)
				}
				payload := fmt.Sprintf(`{"type":"error","status":429,"error":{"code":"usage_limit_reached"},"usage":%s}`, usage)
				if wire.terminal {
					payload = fmt.Sprintf(`{"id":"refused","status":"failed","error":{"code":"usage_limit_reached"},"usage":%s}`, usage)
					if wire.ws || wire.sse {
						payload = `{"type":"response.failed","response":` + payload + `}`
					}
				}
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if wire.status != 0 {
						w.WriteHeader(wire.status)
						fmt.Fprint(w, payload)
						return
					}
					if wire.ws {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						if _, _, err := conn.Read(r.Context()); err == nil {
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(payload))
						}
						return
					}
					if wire.sse {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: "+payload+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, payload)
				}))
				defer server.Close()
				store := &sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}
				target := application.ResponseTarget{KeyID: "key", UseWebSocket: wire.ws, Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}}
				if wire.external {
					store.source, store.credential = zaiSource(server.URL)
					store.source.Chat, store.source.Responses = wire.chat, !wire.chat
					target.Account = store.source.Account()
				}
				adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
				defer adapter.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"hello","stream":%t}`, wire.sse || wire.ws))
				result, err := adapter.Respond(ctx, target, body, func(application.ResponseEvent) error { return nil })
				var failure *application.ProviderFailure
				quota := errors.As(err, &failure) && failure.QuotaRefused || result.Failed && result.ErrorCode == "insufficient_quota"
				if err == nil || !result.UsageReported || result.UsageKnown || !quota || calls.Load() != 1 {
					t.Fatalf("billing report or quota proof lost: result=%+v err=%v calls=%d", result, err, calls.Load())
				}
			})
		}
	}
}
