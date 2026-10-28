package upstream

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// probeWebSocket checks transport liveness without treating pong as model
// progress. Any frame read from the peer also proves the transport is alive, so
// a recently active socket is not pinged. A pong can be read only by the
// socket's reader; while that reader waits for a slow consumer to accept a
// frame the probe cannot fail, because the downstream write bound, not the
// upstream transport, limits that wait. Stopping the probe never waits for a
// pong and never cancels an in-flight ping (see websocketSessions.ping).
func (a *HTTPAdapter) probeWebSocket(ctx context.Context, session *websocketSession) (<-chan error, func()) {
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
				if time.Since(time.Unix(0, session.lastFrame.Load())) < a.websocketPingInterval {
					continue
				}
				sent := time.Now().UnixNano()
				var err error
				select {
				case err = <-a.sessions.ping(session.connection, a.websocketPingTimeout):
				case <-ctx.Done():
					return
				}
				if err == nil || session.delivering.Load() || session.lastFrame.Load() >= sent {
					continue
				}
				if ctx.Err() == nil {
					errors <- &Error{Code: ErrorCodeConnection, Message: "Upstream WebSocket did not answer a liveness probe"}
				}
				return
			}
		}
	}()
	return errors, func() { cancel(); <-done }
}

// ping sends one protocol ping detached from the caller's cancellation.
// coder/websocket closes the whole connection when the context of a
// control-frame write is cancelled during that write, so ending a lease must
// never cancel its in-flight ping: the ping is bounded only by its own timeout
// and is joined by close(). Callers stop waiting on their own cancellation and
// the late result is discarded by the buffered channel.
func (p *websocketSessions) ping(connection *websocket.Conn, timeout time.Duration) <-chan error {
	result := make(chan error, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		result <- errTransportClosed
		return result
	}
	p.pings.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.pings.Done()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result <- connection.Ping(ctx)
	}()
	return result
}
