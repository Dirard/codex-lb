package httpapi

import (
	"bytes"
	"context"
	"io"

	"github.com/coder/websocket"
)

const smallWebSocketMessageBytes = 4096

// readWebSocketFrame charges large bodies of key to the shared request-body
// budget, not idle readers or short cancellation frames. Bytes beyond the small
// prefix are charged as they arrive; a message the budget cannot hold is
// discarded under the connection's read limit and reported as local capacity.
// The caller transfers release to the queued/active request or calls it on rejection.
func (p *ProxyHandler) readWebSocketFrame(ctx context.Context, connection *websocket.Conn, key string) (websocket.MessageType, []byte, func(), error) {
	release := func() {}
	kind, reader, err := connection.Reader(ctx)
	if err != nil || kind != websocket.MessageText {
		return kind, nil, release, err
	}
	prefix, err := io.ReadAll(io.LimitReader(reader, smallWebSocketMessageBytes+1))
	if err != nil || len(prefix) <= smallWebSocketMessageBytes {
		return kind, prefix, release, err
	}
	lease := p.bodies.lease(key)
	var refused error
	if lease.grow(int64(len(prefix)), true) == 0 {
		refused = p.bodies.refuse()
	} else {
		body := bytes.NewBuffer(prefix)
		_, err = body.ReadFrom(&chargedReader{source: reader, lease: lease})
		if refused = budgetRefusal(err); refused == nil {
			return kind, body.Bytes(), lease.release, err
		}
		lease.release()
	}
	// Discard only the excess message so an admitted turn on this socket survives.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return kind, nil, release, err
	}
	return kind, nil, release, refused
}
