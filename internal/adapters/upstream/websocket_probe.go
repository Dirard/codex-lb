package upstream

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// probeWebSocket checks transport liveness without treating pong as model progress.
func (a *HTTPAdapter) probeWebSocket(ctx context.Context, connection *websocket.Conn) (<-chan error, func()) {
	ctx, cancel := context.WithCancel(ctx)
	errors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(a.websocketPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, a.websocketPingTimeout)
				err := connection.Ping(pingCtx)
				stop()
				if err != nil {
					if ctx.Err() == nil {
						errors <- &Error{Code: ErrorCodeConnection, Message: "Upstream WebSocket did not answer a liveness probe"}
					}
					return
				}
			}
		}
	}()
	return errors, func() { cancel(); <-done }
}
