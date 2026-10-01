package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
)

func TestTerminalDeliveryFailureKeepsConfirmedUsage(t *testing.T) {
	for _, transport := range []string{"sse", "websocket", "translated_chat"} {
		t.Run(transport, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if transport == "websocket" {
					connection, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer connection.CloseNow()
					if _, _, err := connection.Read(r.Context()); err != nil {
						return
					}
					_ = connection.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_ws","status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if transport == "translated_chat" {
					_, _ = io.WriteString(w, "data: "+`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\n")
					_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`+"\n\n")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_sse","status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`+"\n\n")
			}))
			defer upstream.Close()
			capabilities := ResponsesCapabilities()
			if transport == "websocket" {
				capabilities.StreamTransport = TransportWebSocket
			} else if transport == "translated_chat" {
				capabilities = ZAIChatCompletionsCapabilities()
			}
			adapter := New(upstream.Client(), nil)
			defer adapter.Close()
			failedDelivery := errors.New("synthetic delivery failure")
			result, err := adapter.OpenStream(context.Background(), testTarget(upstream.URL, capabilities), Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}, func(event Event) error {
				if event.Type == "response.completed" || transport == "translated_chat" && event.Type == "response.output_text.delta" {
					return failedDelivery
				}
				return nil
			})
			if err == nil || !result.UsageKnown || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 {
				t.Fatalf("known usage lost on delivery failure: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestErrorFrameKeepsConfirmedUsage(t *testing.T) {
	for _, transport := range []string{"sse", "websocket", "translated_chat"} {
		t.Run(transport, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if transport == "websocket" {
					connection, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer connection.CloseNow()
					if _, _, err := connection.Read(r.Context()); err != nil {
						return
					}
					_ = connection.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":503,"error":{"code":"synthetic_failure"},"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if transport == "translated_chat" {
					_, _ = io.WriteString(w, "data: "+`{"choices":[],"error":{"code":"synthetic_failure","message":"failed"},"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: "+`{"type":"error","status":503,"error":{"code":"synthetic_failure"},"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`+"\n\n")
			}))
			defer upstream.Close()
			capabilities := ResponsesCapabilities()
			if transport == "websocket" {
				capabilities.StreamTransport = TransportWebSocket
			} else if transport == "translated_chat" {
				capabilities = ZAIChatCompletionsCapabilities()
			}
			adapter := New(upstream.Client(), nil)
			defer adapter.Close()
			result, err := adapter.OpenStream(context.Background(), testTarget(upstream.URL, capabilities), Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}, func(Event) error { return nil })
			if err == nil || !result.UsageKnown || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 {
				t.Fatalf("charged error usage lost: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestTranslatedChatStreamRetainsQuotaRefusalCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"choices":[],"error":{"code":"usage_limit_reached","message":"quota"}}`+"\n\n")
	}))
	defer upstream.Close()
	adapter := New(upstream.Client(), nil)
	defer adapter.Close()
	_, err := adapter.OpenStream(context.Background(), testTarget(upstream.URL, ZAIChatCompletionsCapabilities()), Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}, func(Event) error { return nil })
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeInsufficientQuota {
		t.Fatalf("translated Chat quota proof lost: %v", err)
	}
}
