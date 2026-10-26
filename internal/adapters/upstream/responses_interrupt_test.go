package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const interruptCreated = `{"type":"response.created","response":{"id":"interrupted","status":"in_progress"}}`
const interruptAccepted = `{"type":"response.interrupt.accepted","response_id":"interrupted"}`
const interruptRejected = `{"type":"response.interrupt.failed","response_id":"interrupted","error":{"code":"cannot_interrupt","message":"Already finishing"}}`
const interruptTerminal = `{"type":"response.incomplete","response":{"id":"interrupted","status":"incomplete","output":[],"incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`
const interruptNextTerminal = `{"type":"response.completed","response":{"id":"next","status":"completed","output":[],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}`

func writeInterruptEvent(t *testing.T, ctx context.Context, connection *websocket.Conn, frame string) bool {
	t.Helper()
	if err := connection.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Error(err)
		return false
	}
	return true
}

func TestResponsesInterruptAcknowledgementLifecycle(t *testing.T) {
	for _, name := range []string{"accepted_first", "rejected_first", "terminal_first", "terminal_before_rejection", "mismatched_ack", "closed_without_ack", "ack_timeout", "cancel_after_terminal"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			callCtx, cancelCall := context.WithCancel(ctx)
			defer cancelCall()
			var handshakes, creates atomic.Int32
			terminalSeen := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer connection.CloseNow()
				handshakes.Add(1)
				for {
					_, body, err := connection.Read(ctx)
					if err != nil {
						return
					}
					var frame struct {
						Type       string `json:"type"`
						ResponseID string `json:"response_id"`
						Mode       string `json:"mode"`
					}
					if json.Unmarshal(body, &frame) != nil || frame.Type != "response.create" {
						t.Errorf("unexpected create: %s", body)
						return
					}
					if creates.Add(1) != 1 {
						if !writeInterruptEvent(t, ctx, connection, interruptNextTerminal) {
							return
						}
						continue
					}
					if !writeInterruptEvent(t, ctx, connection, interruptCreated) {
						return
					}
					_, body, err = connection.Read(ctx)
					if err != nil || json.Unmarshal(body, &frame) != nil || frame.Type != "response.interrupt" || frame.ResponseID != "interrupted" || frame.Mode != "discard_partial_items" {
						t.Errorf("unexpected interrupt: %s (%v)", body, err)
						return
					}
					if name == "accepted_first" || name == "rejected_first" {
						ack := interruptAccepted
						if name == "rejected_first" {
							ack = interruptRejected
						}
						if !writeInterruptEvent(t, ctx, connection, ack) || !writeInterruptEvent(t, ctx, connection, interruptTerminal) {
							return
						}
						continue
					}
					if !writeInterruptEvent(t, ctx, connection, interruptTerminal) {
						return
					}
					select {
					case <-terminalSeen:
					case <-ctx.Done():
						return
					}
					switch name {
					case "terminal_first":
						writeInterruptEvent(t, ctx, connection, interruptAccepted)
					case "terminal_before_rejection":
						writeInterruptEvent(t, ctx, connection, interruptRejected)
					case "mismatched_ack":
						writeInterruptEvent(t, ctx, connection, strings.ReplaceAll(interruptAccepted, "interrupted", "another-response"))
					case "closed_without_ack":
						return
					}
				}
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			capabilities := ResponsesCapabilities()
			capabilities.StreamTransport = TransportWebSocket
			target := testTarget(server.URL, capabilities)
			target.SessionID = "interrupt-session"
			var queue chan<- string
			target.OnInterruptReady = func(ready chan<- string) {
				if ready == nil && queue != nil {
					close(terminalSeen)
					if name == "cancel_after_terminal" {
						cancelCall()
					}
				}
				queue = ready
			}
			var kinds []string
			result, err := adapter.OpenStream(callCtx, target, Request{Body: []byte(`{"model":"m","input":"first","stream":true}`)}, func(event Event) error {
				kinds = append(kinds, event.Type)
				if event.Type == "response.created" {
					queue <- "interrupted"
				}
				return nil
			})
			if !result.UsageKnown || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 || result.ResponseID != "interrupted" {
				t.Fatalf("terminal usage lost: %+v (%v)", result, err)
			}
			reusable := name == "accepted_first" || name == "rejected_first" || name == "terminal_first" || name == "terminal_before_rejection"
			want := []string{"response.created", "response.interrupt.accepted", "response.incomplete"}
			switch name {
			case "mismatched_ack":
				var upstreamErr *Error
				if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeInvalidStream {
					t.Fatalf("mismatched ACK error = %v", err)
				}
				want = []string{"response.created"}
			case "cancel_after_terminal":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error = %v", err)
				}
				want = []string{"response.created"}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if name != "accepted_first" && name != "terminal_first" {
					want[1] = "response.interrupt.failed"
				}
			}
			if !reflect.DeepEqual(kinds, want) || queue != nil {
				t.Fatalf("events=%v want=%v control still registered=%v", kinds, want, queue != nil)
			}
			adapter.sessions.mu.Lock()
			lanes := adapter.sessions.lanes
			adapter.sessions.mu.Unlock()
			if reusable && lanes != 1 || !reusable && lanes != 0 {
				t.Fatalf("pool lanes=%d reusable=%v", lanes, reusable)
			}
			target.OnInterruptReady = nil
			body := json.RawMessage(`{"model":"m","input":"next","stream":true}`)
			if reusable {
				body = json.RawMessage(`{"model":"m","input":"next","stream":true,"previous_response_id":"interrupted"}`)
			}
			kinds = nil
			next, err := adapter.OpenStream(ctx, target, Request{Body: body}, func(event Event) error {
				kinds = append(kinds, event.Type)
				return nil
			})
			wantSockets := int32(2)
			if reusable {
				wantSockets = 1
			}
			if err != nil || next.ResponseID != "next" || handshakes.Load() != wantSockets || !reflect.DeepEqual(kinds, []string{"response.completed"}) {
				t.Fatalf("next response inherited old events: result=%+v sockets=%d events=%v err=%v", next, handshakes.Load(), kinds, err)
			}
		})
	}
}

