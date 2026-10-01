package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
)

func TestOperationsRealtimeBridgesLiveCallAndStopsOnUpstreamClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/live/call_1" || r.URL.RawQuery != "version=2" ||
			r.Header.Get("Authorization") != "Bearer chatgpt-access-token" ||
			r.Header.Get("ChatGPT-Account-ID") != "chatgpt-account" ||
			r.Header.Get("Originator") != "codex_cli_rs" {
			t.Fatalf("realtime upstream request invalid: %s?%s %+v", r.URL.Path, r.URL.RawQuery, r.Header)
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept realtime: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "done")
		_, data, err := connection.Read(context.Background())
		if err != nil {
			t.Errorf("read downstream realtime: %v", err)
			return
		}
		if string(data) != `{"type":"ping"}` {
			t.Fatalf("unexpected realtime frame: %s", data)
		}
		if err := connection.Write(context.Background(), websocket.MessageText, []byte(`{"type":"pong"}`)); err != nil {
			t.Errorf("write realtime: %v", err)
		}
	}))
	defer server.Close()
	adapter, _ := operationAdapter(t, server)
	downstream := &realtimeTestConnection{incoming: make(chan application.CodexRealtimeMessage, 1), closed: make(chan struct{})}
	downstream.incoming <- application.CodexRealtimeMessage{Data: []byte(`{"type":"ping"}`)}
	err := adapter.Realtime(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}, application.CodexRealtimeRequest{
		CallID: "call_1", Query: [][2]string{{"version", "2"}},
	}, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(downstream.written) != 1 || string(downstream.written[0].Data) != `{"type":"pong"}` {
		t.Fatalf("realtime echo missing: %+v", downstream.written)
	}
	if !strings.HasPrefix(operationAdapterURL(server), "http://") {
		t.Fatal("unexpected test URL")
	}
}

func operationAdapterURL(server *httptest.Server) string { return server.URL }

type realtimeTestConnection struct {
	incoming chan application.CodexRealtimeMessage
	written  []application.CodexRealtimeMessage
	closed   chan struct{}
}

func (c *realtimeTestConnection) Read(ctx context.Context) (application.CodexRealtimeMessage, error) {
	select {
	case message := <-c.incoming:
		return message, nil
	case <-c.closed:
		return application.CodexRealtimeMessage{}, context.Canceled
	case <-ctx.Done():
		return application.CodexRealtimeMessage{}, ctx.Err()
	}
}

func (c *realtimeTestConnection) Write(_ context.Context, message application.CodexRealtimeMessage) error {
	c.written = append(c.written, message)
	return nil
}

func (c *realtimeTestConnection) Close(string, string) error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
