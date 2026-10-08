package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWebSocketProbeKeepsResponsiveReasoningAndHonorsDeadline(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "completion", false: "overall-deadline"}[complete], func(t *testing.T) {
			var pings, creates atomic.Int32
			ready := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool {
					if pings.Add(1) == 3 {
						close(ready)
					}
					return true
				}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(r.Context()); err != nil {
					return
				}
				creates.Add(1)
				peer := conn.CloseRead(r.Context())
				select {
				case <-ready:
				case <-peer.Done():
					return
				}
				if complete {
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"probe-result","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
				}
				<-peer.Done()
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			adapter.websocketPingInterval, adapter.websocketPingTimeout = 15*time.Millisecond, time.Second
			capabilities := ResponsesCapabilities()
			capabilities.StreamTransport = TransportWebSocket
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			var emitted int
			result, err := adapter.OpenStream(ctx, testTarget(server.URL, capabilities), Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}, func(Event) error { emitted++; return nil })
			if complete {
				if err != nil || !result.UsageKnown || result.ResponseID != "probe-result" || emitted != 1 {
					t.Fatalf("responsive reasoning interrupted or ping emitted: %+v %v events=%d", result, err, emitted)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || result.UsageKnown || emitted != 0 {
				t.Fatalf("pong extended deadline or invented usage: %+v %v events=%d", result, err, emitted)
			}
			if pings.Load() < 3 || creates.Load() != 1 {
				t.Fatalf("pings=%d creates=%d", pings.Load(), creates.Load())
			}
		})
	}
}

func TestWebSocketProbeFailsWithoutPongAndDoesNotReplay(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "parent-cancel"}[cancelParent], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var creates, pings atomic.Int32
			peerStopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(peerStopped)
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool {
					pings.Add(1)
					if cancelParent {
						cancel()
					}
					return false
				}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(r.Context()); err != nil {
					return
				}
				creates.Add(1)
				<-conn.CloseRead(r.Context()).Done()
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			adapter.websocketPingInterval, adapter.websocketPingTimeout = 15*time.Millisecond, 50*time.Millisecond
			capabilities := ResponsesCapabilities()
			capabilities.StreamTransport = TransportWebSocket
			started := time.Now()
			result, err := adapter.OpenStream(ctx, testTarget(server.URL, capabilities), Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}, func(Event) error { t.Error("probe became application output"); return nil })
			var failure *Error
			if cancelParent {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			} else if !errors.As(err, &failure) || failure.Code != ErrorCodeConnection || failure.RejectedBeforeExecution || failure.WebSocketHTTPFallback {
				t.Fatalf("unsafe probe classification: %+v %v", failure, err)
			}
			if time.Since(started) > 2*time.Second || result.UsageKnown || creates.Load() != 1 || pings.Load() != 1 {
				t.Fatalf("unbounded/replayed probe: %+v creates=%d pings=%d", result, creates.Load(), pings.Load())
			}
			select {
			case <-peerStopped:
			case <-time.After(time.Second):
				t.Fatal("broken upstream socket leaked")
			}
		})
	}
}

func TestWebSocketCompletionStopsPendingProbeAndReusesConnection(t *testing.T) {
	var handshakes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var conn *websocket.Conn
		var err error
		var completed bool
		conn, err = websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool {
			if !completed {
				completed = true
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"probe-first","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
			}
			return false
		}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		handshakes.Add(1)
		for turn := 0; ; turn++ {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			if turn > 0 {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"probe-second","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
			}
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	adapter.websocketPingInterval, adapter.websocketPingTimeout = 15*time.Millisecond, 3*time.Second
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.SessionID = "probe-thread"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, err := adapter.OpenStream(ctx, target, Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}, func(Event) error { return nil })
	if err != nil || first.ResponseID != "probe-first" {
		t.Fatalf("completion waited for pong: %+v %v", first, err)
	}
	second, err := adapter.OpenStream(ctx, target, Request{Body: []byte(`{"model":"m","input":"next","stream":true,"previous_response_id":"probe-first"}`)}, func(Event) error { return nil })
	if err != nil || second.ResponseID != "probe-second" || handshakes.Load() != 1 {
		t.Fatalf("normal completion destroyed owner socket: %+v %v handshakes=%d", second, err, handshakes.Load())
	}
}
