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

func TestWebSocketContinuationReusesOwnedConnection(t *testing.T) {
	var handshakes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connectionID := handshakes.Add(1)
		previous := ""
		for turn := 1; ; turn++ {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				Previous string `json:"previous_response_id"`
			}
			if json.Unmarshal(body, &request) != nil {
				t.Error("invalid response.create")
				return
			}
			if request.Previous != "" && request.Previous != previous {
				t.Error("previous response moved between upstream connections")
				return
			}
			id := fmt.Sprintf("response_%d_%d", connectionID, turn)
			if err := conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.output_text.delta", "delta": strings.Repeat("x", 64<<10)})); err != nil {
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})); err != nil {
				return
			}
			previous = id
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.SessionID = "logical-thread"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true})}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var canonical struct {
		ID     string            `json:"id"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(first.Response, &canonical) != nil || canonical.ID != first.ResponseID || canonical.Output == nil {
		t.Fatal("result retained SSE wrapper instead of response body")
	}
	second, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "next", "stream": true, "previous_response_id": first.ResponseID})}, func(Event) error { return nil })
	if err != nil || second.ResponseID != "response_1_2" || handshakes.Load() != 1 {
		t.Fatalf("upstream continuation reconnected: count=%d error=%v", handshakes.Load(), err)
	}
	target.KeyID = "other-key"
	if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "new", "stream": true})}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if handshakes.Load() != 2 {
		t.Fatal("different API keys shared an upstream WebSocket")
	}
	target.AccountGeneration++ // Reimport may use exactly the same credentials.
	if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "fresh", "stream": true})}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if handshakes.Load() != 3 {
		t.Fatal("reimported account reused the retired incarnation's WebSocket")
	}
	target.RouteRevision++ // A -> B -> A can restore URL and credential bytes.
	if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "new route", "stream": true})}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if handshakes.Load() != 4 {
		t.Fatal("edited route reused the retired WebSocket")
	}
}

func TestRequiredCapabilityRetiresOrdinaryUpstreamSocket(t *testing.T) {
	var handshakes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		id := handshakes.Add(1)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", id), "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}))
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.SessionID = "same-logical-session"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hello", "stream": true})}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	adapter.sessions.mu.Lock()
	var ordinary *websocketSession
	for _, entry := range adapter.sessions.entries {
		ordinary = entry
	}
	adapter.sessions.mu.Unlock()
	if ordinary == nil {
		t.Fatal("ordinary session was not retained")
	}
	target.RequiredCapability = true
	if _, err := adapter.OpenStream(ctx, target, Request{Body: mustJSON(map[string]any{"model": "m", "input": "next", "stream": true, "previous_response_id": first.ResponseID})}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	adapter.sessions.mu.Lock()
	retired := ordinary.dead
	var required *websocketSession
	for _, entry := range adapter.sessions.entries {
		if entry.key.RequiredCapability {
			required = entry
		}
	}
	adapter.sessions.mu.Unlock()
	if !retired || handshakes.Load() != 2 {
		t.Fatalf("required request reused ordinary socket: retired=%v handshakes=%d", retired, handshakes.Load())
	}
	if required == nil {
		t.Fatal("required upstream socket was not retained")
	}
	adapter.RetireRequiredCapability(ownerOf(target))
	adapter.sessions.mu.Lock()
	retired = required.dead
	adapter.sessions.mu.Unlock()
	if !retired {
		t.Fatal("revoked required socket was not retired")
	}
}

func TestRequiredCapabilityRejectsPendingOrdinaryUpstreamSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			_, _, _ = conn.Read(r.Context())
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.SessionID = "pending-session"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ordinary, _, err := adapter.sessions.acquire(ctx, target, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.sessions.release(ordinary, false, "", true)
	target.RequiredCapability = true
	_, _, err = adapter.sessions.acquire(ctx, target, "", server.Client())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "capability_routing_unavailable" {
		t.Fatalf("pending ordinary socket was reused: %v", err)
	}
}

func TestRequiredCapabilityRetirementWaitsForPendingWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			_, _, _ = conn.Read(r.Context())
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.SessionID = "required-pending-session"
	target.RequiredCapability = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entry, _, err := adapter.sessions.acquire(ctx, target, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	adapter.RetireRequiredCapability(ownerOf(target))
	adapter.sessions.mu.Lock()
	deferred, dead := entry.retireWhenIdle, entry.dead
	adapter.sessions.mu.Unlock()
	if !deferred || dead {
		t.Fatal("pending required work was closed before settlement")
	}
	adapter.sessions.release(entry, false, "", true)
	adapter.sessions.mu.Lock()
	dead = entry.dead
	adapter.sessions.mu.Unlock()
	if !dead {
		t.Fatal("retired required socket remained after pending work")
	}
}

func TestWebSocketQuotaFailuresDoNotEmitBeforeFailover(t *testing.T) {
	for _, handshake := range []bool{true, false} {
		t.Run(fmt.Sprint(handshake), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handshake {
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":{"code":"usage_limit_reached","message":"quota"}}`)
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
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":429,"error":{"code":"usage_limit_reached","message":"quota"}}`))
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			capabilities := ResponsesCapabilities()
			capabilities.StreamTransport = TransportWebSocket
			emitted := 0
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := adapter.OpenStream(ctx, testTarget(server.URL, capabilities), Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}, func(Event) error { emitted++; return nil })
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != ErrorCodeInsufficientQuota || emitted != 0 {
				t.Fatalf("quota misclassified or emitted: events=%d error=%v", emitted, err)
			}
		})
	}
}

type endlessSSELine struct{ read int }

func (r *endlessSSELine) Read(dst []byte) (int, error) {
	for i := range dst {
		dst[i] = 'a'
	}
	r.read += len(dst)
	return len(dst), nil
}

func TestSSELineBoundBeforeUnboundedAllocation(t *testing.T) {
	reader := new(endlessSSELine)
	err := readSSE(reader, func(string, []byte) error { return nil })
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeInvalidStream || reader.read > maxSSEEventBytes+64<<10 {
		t.Fatalf("unbounded SSE read: bytes=%d error=%v", reader.read, err)
	}
}
