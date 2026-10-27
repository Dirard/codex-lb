package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func websocketTestTarget(serverURL, session string) Target {
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(serverURL, capabilities)
	target.SessionID = session
	return target
}

func completedFrame(id string) json.RawMessage {
	return mustJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
}

// NET-1: a frame that arrives while the pooled socket has no lease belongs to
// no response and must not be delivered to the next response on that socket.
func TestWebSocketUnsolicitedIdleFrameNeverReachesNextResponse(t *testing.T) {
	var handshakes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		handshakes.Add(1)
		for turn := 1; ; turn++ {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			id := fmt.Sprintf("resp_%d", turn)
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": id}}))
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.output_text.delta", "delta": "turn " + id}))
			_ = conn.Write(r.Context(), websocket.MessageText, completedFrame(id))
			// Unsolicited trailing frames after the terminal event.
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.output_text.delta", "delta": "STALE from " + id}))
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "responsesapi.websocket_timing", "response_id": id}))
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	target := websocketTestTarget(server.URL, "stale-thread")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	collect := func(body map[string]any) (Result, []string, error) {
		var events []string
		result, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(body)}, func(e Event) error {
			events = append(events, string(e.Data))
			return nil
		})
		return result, events, err
	}
	if _, _, err := collect(map[string]any{"model": "m", "input": "hi", "stream": true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the trailing frames arrive while the socket is idle
	second, events, err := collect(map[string]any{"model": "m", "input": "next", "stream": true})
	if err != nil || second.ResponseID != "resp_2" {
		t.Fatalf("second response: %+v %v", second, err)
	}
	for _, event := range events {
		if strings.Contains(event, "resp_1") {
			t.Fatalf("previous response frame reached the next lease: %v", events)
		}
	}
	time.Sleep(100 * time.Millisecond)
	third, events, err := collect(map[string]any{"model": "m", "input": "continue", "stream": true, "previous_response_id": "resp_2"})
	if err != nil || third.ResponseID != "resp_3" || handshakes.Load() != 1 {
		t.Fatalf("fenced socket lost its continuation: %+v %v handshakes=%d", third, err, handshakes.Load())
	}
	for _, event := range events {
		if strings.Contains(event, "resp_2") && !strings.Contains(event, "response.created") {
			t.Fatalf("previous response frame reached the continuation: %v", events)
		}
	}
}

// NET-1: events are matched to the response announced by response.created; a
// terminal for another response cannot settle this request or keep its socket.
func TestWebSocketTerminalMustBelongToCurrentResponse(t *testing.T) {
	for name, frames := range map[string][]json.RawMessage{
		"foreign-terminal": {
			mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_current"}}),
			completedFrame("resp_foreign"),
		},
		"second-created": {
			mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_current"}}),
			mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_other"}}),
			completedFrame("resp_other"),
		},
		"previous-terminal-replayed": {
			completedFrame("resp_seed"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			var handshakes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				handshakes.Add(1)
				for turn := 1; ; turn++ {
					if _, _, err := conn.Read(r.Context()); err != nil {
						return
					}
					if turn == 1 {
						_ = conn.Write(r.Context(), websocket.MessageText, completedFrame("resp_seed"))
						continue
					}
					for _, frame := range frames {
						_ = conn.Write(r.Context(), websocket.MessageText, frame)
					}
				}
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			target := websocketTestTarget(server.URL, "id-thread")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "seed", "stream": true})}, func(Event) error { return nil }); err != nil {
				t.Fatal(err)
			}
			var terminals int
			result, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "next", "stream": true, "previous_response_id": "resp_seed"})}, func(e Event) error {
				if e.Type == "response.completed" {
					terminals++
				}
				return nil
			})
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != ErrorCodeInvalidStream || failure.RejectedBeforeExecution || terminals != 0 || result.UsageKnown {
				t.Fatalf("foreign terminal accepted: %+v %v terminals=%d", result, err, terminals)
			}
			if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "fresh", "stream": true})}, func(Event) error { return nil }); handshakes.Load() != 2 {
				t.Fatalf("contaminated socket was reused: handshakes=%d err=%v", handshakes.Load(), err)
			}
		})
	}
}

