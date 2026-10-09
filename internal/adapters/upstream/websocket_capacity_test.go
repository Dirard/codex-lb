package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func Test512ActiveWebSocketSessionsKeepTheirOwnedConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, body, err := conn.Read(ctx)
		var request struct {
			Input string `json:"input"`
		}
		if err != nil || json.Unmarshal(body, &request) != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"ready"}`)); err != nil {
			return
		}
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, request.Input)))
		_, _, _ = conn.Read(ctx) // Retain the connection until the adapter closes it.
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	defer unblock()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	base := testTarget(server.URL, capabilities)
	base.SessionID = "same-logical-session"
	body := Request{Body: []byte(`{"model":"m","input":"hello","stream":true}`)}
	opened, finished := make(chan struct{}, 512), make(chan error, 512)
	var workers sync.WaitGroup
	workers.Add(512)
	defer func() { cancel(); unblock(); workers.Wait() }()
	for i := range 512 {
		go func() {
			defer workers.Done()
			id := fmt.Sprintf("response-%d", i)
			request := Request{Body: []byte(fmt.Sprintf(`{"model":"m","input":%q,"stream":true}`, id))}
			result, err := adapter.OpenStream(ctx, base, request, func(event Event) error {
				if event.Type == "response.output_text.delta" {
					opened <- struct{}{}
				}
				return nil
			})
			if err == nil && (!result.UsageKnown || result.ResponseID != id) {
				err = fmt.Errorf("stream lost its terminal response or usage")
			}
			finished <- err
		}()
	}
	for i := range 512 {
		select {
		case <-opened:
		case err := <-finished:
			t.Fatalf("stream returned before all 512 were active (%d opened): %v", i, err)
		case <-ctx.Done():
			t.Fatalf("only %d streams active: %v", i, ctx.Err())
		}
	}
	overflow := base
	overflow.SessionID = "overflow-session"
	_, err := adapter.OpenStream(ctx, overflow, body, func(Event) error { return nil })
	var rejected *Error
	if !errors.As(err, &rejected) || rejected.Code != "local_capacity_exceeded" || !rejected.RejectedBeforeExecution {
		t.Fatalf("overflow was not bounded: %v", err)
	}
	unblock()
	for range 512 {
		if err := <-finished; err != nil {
			t.Fatalf("active peer lost its connection: %v", err)
		}
	}
	adapter.sessions.mu.Lock()
	retained := len(adapter.sessions.responses)
	adapter.sessions.mu.Unlock()
	if retained != 512 {
		t.Fatalf("parallel branches lost their response affinity: %d", retained)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	adapter.sessions.mu.Lock()
	remaining := len(adapter.sessions.entries) + len(adapter.sessions.responses) + adapter.sessions.lanes
	adapter.sessions.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("Close retained %d session entries", remaining)
	}
}
