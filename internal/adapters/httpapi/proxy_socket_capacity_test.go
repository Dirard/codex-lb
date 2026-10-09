package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func TestIdleWebSocketsDoNotConsumeHTTPBodyCapacity(t *testing.T) {
	var calls atomic.Int64
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete(fmt.Sprintf("beside-idle-sockets-%d", calls.Add(1)), emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer synthetic-key"}}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/backend-api/codex/responses"
	connections := make([]*websocket.Conn, 0, 512)
	defer func() {
		for _, connection := range connections {
			connection.CloseNow()
		}
	}()
	// Failed handshakes must release the same lifetime budget as disconnects.
	for range 257 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/responses", nil)
		req.Header = headers.Clone()
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable {
			t.Fatal("failed upgrades leaked socket admission")
		}
	}
	for i := range 512 {
		connection, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Fatalf("idle socket %d rejected, status=%d: %v", i+1, status, err)
		}
		connections = append(connections, connection)
	}
	if calls.Load() != 0 {
		t.Fatal("empty connections dispatched upstream")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
	req.Header = headers.Clone()
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "beside-idle-sockets-1") {
		t.Fatalf("idle sockets blocked HTTP: status=%d read=%v body=%s", response.StatusCode, readErr, body)
	}
	if err := connections[0].Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	_, message, err := connections[0].Read(ctx)
	if err != nil || !strings.Contains(string(message), "beside-idle-sockets-2") || calls.Load() != 2 {
		t.Fatalf("standby sockets blocked a generation: %s %v", message, err)
	}
}
