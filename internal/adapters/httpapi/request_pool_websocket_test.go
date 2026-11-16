package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func poolWebSocketFixture(t *testing.T, pool domain.RequestPool) (*httptest.Server, *sqlite.Store, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	provider := heartbeatProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		id := fmt.Sprintf("resp_pool_%d", calls.Add(1))
		body := json.RawMessage(fmt.Sprintf(`{"id":%q,"object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`, id))
		terminal, _ := json.Marshal(map[string]any{"type": "response.completed", "response": body})
		if err := emit(application.ResponseEvent{Type: "response.completed", Data: terminal}); err != nil {
			return application.ResponseResult{}, err
		}
		return application.ResponseResult{ResponseID: id, Response: body, Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
	})
	server, store := heartbeatFixtureConfigured(t, provider, func(handler *ProxyHandler) {
		handler.ConfigureRequestPools(application.NewRequestPools())
		handler.websocketKeepaliveInterval = 20 * time.Millisecond
	})
	setHeartbeatKeyPool(t, store, &pool)
	return server, store, calls
}

func setHeartbeatKeyPool(t *testing.T, store *sqlite.Store, pool *domain.RequestPool) {
	t.Helper()
	key, err := store.GetAPIKey(context.Background(), "heartbeat-key")
	if err != nil {
		t.Fatal(err)
	}
	key.RequestPool = pool
	if err := store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
}

type poolFrame struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Error  struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Headers map[string]string `json:"headers"`
}

func readPoolFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) poolFrame {
	t.Helper()
	_, body, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame poolFrame
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func sendCreate(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
}

func readUntilCompleted(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	for {
		frame := readPoolFrame(t, ctx, conn)
		switch frame.Type {
		case "response.completed":
			return
		case "error":
			t.Fatalf("turn failed: %+v", frame)
		}
	}
}

func TestRequestPoolWebSocketRefusesATurnAndKeepsTheConnection(t *testing.T) {
	server, store, calls := poolWebSocketFixture(t, domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 0})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses",
		&websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer synthetic-heartbeat"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// The upgrade reports the pool and takes no slot.
	if response.Header.Get("X-Ratelimit-Limit-Requests") != "1" || response.Header.Get("X-Ratelimit-Remaining-Requests") != "1" {
		t.Fatalf("upgrade headers %v", response.Header)
	}
	sendCreate(t, ctx, conn)
	readUntilCompleted(t, ctx, conn)
	sendCreate(t, ctx, conn)
	frame := readPoolFrame(t, ctx, conn)
	if frame.Type != "error" || frame.Status != 429 || frame.Error.Type != "requests" || frame.Error.Code != "rate_limit_exceeded" ||
		!strings.Contains(frame.Error.Message, "Please try again in 60s.") || frame.Headers["retry-after"] != "60" ||
		frame.Headers["x-ratelimit-limit-requests"] != "1" || frame.Headers["x-ratelimit-remaining-requests"] != "0" ||
		!strings.HasSuffix(frame.Headers["x-ratelimit-reset-requests"], "s") {
		t.Fatalf("refusal frame %+v", frame)
	}
	if calls.Load() != 1 {
		t.Fatalf("refused turn reached upstream: %d calls", calls.Load())
	}
	// The connection stays open and serves the next turn once a slot is free.
	setHeartbeatKeyPool(t, store, &domain.RequestPool{RequestsPerMinute: 2})
	sendCreate(t, ctx, conn)
	readUntilCompleted(t, ctx, conn)
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
}

func TestRequestPoolWebSocketCancelledWaitTakesNoSlot(t *testing.T) {
	server, store, calls := poolWebSocketFixture(t, domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 60})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses",
		&websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer synthetic-heartbeat"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	sendCreate(t, ctx, conn)
	readUntilCompleted(t, ctx, conn)
	sendCreate(t, ctx, conn)
	// Codex hears keepalives while the turn waits for its slot.
	if frame := readPoolFrame(t, ctx, conn); frame.Type != "codex.keepalive" {
		t.Fatalf("waiting turn sent %+v", frame)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.cancel"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		frame := readPoolFrame(t, ctx, conn)
		if frame.Type == "codex.keepalive" {
			continue
		}
		if frame.Type != "error" || frame.Error.Code != "request_cancelled" {
			t.Fatalf("cancelled wait sent %+v", frame)
		}
		break
	}
	// Had the cancelled turn taken a slot, a pool of two would be full and
	// this turn would wait a minute.
	setHeartbeatKeyPool(t, store, &domain.RequestPool{RequestsPerMinute: 2, MaxWaitSeconds: 60})
	sendCreate(t, ctx, conn)
	readUntilCompleted(t, ctx, conn)
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
}