// NET-2: a slow downstream consumer must not make a healthy upstream fail the
// transport probe while the reader is waiting to hand over a frame.
func TestWebSocketSlowConsumerKeepsHealthyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		peer := conn.CloseRead(r.Context()) // keeps answering pings
		_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_slow"}}))
		for i := range 5 {
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.output_text.delta", "delta": fmt.Sprint(i)}))
		}
		_ = conn.Write(r.Context(), websocket.MessageText, completedFrame("resp_slow"))
		<-peer.Done()
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	adapter.websocketPingInterval, adapter.websocketPingTimeout = 100*time.Millisecond, 100*time.Millisecond
	defer adapter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocked, deltas := false, 0
	result, err := adapter.OpenStream(ctx, websocketTestTarget(server.URL, "slow-thread"), Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(e Event) error {
		if e.Type == "response.output_text.delta" {
			deltas++
			if !blocked {
				blocked = true
				time.Sleep(time.Second) // slower than ping interval+timeout, within the 30 s downstream write bound
			}
		}
		return nil
	})
	if err != nil || result.ResponseID != "resp_slow" || !result.UsageKnown || deltas != 5 {
		t.Fatalf("slow consumer killed a healthy response: %+v %v deltas=%d", result, err, deltas)
	}
}

// deadAfterFirstTurn answers the first response.create and then stops reading
// (no pong, no events) while keeping TCP open; later connections are healthy.
func deadAfterFirstTurn(handshakes, deadCreates *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connection := handshakes.Add(1)
		for turn := 1; ; turn++ {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if connection == 1 {
				deadCreates.Add(1)
			}
			var create struct {
				Input string `json:"input"`
			}
			_ = json.Unmarshal(body, &create)
			_ = conn.Write(r.Context(), websocket.MessageText, completedFrame(fmt.Sprintf("resp_%d_%d", connection, turn)))
			if connection == 1 {
				<-r.Context().Done()
				return
			}
		}
	})
}

// NET-3: a socket that died while idle is detected under its lease before
// response.create, so a fresh request uses a new connection instead of failing
// after dispatch.
func TestWebSocketDeadIdleSocketIsReplacedBeforeCreate(t *testing.T) {
	var handshakes, deadCreates atomic.Int32
	server := httptest.NewServer(deadAfterFirstTurn(&handshakes, &deadCreates))
	defer server.Close()
	adapter := New(server.Client(), nil)
	adapter.websocketReuseProbeAfter, adapter.websocketReuseProbeTimeout = 50*time.Millisecond, 100*time.Millisecond
	defer adapter.Close()
	target := websocketTestTarget(server.URL, "dead-thread")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	result, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "again", "stream": true})}, func(Event) error { return nil })
	if err != nil || result.ResponseID != "resp_2_1" || handshakes.Load() != 2 || deadCreates.Load() != 1 {
		t.Fatalf("dead idle socket reused: %+v %v handshakes=%d deadCreates=%d", result, err, handshakes.Load(), deadCreates.Load())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("dead idle socket detection was not bounded: %v", elapsed)
	}
}

// NET-3: an exact continuation whose idle anchor died is rejected before
// execution; it is never sent to a different connection.
func TestWebSocketDeadIdleContinuationIsRejectedBeforeCreate(t *testing.T) {
	var handshakes, deadCreates atomic.Int32
	server := httptest.NewServer(deadAfterFirstTurn(&handshakes, &deadCreates))
	defer server.Close()
	adapter := New(server.Client(), nil)
	adapter.websocketReuseProbeAfter, adapter.websocketReuseProbeTimeout = 50*time.Millisecond, 100*time.Millisecond
	defer adapter.Close()
	target := websocketTestTarget(server.URL, "dead-continuation")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err = adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "again", "stream": true, "previous_response_id": first.ResponseID})}, func(Event) error { return nil })
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeContinuationNotFound || !failure.RejectedBeforeExecution || deadCreates.Load() != 1 || handshakes.Load() != 1 {
		t.Fatalf("dead continuation anchor was not rejected before create: %+v %v deadCreates=%d handshakes=%d", failure, err, deadCreates.Load(), handshakes.Load())
	}
}

// A recently used socket is reused without an extra round trip.
func TestWebSocketRecentlyIdleSocketSkipsReuseProbe(t *testing.T) {
	var pings atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return true }})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for turn := 1; ; turn++ {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, completedFrame(fmt.Sprintf("resp_%d", turn)))
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	target := websocketTestTarget(server.URL, "warm-thread")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 3 {
		if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if pings.Load() != 0 {
		t.Fatalf("warm reuse paid a liveness round trip: pings=%d", pings.Load())
	}
}
