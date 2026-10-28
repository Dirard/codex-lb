package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func heartbeatSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// silentThenComplete is silent, announces local-response, is silent again and
// completes.
func silentThenComplete(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	if err := heartbeatSleep(ctx, 80*time.Millisecond); err != nil {
		return application.ResponseResult{}, err
	}
	if err := emit(application.ResponseEvent{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"local-response","status":"in_progress"}}`)}); err != nil {
		return application.ResponseResult{}, err
	}
	if err := heartbeatSleep(ctx, 80*time.Millisecond); err != nil {
		return application.ResponseResult{}, err
	}
	return (localReplyProvider{}).Respond(ctx, target, body, emit)
}

// continuousDeltas emits a visible delta every 5 ms for 300 ms.
func continuousDeltas(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	if err := emit(application.ResponseEvent{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"local-response","status":"in_progress"}}`)}); err != nil {
		return application.ResponseResult{}, err
	}
	for i := range 60 {
		if err := emit(application.ResponseEvent{Type: "response.output_text.delta", Data: []byte(fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"msg","output_index":0,"content_index":0,"delta":"%d"}`, i))}); err != nil {
			return application.ResponseResult{}, err
		}
		if err := heartbeatSleep(ctx, 5*time.Millisecond); err != nil {
			return application.ResponseResult{}, err
		}
	}
	return (localReplyProvider{}).Respond(ctx, target, body, emit)
}

type sseFrame struct{ comment, event, data string }

func postSSE(t *testing.T, server *httptest.Server, path string) []sseFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(`{"model":"gpt-6-sol","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-heartbeat")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status %d: %s", response.StatusCode, payload)
	}
	var frames []sseFrame
	var current sseFrame
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			frames = append(frames, current)
			current = sseFrame{}
		case strings.HasPrefix(line, ":"):
			current.comment = line
		case strings.HasPrefix(line, "event: "):
			current.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frames
}

// NET-15: the Codex SSE parser ignores comments, so native SSE heartbeats are
// data events; generic /v1 SSE keeps comment heartbeats.
func TestNativeSSEHeartbeatsAreDataEvents(t *testing.T) {
	for _, path := range []string{"/backend-api/codex/responses", "/backend-api/codex/responses/", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			server, _ := heartbeatFixture(t, silentThenComplete)
			frames := postSSE(t, server, path)
			native := strings.HasPrefix(path, "/backend-api/")
			keepalives, inProgress, comments, completed := 0, 0, 0, false
			for _, frame := range frames {
				switch {
				case frame.comment != "":
					comments++
				case frame.event == "codex.keepalive" && frame.data == `{"type":"codex.keepalive"}`:
					keepalives++
				case frame.event == "response.in_progress":
					if !strings.Contains(frame.data, `"id":"local-response"`) {
						t.Fatalf("heartbeat without the real response ID: %s", frame.data)
					}
					inProgress++
				case frame.event == "response.completed":
					completed = true
				}
			}
			if !completed {
				t.Fatalf("stream did not complete: %+v", frames)
			}
			if native && (comments != 0 || keepalives == 0 || inProgress == 0) {
				t.Fatalf("native SSE heartbeats: comments=%d keepalive=%d in_progress=%d", comments, keepalives, inProgress)
			}
			if !native && (comments == 0 || keepalives != 0 || inProgress != 0) {
				t.Fatalf("generic SSE heartbeats: comments=%d keepalive=%d in_progress=%d", comments, keepalives, inProgress)
			}
		})
	}
}

// NET-25: a silent turn on any Responses WebSocket route carries protocol
// pings, never synthetic application events on the generic route.
func TestWebSocketTurnCarriesProtocolPings(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		t.Run(path, func(t *testing.T) {
			server, _ := heartbeatFixtureConfigured(t, silentThenComplete, func(handler *ProxyHandler) {
				handler.websocketKeepaliveInterval = time.Hour
				handler.websocketPingInterval = 20 * time.Millisecond
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var pings atomic.Int32
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{
				HTTPHeader:     map[string][]string{"Authorization": {"Bearer synthetic-heartbeat"}},
				OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return true },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hi"}`)); err != nil {
				t.Fatal(err)
			}
			for {
				kind, _ := heartbeatRead(t, ctx, conn)
				if kind == "response.completed" {
					break
				}
				if kind != "response.created" {
					t.Fatalf("synthetic application event: %s", kind)
				}
			}
			if pings.Load() == 0 {
				t.Fatal("silent turn carried no protocol ping")
			}
		})
	}
}

// NET-19: heartbeats follow real silence; a continuous stream gets none.
func TestHeartbeatsAreSentOnlyAfterSilence(t *testing.T) {
	server, _ := heartbeatFixtureInterval(t, continuousDeltas, 60*time.Millisecond)
	// Admission before the first delta may legitimately be silent; only
	// heartbeats between real deltas are checked.
	t.Run("sse", func(t *testing.T) {
		streaming := false
		for _, frame := range postSSE(t, server, "/backend-api/codex/responses") {
			streaming = streaming || frame.event == "response.output_text.delta"
			if streaming && frame.event != "response.completed" && frame.event != "response.output_text.delta" {
				t.Fatalf("heartbeat inserted into a continuous stream: %+v", frame)
			}
		}
		if !streaming {
			t.Fatal("no deltas")
		}
	})
	t.Run("websocket", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn := heartbeatConnect(t, ctx, server, "/backend-api/codex/responses")
		streaming := false
		for {
			kind, _ := heartbeatRead(t, ctx, conn)
			if kind == "response.completed" {
				return
			}
			streaming = streaming || kind == "response.output_text.delta"
			if streaming && kind != "response.output_text.delta" {
				t.Fatalf("heartbeat inserted into a continuous stream: %s", kind)
			}
		}
	})
}
