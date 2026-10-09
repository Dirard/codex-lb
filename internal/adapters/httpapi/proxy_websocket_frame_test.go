package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func frameConnection(t *testing.T, ctx context.Context, server *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-heartbeat"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func largeCreate() []byte {
	return []byte(`{"type":"response.create","model":"gpt-6-sol","input":"` + strings.Repeat("x", 6000) + `"}`)
}

func TestLargeWebSocketBodyPermitsDoNotBlockCancellation(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server, _ := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return application.ResponseResult{}, ctx.Err()
		}
		return (localReplyProvider{}).Respond(ctx, target, body, emit)
	})
	p := server.Config.Handler.(*ProxyHandler)
	p.websocketBodies = make(chan struct{}, 1)
	p.websocketKeepaliveInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := frameConnection(t, ctx, server)
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if err := heartbeatWait(ctx, started); err != nil {
		t.Fatal(err)
	}
	if len(p.websocketBodies) != 1 {
		t.Fatal("active large body was not charged")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.cancel"}`)); err != nil {
		t.Fatal(err)
	}
	if kind, _ := heartbeatRead(t, ctx, conn); kind != "error" {
		t.Fatal("cancellation did not finish the turn", kind)
	}
	if len(p.websocketBodies) != 0 {
		t.Fatal("cancelled body retained a permit")
	}
	for _, invalid := range []string{
		`{` + strings.Repeat(" ", 6000),
		`{"type":"unsupported","padding":"` + strings.Repeat("x", 6000) + `"}`,
		`{"type":"response.create","model":123,"input":"` + strings.Repeat("x", 6000) + `"}`,
	} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(invalid)); err != nil {
			t.Fatal(err)
		}
		if kind, _ := heartbeatRead(t, ctx, conn); kind != "error" {
			t.Fatal(kind)
		}
		if len(p.websocketBodies) != 0 {
			t.Fatal("invalid frame retained a body permit")
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if kind, _ := heartbeatRead(t, ctx, conn); kind != "response.completed" {
		t.Fatal(kind)
	}
	// Terminal delivery is acknowledged before the handler releases the input.
	for deadline := time.Now().Add(time.Second); len(p.websocketBodies) != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("completed body retained a permit")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("invalid or cancelled work replayed: %d", calls.Load())
	}
}

func TestLargeQueuedWebSocketBodiesReleaseOnOverflowAndDisconnect(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "overflow"}[overflow], func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			server, _ := heartbeatFixture(t, func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-ctx.Done()
				return application.ResponseResult{}, ctx.Err()
			})
			p := server.Config.Handler.(*ProxyHandler)
			p.websocketBodies = make(chan struct{}, 2)
			p.websocketKeepaliveInterval = time.Hour
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := frameConnection(t, ctx, server)
			if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
				t.Fatal(err)
			}
			if err := heartbeatWait(ctx, started); err != nil {
				t.Fatal(err)
			}
			if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(time.Second); len(p.websocketBodies) != 2; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("queued body not charged")
				}
			}
			if overflow {
				if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
					t.Fatal(err)
				}
				_, body, err := conn.Read(ctx)
				if err != nil || !strings.Contains(string(body), "local_capacity_exceeded") {
					t.Fatalf("body pressure was not reported: %s %v", body, err)
				}
			}
			conn.CloseNow()
			for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
				sockets := len(p.websockets)
				if sockets == 0 && len(p.websocketBodies) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("cleanup leaked socket=%d bodies=%d", sockets, len(p.websocketBodies))
				}
			}
			if calls.Load() != 1 {
				t.Fatal("queued/unread generation was dispatched")
			}
		})
	}
}

func TestLargeWebSocketCurrentAndStagedRequestsComplete(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server, _ := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(started)
			if err := heartbeatWait(ctx, finish); err != nil {
				return application.ResponseResult{}, err
			}
		}
		return (localReplyProvider{}).Respond(ctx, target, body, emit)
	})
	p := server.Config.Handler.(*ProxyHandler)
	p.websocketBodies = make(chan struct{}, 2)
	p.websocketKeepaliveInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := frameConnection(t, ctx, server)
	for range 2 {
		if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
			t.Fatal(err)
		}
		if err := heartbeatWait(ctx, started); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(time.Second); len(p.websocketBodies) != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("next message was not staged beside its current response")
		}
	}
	close(finish)
	for range 2 {
		if kind, _ := heartbeatRead(t, ctx, conn); kind != "response.completed" {
			t.Fatal("staged response did not complete", kind)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("staged response was dropped or replayed")
	}
}

