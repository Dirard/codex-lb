package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The capability conflict check reads the turn as finalizing from the moment
// its terminal frame was accepted, not when the worker returns afterwards.
func TestWebSocketTurnReportsItsFirstTerminalWrite(t *testing.T) {
	t.Parallel()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- connection
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	connection := <-accepted
	defer connection.CloseNow()
	go func() {
		for {
			if _, _, err := client.Read(ctx); err != nil {
				return
			}
		}
	}()
	var notified atomic.Int32
	turn := &websocketTurn{connection: connection, lastWrite: time.Now(), onTerminal: func() { notified.Add(1) }}
	if err := turn.write([]byte(`{"type":"response.created"}`), false); err != nil || notified.Load() != 0 {
		t.Fatalf("a non-terminal frame reported the terminal: %v, %d", err, notified.Load())
	}
	for range 2 {
		if err := turn.write([]byte(`{"type":"response.completed"}`), true); err != nil {
			t.Fatal(err)
		}
	}
	if notified.Load() != 1 {
		t.Fatalf("terminal reported %d times, want once", notified.Load())
	}
}
