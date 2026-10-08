package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

// respondWebSocket keeps a waiting Codex connected without committing the response prelude.
func (p *ProxyHandler) respondWebSocket(ctx context.Context, connection *websocket.Conn, options application.ResponseOptions, body json.RawMessage) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan dispatchedEvent)
	responseIDs := make(chan string)
	done := make(chan proxyOutcome, 1)
	workerDone := make(chan struct{})
	defer func() { cancel(); <-workerDone }()
	var ticks <-chan time.Time
	if options.Codex {
		ticker := time.NewTicker(p.websocketKeepaliveInterval)
		defer ticker.Stop()
		ticks = ticker.C
		options.OnResponseID = func(id string) {
			select {
			case responseIDs <- id:
			case <-ctx.Done():
			}
		}
	}
	go func() {
		defer close(workerDone)
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
	write := func(data []byte) error {
		writeCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		return connection.Write(writeCtx, websocket.MessageText, data)
	}
	terminal, responseID := false, ""
	for {
		select {
		case <-ctx.Done():
			return terminal, ctx.Err()
		case responseID = <-responseIDs:
		case message := <-events:
			err := write(message.event.Data)
			message.ack <- err
			if err != nil {
				return terminal, err
			}
			terminal = terminal || terminalEvent(message.event.Type)
		case outcome := <-done:
			return terminal, outcome.err
		case <-ticks:
			if terminal {
				continue
			}
			data := []byte(`{"type":"codex.keepalive"}`)
			if responseID != "" {
				var event struct {
					Type     string `json:"type"`
					Response struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"response"`
				}
				event.Type, event.Response.ID, event.Response.Status = "response.in_progress", responseID, "in_progress"
				data, _ = json.Marshal(event)
			}
			if err := write(data); err != nil {
				return terminal, err
			}
		}
	}
}
