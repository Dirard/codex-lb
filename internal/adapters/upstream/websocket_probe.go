package upstream

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// probeWebSocket checks transport liveness without treating pong as model progress.
// Stopping the probe never cancels an in-flight ping: coder/websocket closes the
// whole connection when a write's context ends mid-frame, which would destroy a
// healthy owner socket right after a normal completion. Such a ping finishes on
// its own deadline and Close waits for it.
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
			}
			result := make(chan error, 1)
			a.pings.Add(1)
			go func() {
				defer a.pings.Done()
				pingCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), a.websocketPingTimeout)
				defer stop()
				result <- connection.Ping(pingCtx)
			}()
			select {
			case <-ctx.Done():
				return
			case err := <-result:
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
