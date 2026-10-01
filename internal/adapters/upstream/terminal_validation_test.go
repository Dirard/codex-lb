package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
)

func TestInvalidTerminalIsNotEmittedAsSuccessfulCompletion(t *testing.T) {
	for _, transport := range []StreamTransport{TransportHTTP, TransportWebSocket} {
		for _, raw := range []string{
			`{"type":"response.completed","response":{"id":"r","status":"completed"}}`,
			`{"type":"response.completed","response":{"id":"r","status":"failed","usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}`,
		} {
			t.Run(string(transport), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == TransportHTTP {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: " + raw + "\n\n"))
						return
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer conn.CloseNow()
					if _, _, err := conn.Read(r.Context()); err != nil {
						return
					}
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(raw))
					_, _, _ = conn.Read(r.Context())
				}))
				defer server.Close()
				adapter := New(server.Client(), nil)
				defer adapter.Close()
				caps := ResponsesCapabilities()
				caps.StreamTransport = transport
				emitted := 0
				_, err := adapter.OpenStream(context.Background(), Target{ProviderID: "p", AccountID: "a", KeyID: "k", BaseURL: server.URL, Capabilities: caps}, Request{Body: json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`)}, func(Event) error { emitted++; return nil })
				var failure *Error
				if !errors.As(err, &failure) || emitted != 0 {
					t.Fatalf("invalid terminal emitted: count=%d err=%v", emitted, err)
				}
			})
		}
	}
}
