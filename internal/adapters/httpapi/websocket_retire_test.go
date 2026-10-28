package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func expectGoingAway(t *testing.T, ctx context.Context, conn *websocket.Conn, reason string) {
	t.Helper()
	for {
		_, body, err := conn.Read(ctx)
		if err == nil {
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(body, &event) == nil && event.Type == "codex.keepalive" {
				continue
			}
			t.Fatalf("event before close: %s", body)
		}
		var closeErr websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.StatusGoingAway || closeErr.Reason != reason {
			t.Fatalf("close: %v, want 1001 %q", err, reason)
		}
		return
	}
}

func TestWebSocketRetirementClosesIdleSessionAndKeepsHTTPAdmission(t *testing.T) {
	server, _ := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return (localReplyProvider{}).Respond(ctx, target, body, emit)
	})
	p := server.Config.Handler.(*ProxyHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	idle := frameConnection(t, ctx, server)
	p.proxy.BeginRetire()
	expectGoingAway(t, ctx, idle, "Server is restarting; reconnect")
	frames := postSSE(t, server, "/v1/responses")
	if len(frames) == 0 || frames[len(frames)-1].event != "response.completed" {
		t.Fatalf("retiring server did not serve a new HTTP request: %+v", frames)
	}
	// A socket that still reaches a retiring server is moved before its first turn.
	late := frameConnection(t, ctx, server)
	expectGoingAway(t, ctx, late, "Server is restarting; reconnect")
}

func TestWebSocketRetirementFinishesActiveTurnBeforeClosing(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server, _ := heartbeatFixtureConfigured(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(entered)
			if err := heartbeatWait(ctx, finish); err != nil {
				return application.ResponseResult{}, err
			}
		}
		return (localReplyProvider{}).Respond(ctx, target, body, emit)
	}, func(handler *ProxyHandler) { handler.websocketKeepaliveInterval = time.Hour })
	p := server.Config.Handler.(*ProxyHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := frameConnection(t, ctx, server)
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if err := heartbeatWait(ctx, entered); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	// Large frames are charged to the body budget: two charges mean the next
	// turn is queued.
	for deadline := time.Now().Add(time.Second); p.bodies.Usage().UsedBytes < 2*int64(len(largeCreate())); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("second response.create was not queued")
		}
	}
	p.proxy.BeginRetire()
	close(finish)
	if kind, id := heartbeatRead(t, ctx, conn); kind != "response.completed" || id != "local-response" {
		t.Fatalf("active turn did not finish on the retiring server: %s %q", kind, id)
	}
	expectGoingAway(t, ctx, conn, "Server is restarting; reconnect")
	if calls.Load() != 1 {
		t.Fatalf("queued turn started on a retiring server: %d upstream calls", calls.Load())
	}
	for deadline := time.Now().Add(time.Second); p.bodies.Usage().UsedBytes != 0 || len(p.websockets) != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("closed session leaked: body bytes=%d sockets=%d", p.bodies.Usage().UsedBytes, len(p.websockets))
		}
	}
}

func TestWebSocketDrainKeepsShutdownCloseReason(t *testing.T) {
	server, _ := heartbeatFixture(t, nil)
	p := server.Config.Handler.(*ProxyHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := frameConnection(t, ctx, server)
	p.proxy.BeginRetire()
	p.proxy.BeginDrain()
	expectGoingAway(t, ctx, conn, "Server is shutting down")
}
