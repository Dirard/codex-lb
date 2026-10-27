package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/application"
)

// downstreamWriteTimeout bounds one downstream SSE/WebSocket write.
const downstreamWriteTimeout = 30 * time.Second

type proxyOutcome struct {
	result application.ResponseResult
	err    error
}

// stream relays one Responses SSE response. The application worker writes
// events directly through sseStream, so emit returns only after the event was
// written and flushed; the handler goroutine only sends heartbeats and the
// final [DONE]/error frame after the worker has returned.
func (p *ProxyHandler) stream(w http.ResponseWriter, r *http.Request, options application.ResponseOptions, body json.RawMessage) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := &sseStream{w: w, controller: http.NewResponseController(w), codex: options.Codex, lastWrite: time.Now()}
	done := make(chan proxyOutcome, 1)
	dispatched := make(chan struct{})
	options.OnDispatch = sync.OnceFunc(func() { close(dispatched) })
	if options.Codex {
		onResponseID := options.OnResponseID
		options.OnResponseID = func(id string) {
			if onResponseID != nil {
				onResponseID(id)
			}
			out.setResponseID(id)
		}
	}
	ready := (<-chan struct{})(dispatched)
	go func() {
		outcome := proxyOutcome{}
		defer func() {
			if recover() != nil {
				outcome.err = errors.New("proxy worker failed")
			}
			done <- outcome
		}()
		outcome.result, outcome.err = p.proxy.Respond(ctx, options, body, func(event application.ResponseEvent) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return out.event(event)
		})
	}()
	// The heartbeat is due only after real silence; it never runs before
	// dispatch so capacity waits keep their HTTP status and Retry-After.
	interval := p.websocketKeepaliveInterval
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()
	stop := func() { cancel(); <-done }
	for {
		select {
		case <-ready:
			ready = nil
			heartbeat.Reset(out.untilHeartbeat(interval))
		case <-ctx.Done():
			stop()
			return
		case outcome := <-done:
			out.finish(outcome)
			return
		case <-heartbeat.C:
			if ready != nil {
				continue
			}
			wait, err := out.heartbeat(interval)
			if err != nil {
				stop()
				return
			}
			heartbeat.Reset(wait)
		}
	}
}

// sseStream is the single writer of one SSE response. Writes are serialized,
// the first failed write ends all later writes, and lastWrite drives
// silence-based heartbeats.
type sseStream struct {
	mu         sync.Mutex
	w          http.ResponseWriter
	controller *http.ResponseController
	codex      bool
	buffer     []byte
	started    bool
	terminal   bool
	failed     error
	lastWrite  time.Time
	responseID string
}

func (s *sseStream) setResponseID(id string) {
	s.mu.Lock()
	s.responseID = id
	s.mu.Unlock()
}

func (s *sseStream) startLocked() {
	if s.started {
		return
	}
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache, no-store")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
}

func (s *sseStream) writeLocked(frame []byte) error {
	if s.failed != nil {
		return s.failed
	}
	s.startLocked()
	_ = s.controller.SetWriteDeadline(time.Now().Add(downstreamWriteTimeout))
	_, err := s.w.Write(frame)
	if err == nil {
		err = s.controller.Flush()
	}
	if err != nil {
		s.failed = err
		return err
	}
	s.lastWrite = time.Now()
	return nil
}

// event writes one upstream event; it is called by the application worker.
func (s *sseStream) event(event application.ResponseEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	frame, err := appendSSE(s.buffer[:0], event)
	if err != nil {
		s.failed = err
		return err
	}
	if cap(frame) <= 64<<10 {
		s.buffer = frame // keep a small scratch buffer; drop one grown by a large event
	}
	if err := s.writeLocked(frame); err != nil {
		return err
	}
	s.terminal = s.terminal || terminalEvent(event.Type)
	return nil
}

func (s *sseStream) untilHeartbeat(interval time.Duration) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(0, interval-time.Since(s.lastWrite))
}

// heartbeat writes a heartbeat after interval without a real write and returns
// the delay until the next check. Native Codex SSE uses data events because
// the Codex client ignores SSE comments when resetting its idle timer.
func (s *sseStream) heartbeat(interval time.Duration) (time.Duration, error) {
	if !s.mu.TryLock() {
		return interval, nil // an event write is in progress
	}
	defer s.mu.Unlock()
	if s.failed != nil {
		return 0, s.failed
	}
	if s.terminal {
		return interval, nil
	}
	if wait := interval - time.Since(s.lastWrite); wait > 0 {
		return wait, nil
	}
	frame := []byte(": keepalive\n\n")
	if s.codex {
		frame = nativeHeartbeat(s.responseID)
	}
	return interval, s.writeLocked(frame)
}

// nativeHeartbeat is a Codex-visible SSE event that carries no output or usage.
func nativeHeartbeat(responseID string) []byte {
	if responseID == "" {
		return []byte("event: codex.keepalive\ndata: {\"type\":\"codex.keepalive\"}\n\n")
	}
	data, _ := json.Marshal(nativeInProgress(responseID))
	return append(append([]byte("event: response.in_progress\ndata: "), data...), "\n\n"...)
}

type inProgressEvent struct {
	Type     string `json:"type"`
	Response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"response"`
}

func nativeInProgress(responseID string) inProgressEvent {
	var event inProgressEvent
	event.Type, event.Response.ID, event.Response.Status = "response.in_progress", responseID, "in_progress"
	return event
}

// finish writes the final [DONE] marker or a stream error after the worker
// returned. It writes nothing after a failed downstream write.
func (s *sseStream) finish(outcome proxyOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	if outcome.err == nil && s.terminal && outcome.result.DoneMarker {
		_ = s.writeLocked([]byte("data: [DONE]\n\n"))
	}
	if outcome.err == nil || s.terminal {
		return
	}
	if !s.started {
		writeProxyError(s.w, outcome.err)
		return
	}
	_, code, message := proxyError(outcome.err)
	payload, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Type: "error", Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}})
	if frame, err := appendSSE(nil, application.ResponseEvent{Type: "error", Data: payload}); err == nil {
		_ = s.writeLocked(frame)
	}
}

func terminalEvent(kind string) bool {
	return kind == "response.completed" || kind == "response.failed" || kind == "response.incomplete"
}

// appendSSE appends one SSE frame. Event data was validated as JSON when it
// entered the proxy; only data containing CR or LF is compacted, because a raw
// line break would split the SSE frame.
func appendSSE(dst []byte, event application.ResponseEvent) ([]byte, error) {
	if len(event.Type) > 128 || strings.ContainsAny(event.Type, "\r\n") {
		return dst, errors.New("invalid upstream event type")
	}
	data := []byte(event.Data)
	if bytes.IndexByte(data, '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return dst, errors.New("invalid upstream event JSON")
		}
		data = compact.Bytes()
	}
	dst = append(dst, "event: "...)
	dst = append(dst, event.Type...)
	dst = append(dst, "\ndata: "...)
	dst = append(dst, data...)
	return append(dst, "\n\n"...), nil
}
