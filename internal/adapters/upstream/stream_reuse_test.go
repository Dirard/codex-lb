package upstream

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const reuseTerminal = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

const reuseChatDone = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"

// streamServer writes body, flushes, then keeps the HTTP/1.1 response open
// for tail (or until the client goes away when tail is negative).
func streamServer(t *testing.T, body string, tail time.Duration, connections *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
		w.(http.Flusher).Flush()
		if tail < 0 {
			<-r.Context().Done()
			return
		}
		time.Sleep(tail)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// NET-10: the body tail after a terminal is drained so HTTP/1.1 keep-alive
// connections are reused instead of paying TCP+TLS on every request.
func TestHTTP1StreamReusesConnectionAfterTerminal(t *testing.T) {
	for name, test := range map[string]struct {
		body         string
		capabilities Capabilities
	}{
		"responses": {reuseTerminal, ResponsesCapabilities()},
		"chat":      {reuseChatDone, ZAIChatCompletionsCapabilities()},
	} {
		t.Run(name, func(t *testing.T) {
			var connections atomic.Int64
			server := streamServer(t, test.body, 5*time.Millisecond, &connections)
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			for range 20 {
				if _, err := adapter.OpenStream(context.Background(), testTarget(server.URL, test.capabilities), Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if connections.Load() != 1 {
				t.Fatalf("HTTP/1.1 connections=%d, want 1", connections.Load())
			}
		})
	}
}

// NET-10: an upstream that never ends the body after the terminal is cut at
// the drain bound without changing the result.
func TestStreamTerminalDoesNotWaitForEndlessBody(t *testing.T) {
	var connections atomic.Int64
	server := streamServer(t, reuseTerminal, -1, &connections)
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	started := time.Now()
	result, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ResponsesCapabilities()), Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil })
	if err != nil || result.ResponseID != "r" || !result.UsageKnown {
		t.Fatalf("terminal result changed: %+v %v", result, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("endless body held the stream for %v", elapsed)
	}
}