func TestLargeWebSocketBodyDiscardLeavesControlReadable(t *testing.T) {
	p := &ProxyHandler{websocketBodies: make(chan struct{}, 1)}
	p.websocketBodies <- struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(32 << 20)
		_, body, release, err := p.readWebSocketFrame(r.Context(), conn)
		defer release()
		if len(body) != 0 || err == nil {
			t.Error("read a large body without admission")
			return
		}
		_ = sendWebSocketError(r.Context(), conn, err)
		_, control, release, err := p.readWebSocketFrame(r.Context(), conn)
		defer release()
		if err != nil || string(control) != `{"type":"response.cancel"}` {
			t.Error("control frame unavailable after discard", err)
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, control)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 64<<10))); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(body), "local_capacity_exceeded") {
		t.Fatalf("overflow not reported: %s %v", body, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.cancel"}`)); err != nil {
		t.Fatal(err)
	}
	if _, body, err := conn.Read(ctx); err != nil || string(body) != `{"type":"response.cancel"}` {
		t.Fatalf("control lost after discard: %s %v", body, err)
	}
	if len(p.websocketBodies) != 1 {
		t.Fatal("rejection released someone else's permit")
	}
}

func TestLargePartialWebSocketFrameDisconnectReleasesPermit(t *testing.T) {
	var calls atomic.Int32
	server, store := heartbeatFixture(t, func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		return application.ResponseResult{}, nil
	})
	p := server.Config.Handler.(*ProxyHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := frameConnection(t, ctx, server)
	writer, err := conn.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(strings.Repeat("x", 16<<10))); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); len(p.websocketBodies) != 1; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("partial large input was not charged")
		}
	}
	conn.CloseNow()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if len(p.websockets) == 0 && len(p.websocketBodies) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial input leaked its permits")
		}
	}
	key, err := store.GetAPIKey(ctx, "heartbeat-key")
	if err != nil || key.Limits[0].CurrentValue != 0 || calls.Load() != 0 {
		t.Fatal("partial input created billable work")
	}
}

func TestWebSocketInputPressurePreservesSameActiveTurn(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	var calls atomic.Int32
	server, _ := heartbeatFixture(t, func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		close(stopped)
		return application.ResponseResult{}, ctx.Err()
	})
	p := server.Config.Handler.(*ProxyHandler)
	p.websocketBodies = make(chan struct{}, 1)
	p.websocketKeepaliveInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active := frameConnection(t, ctx, server)
	if err := active.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if err := heartbeatWait(ctx, started); err != nil {
		t.Fatal(err)
	}
	if err := active.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	_, body, err := active.Read(ctx)
	if err != nil || !strings.Contains(string(body), "local_capacity_exceeded") {
		t.Fatalf("overflow: %s %v", body, err)
	}
	select {
	case <-stopped:
		t.Fatal("excess input cancelled the admitted turn")
	default:
	}
	if len(p.websocketBodies) != 1 || calls.Load() != 1 {
		t.Fatal("excess input changed admission or dispatched upstream")
	}
	if err := active.Write(ctx, websocket.MessageText, []byte(`{"type":"response.cancel"}`)); err != nil {
		t.Fatal(err)
	}
	if kind, _ := heartbeatRead(t, ctx, active); kind != "error" {
		t.Fatal(kind)
	}
	if len(p.websocketBodies) != 0 {
		t.Fatal("cancelled body retained a permit")
	}
}

func TestLargeWebSocketDiscardKeepsMessageSizeLimit(t *testing.T) {
	p := &ProxyHandler{websocketBodies: make(chan struct{}, 1)}
	p.websocketBodies <- struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(8 << 10)
		_, body, release, err := p.readWebSocketFrame(r.Context(), conn)
		release()
		if body != nil || !errors.Is(err, websocket.ErrMessageTooBig) {
			t.Error("discard bypassed the message size limit", err)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = conn.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 64<<10)))
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("oversized message did not close the connection: %v", err)
	}
	if len(p.websocketBodies) != 1 {
		t.Fatal("discard released another message's permit")
	}
}
