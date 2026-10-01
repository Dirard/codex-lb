package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/application"
)

type dispatchedEvent struct {
	event application.ResponseEvent
	ack   chan error
}

type proxyOutcome struct {
	result application.ResponseResult
	err    error
}

func (p *ProxyHandler) stream(w http.ResponseWriter, r *http.Request, options application.ResponseOptions, body json.RawMessage) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := make(chan dispatchedEvent)
	done := make(chan proxyOutcome, 1)
	dispatched := make(chan struct{})
	options.OnDispatch = sync.OnceFunc(func() { close(dispatched) })
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
			message := dispatchedEvent{event: event, ack: make(chan error, 1)}
			select {
			case events <- message:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case err := <-message.ack:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	keepalive := time.NewTicker(10 * time.Second)
	defer keepalive.Stop()
	controller := http.NewResponseController(w)
	started, terminal := false, false
	start := func() {
		if started {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		started = true
	}
	stop := func() { cancel(); <-done }
	for {
		select {
		case <-ready:
			ready = nil
		case <-ctx.Done():
			stop()
			return
		case message := <-events:
			start()
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := writeSSE(w, message.event)
			if err == nil {
				err = controller.Flush()
			}
			message.ack <- err
			if err != nil {
				stop()
				return
			}
			terminal = terminal || terminalEvent(message.event.Type)
		case outcome := <-done:
			if outcome.err == nil && terminal && outcome.result.DoneMarker {
				_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				_ = controller.Flush()
			}
			if outcome.err != nil && !terminal {
				if !started {
					writeProxyError(w, outcome.err)
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
				_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
				_ = writeSSE(w, application.ResponseEvent{Type: "error", Data: payload})
				_ = controller.Flush()
			}
			return
		case <-keepalive.C:
			if ready != nil {
				continue // Capacity wait must retain the HTTP error status and Retry-After.
			}
			start()
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				stop()
				return
			}
			if err := controller.Flush(); err != nil {
				stop()
				return
			}
		}
	}
}

func terminalEvent(kind string) bool {
	return kind == "response.completed" || kind == "response.failed" || kind == "response.incomplete"
}

func writeSSE(w http.ResponseWriter, event application.ResponseEvent) error {
	if len(event.Type) > 128 || strings.ContainsAny(event.Type, "\r\n") {
		return errors.New("invalid upstream event type")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, event.Data); err != nil {
		return errors.New("invalid upstream event JSON")
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, compact.Bytes())
	return err
}
