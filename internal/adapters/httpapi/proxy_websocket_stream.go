package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

// respondWebSocket keeps a waiting Codex connected without committing the
// response prelude. The application worker writes events directly through
// websocketTurn, so emit returns only after the client accepted the frame;
// the handler goroutine only sends native heartbeats after real silence.
// onTerminal runs once, before the terminal frame is written: a client frame
// that follows the terminal then always finds the turn finalizing.
func (p *ProxyHandler) respondWebSocket(ctx context.Context, connection *websocket.Conn, options application.ResponseOptions, body json.RawMessage, onTerminal func()) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	turn := &websocketTurn{connection: connection, lastWrite: time.Now(), onTerminal: onTerminal}
	done := make(chan proxyOutcome, 1)
	workerDone := make(chan struct{})
	defer func() { cancel(); <-workerDone }()
	onResponseID := options.OnResponseID
	options.OnResponseID = func(id string) {
		if onResponseID != nil {
			onResponseID(id)
		}
		turn.setResponseID(id)
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
			if err := ctx.Err(); err != nil {
				return err
			}
			// A failed client write ends the turn: the error is returned and the
			// turn context is cancelled.
			err := turn.write(ctx, event.Data, terminalEvent(event.Type))
			if err != nil {
				cancel()
			}
			return err
		})
	}()
	return turn.wait(ctx, done, options.Codex, p.websocketKeepaliveInterval)
}

// websocketTurn is the single writer of one downstream WebSocket turn.
type websocketTurn struct {
	mu         sync.Mutex
	connection *websocket.Conn
	terminal   bool
	failed     error
	lastWrite  time.Time
	responseID string
	onTerminal func()
	terminalAt sync.Once
}

func (t *websocketTurn) setResponseID(id string) {
	t.mu.Lock()
	t.responseID = id
	t.mu.Unlock()
}

func (t *websocketTurn) writeLocked(ctx context.Context, data []byte) error {
	if t.failed != nil {
		return t.failed
	}
	writeCtx, stop := context.WithTimeout(ctx, downstreamWriteTimeout)
	err := t.connection.Write(writeCtx, websocket.MessageText, data)
	stop()
	if err != nil {
		t.failed = err
		return err
	}
	t.lastWrite = time.Now()
	return nil
}

func (t *websocketTurn) write(ctx context.Context, data []byte, terminal bool) error {
	if terminal && t.onTerminal != nil {
		t.terminalAt.Do(t.onTerminal)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.writeLocked(ctx, data); err != nil {
		return err
	}
	t.terminal = t.terminal || terminal
	return nil
}

// heartbeat sends a native application heartbeat after interval without a
// real write and returns the delay until the next check.
func (t *websocketTurn) heartbeat(ctx context.Context, interval time.Duration) (time.Duration, error) {
	if !t.mu.TryLock() {
		return interval, nil // an event write is in progress
	}
	defer t.mu.Unlock()
	if t.failed != nil {
		return 0, t.failed
	}
	if t.terminal {
		return interval, nil
	}
	if wait := interval - time.Since(t.lastWrite); wait > 0 {
		return wait, nil
	}
	data := []byte(`{"type":"codex.keepalive"}`)
	if t.responseID != "" {
		data, _ = json.Marshal(nativeInProgress(t.responseID))
	}
	return interval, t.writeLocked(ctx, data)
}

func (t *websocketTurn) state() (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal, t.failed
}

// wait runs the handler side of a turn until the worker returns, the turn is
// cancelled or a heartbeat write fails.
func (t *websocketTurn) wait(ctx context.Context, done <-chan proxyOutcome, codex bool, interval time.Duration) (bool, error) {
	if codex {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return t.cancelled(ctx)
			case outcome := <-done:
				return t.outcome(outcome)
			case <-timer.C:
				wait, err := t.heartbeat(ctx, interval)
				if err != nil {
					terminal, _ := t.state()
					return terminal, err
				}
				timer.Reset(wait)
			}
		}
	}
	select {
	case <-ctx.Done():
		return t.cancelled(ctx)
	case outcome := <-done:
		return t.outcome(outcome)
	}
}

// cancelled reports a cancelled turn, preferring the client write error that
// caused the cancellation.
func (t *websocketTurn) cancelled(ctx context.Context) (bool, error) {
	terminal, failed := t.state()
	if failed != nil {
		return terminal, failed
	}
	return terminal, ctx.Err()
}

func (t *websocketTurn) outcome(outcome proxyOutcome) (bool, error) {
	terminal, failed := t.state()
	if failed != nil {
		return terminal, failed
	}
	return terminal, outcome.err
}
