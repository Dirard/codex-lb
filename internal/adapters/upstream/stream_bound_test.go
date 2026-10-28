package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// NET-18: a long healthy response with more than 100 000 small events is not
// cut off after execution; streams are bounded by size and time.
func TestLongResponseWithManySmallEventsCompletes(t *testing.T) {
	const events = 150000
	t.Run("sse", func(t *testing.T) {
		adapter := New(&http.Client{Transport: memoryStreamTransport{benchmarkSSEStream(events)}}, nil)
		defer adapter.Close()
		deltas := 0
		result, err := adapter.OpenStream(context.Background(), testTarget("https://upstream.invalid", ResponsesCapabilities()), Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}, func(e Event) error {
			if e.Type == "response.output_text.delta" {
				deltas++
			}
			return nil
		})
		if err != nil || !result.UsageKnown || deltas != events {
			t.Fatalf("long response cut off: deltas=%d %+v %v", deltas, result, err)
		}
	})
	t.Run("websocket", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"long"}}`))
			for range events {
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"x"}`)); err != nil {
					return
				}
			}
			_ = conn.Write(r.Context(), websocket.MessageText, completedFrame("long"))
			<-conn.CloseRead(r.Context()).Done()
		}))
		defer server.Close()
		adapter := New(server.Client(), nil)
		defer adapter.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		deltas := 0
		result, err := adapter.OpenStream(ctx, websocketTestTarget(server.URL, "long-thread"), Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}, func(e Event) error {
			if e.Type == "response.output_text.delta" {
				deltas++
			}
			return nil
		})
		if err != nil || !result.UsageKnown || deltas != events {
			t.Fatalf("long response cut off: deltas=%d %+v %v", deltas, result, err)
		}
	})
}
