package httpapi

import (
	"bytes"
	"context"
	"io"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

const smallWebSocketMessageBytes = 4096

// readWebSocketFrame charges large bodies, not idle readers or short cancellation frames.
// The caller transfers release to the queued/active request or calls it on rejection.
func (p *ProxyHandler) readWebSocketFrame(ctx context.Context, connection *websocket.Conn) (websocket.MessageType, []byte, func(), error) {
	release := func() {}
	kind, reader, err := connection.Reader(ctx)
	if err != nil || kind != websocket.MessageText {
		return kind, nil, release, err
	}
	prefix, err := io.ReadAll(io.LimitReader(reader, smallWebSocketMessageBytes+1))
	if err != nil || len(prefix) <= smallWebSocketMessageBytes {
		return kind, prefix, release, err
	}
	select {
	case p.websocketBodies <- struct{}{}:
	default:
		// Discard only the excess frame so an admitted turn on this socket survives.
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return kind, nil, release, err
		}
		return kind, nil, release, &application.ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Too many pending large WebSocket messages"}
	}
	release = func() { <-p.websocketBodies }
	body := bytes.NewBuffer(prefix)
	_, err = body.ReadFrom(reader)
	return kind, body.Bytes(), release, err
}
