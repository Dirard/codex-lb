package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func TestIdleWebSocketsDoNotConsumeHTTPBodyCapacity(t *testing.T) {
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete("http-beside-idle-sockets", emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer synthetic-key"}}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/backend-api/codex/responses"
	connections := make([]*websocket.Conn, 0, 256)
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
	for i := range 256 {
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
	connection, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil {
		connection.CloseNow()
		t.Fatal("socket capacity is unbounded")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected socket overflow: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
	req.Header = headers.Clone()
	response, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "http-beside-idle-sockets") {
		t.Fatalf("idle sockets blocked HTTP: status=%d read=%v body=%s", response.StatusCode, readErr, body)
	}
	connections[0].CloseNow()
	// The server processes the peer close asynchronously; wait only for that
	// local cleanup, not for the 120-second idle timeout.
	for deadline := time.Now().Add(2 * time.Second); ; {
		connection, _, err = websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if err == nil {
			connections = append(connections, connection)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnect did not release socket capacity: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