func TestResponsesInterruptQueuedAtTerminalIsNotSentOnReusedSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var handshakes, creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		handshakes.Add(1)
		for {
			_, body, err := connection.Read(ctx)
			if err != nil {
				return
			}
			var frame struct{ Type string }
			if json.Unmarshal(body, &frame) != nil || frame.Type != "response.create" {
				t.Errorf("queued control leaked into reused socket: %s", body)
				return
			}
			terminal := interruptNextTerminal
			if creates.Add(1) == 1 {
				terminal = interruptTerminal
			}
			if !writeInterruptEvent(t, ctx, connection, terminal) {
				return
			}
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	var queue chan<- string
	target.OnInterruptReady = func(ready chan<- string) {
		if ready == nil && queue != nil {
			// Simulate ingress enqueue immediately before terminal revokes registration.
			queue <- "interrupted"
		}
		queue = ready
	}
	var kinds []string
	result, err := adapter.OpenStream(ctx, target, Request{Body: []byte(`{"model":"m","input":"first","stream":true}`)}, func(event Event) error {
		kinds = append(kinds, event.Type)
		if event.Type == "response.interrupt.failed" && !strings.Contains(string(event.Data), "response_already_ended") {
			t.Errorf("unexpected failure: %s", event.Data)
		}
		return nil
	})
	if err != nil || !result.UsageKnown || !reflect.DeepEqual(kinds, []string{"response.interrupt.failed", "response.incomplete"}) {
		t.Fatalf("queued interruption: result=%+v events=%v err=%v", result, kinds, err)
	}
	target.OnInterruptReady = nil
	_, err = adapter.OpenStream(ctx, target, Request{Body: []byte(`{"model":"m","input":"next","stream":true,"previous_response_id":"interrupted"}`)}, func(Event) error { return nil })
	if err != nil || handshakes.Load() != 1 || creates.Load() != 2 {
		t.Fatalf("socket was not reused safely: sockets=%d creates=%d err=%v", handshakes.Load(), creates.Load(), err)
	}
}

func TestResponsesInterruptForeignAndDuplicateControlsStayLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	duplicateRejected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		if _, _, err := connection.Read(ctx); err != nil || !writeInterruptEvent(t, ctx, connection, interruptCreated) {
			return
		}
		_, body, err := connection.Read(ctx)
		var control struct {
			Type       string `json:"type"`
			ResponseID string `json:"response_id"`
		}
		if err != nil || json.Unmarshal(body, &control) != nil || control.Type != "response.interrupt" || control.ResponseID != "interrupted" {
			t.Errorf("foreign control reached upstream: %s (%v)", body, err)
			return
		}
		if !writeInterruptEvent(t, ctx, connection, interruptAccepted) {
			return
		}
		select {
		case <-duplicateRejected:
		case <-ctx.Done():
			return
		}
		if !writeInterruptEvent(t, ctx, connection, interruptTerminal) {
			return
		}
		if _, body, err := connection.Read(ctx); err == nil {
			t.Errorf("duplicate control reached upstream: %s", body)
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	var queue chan<- string
	target.OnInterruptReady = func(ready chan<- string) { queue = ready }
	failures := 0
	result, err := adapter.OpenStream(ctx, target, Request{Body: []byte(`{"model":"m","input":"first","stream":true}`)}, func(event Event) error {
		switch event.Type {
		case "response.created":
			queue <- "foreign"
		case "response.interrupt.accepted":
			queue <- "interrupted"
		case "response.interrupt.failed":
			failures++
			if failures == 1 {
				queue <- "interrupted"
			} else if failures == 2 {
				close(duplicateRejected)
			}
		}
		return nil
	})
	if err != nil || !result.UsageKnown || failures != 2 {
		t.Fatalf("local control rejection: result=%+v failures=%d err=%v", result, failures, err)
	}
}
