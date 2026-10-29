package upstream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type websocketScope struct {
	Owner
	Endpoint           string
	CredentialHash     [32]byte
	RequiredCapability bool
}
type websocketKey struct {
	websocketScope
	Session string
}
type websocketResponseKey struct {
	websocketScope
	Response string
}
type websocketFrame struct {
	kind websocket.MessageType
	data []byte
	err  error
}

type websocketSession struct {
	key            websocketKey
	connection     *websocket.Conn
	gate           chan struct{}
	readerDone     chan struct{}
	cancel         context.CancelFunc
	latest         string
	refs           int
	dead           bool
	retireWhenIdle bool
	last           time.Time
	timer          *time.Timer

	// Reader state shared with the leased response without p.mu.
	lease       atomic.Pointer[websocketLease]
	lastFrame   atomic.Int64 // UnixNano of the latest frame read from upstream
	delivering  atomic.Bool  // a read frame waits for the lease to accept it
	unsolicited atomic.Bool  // the peer sent a frame that no response owned
}

// websocketLease owns the frames of exactly one response.create. Frames read
// while no lease is armed, or after its response stopped reading, belong to no
// response: the reader drops them and marks the socket so every later reuse is
// fenced by a ping round trip first. The hand-off is unbuffered, so a frame is
// either accepted by the response that is still reading or attributed to none.
type websocketLease struct {
	frames chan websocketFrame
	done   chan struct{}
}

// arm routes upstream frames to a new response; call it before response.create.
func (s *websocketSession) arm() *websocketLease {
	lease := &websocketLease{frames: make(chan websocketFrame), done: make(chan struct{})}
	s.lease.Store(lease)
	return lease
}

// disarm stops routing frames to the lease before the socket can be released.
func (s *websocketSession) disarm(lease *websocketLease) {
	s.lease.CompareAndSwap(lease, nil)
	close(lease.done)
}

// websocketReuseFence bounds the liveness proof for a pooled idle socket; the
// zero value disables it.
type websocketReuseFence struct {
	after   time.Duration
	timeout time.Duration
}

// A live upstream connection is part of Responses continuation state. The
// store is bounded and isolated by provider/account/key/endpoint/credential;
// independent requests never share a response.create lock.
// ponytail: scans are bounded by 512 sockets; index idle lanes if profiling shows contention.
type websocketSessions struct {
	mu        sync.Mutex
	entries   map[websocketKey][]*websocketSession
	responses map[websocketResponseKey]*websocketSession
	lanes     int
	maxLanes  int // live session bound; 0 uses defaultWebSocketSessions
	closed    bool
	readers   sync.WaitGroup
	pings     sync.WaitGroup
}

var errTransportClosed = errors.New("upstream transport is closed")

// defaultWebSocketSessions is the measured 1 GB-host bound used when the
// runtime does not size the pool from the host.
const defaultWebSocketSessions = 512

func (p *websocketSessions) sessionLimit() int {
	if p.maxLanes > 0 {
		return p.maxLanes
	}
	return defaultWebSocketSessions
}

func (p *websocketSessions) acquire(ctx context.Context, target Target, previous string, client *http.Client, fence websocketReuseFence) (*websocketSession, Result, error) {
	scope := websocketScope{Owner: ownerOf(target), Endpoint: endpoint(target.BaseURL, "responses"), CredentialHash: sha256.Sum256([]byte(target.Credential)), RequiredCapability: target.RequiredCapability}
	key := websocketKey{websocketScope: scope, Session: target.SessionID}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, Result{}, errTransportClosed
		}
		if p.entries == nil {
			p.entries = make(map[websocketKey][]*websocketSession)
			p.responses = make(map[websocketResponseKey]*websocketSession)
		}
		if target.RequiredCapability {
			pending, retiring := false, make([]*websocketSession, 0, 2)
			for _, lanes := range p.entries {
				for _, ordinary := range lanes {
					if ordinary.key.Owner != scope.Owner || ordinary.key.AccountGeneration != scope.AccountGeneration || ordinary.key.Endpoint != scope.Endpoint || ordinary.key.RequiredCapability {
						continue
					}
					if target.SessionID == "" || ordinary.key.Session != target.SessionID {
						if previous == "" || ordinary.latest != previous {
							continue
						}
					}
					if ordinary.refs != 0 {
						pending = true
					} else {
						retiring = append(retiring, ordinary)
					}
				}
			}
			if pending {
				p.mu.Unlock()
				return nil, Result{}, &Error{Code: "capability_routing_unavailable", Status: 503, Message: "Ordinary upstream work is still pending"}
			}
			for _, ordinary := range retiring {
				p.discardLocked(ordinary)
			}
		}

		var entry *websocketSession
		if previous != "" {
			entry = p.responses[websocketResponseKey{scope, previous}]
			if entry == nil {
				p.mu.Unlock()
				return nil, Result{}, &Error{Code: ErrorCodeContinuationNotFound, Message: "Upstream session is no longer available", RejectedBeforeExecution: true}
			}
		} else {
			for _, candidate := range p.entries[key] {
				if candidate.refs == 0 && !candidate.dead {
					entry = candidate
					break
				}
			}
			if entry == nil {
				if p.lanes >= p.sessionLimit() {
					var oldest *websocketSession
					for _, lanes := range p.entries {
						for _, candidate := range lanes {
							if candidate.refs == 0 && (oldest == nil || candidate.last.Before(oldest.last)) {
								oldest = candidate
							}
						}
					}
					if oldest == nil {
						p.mu.Unlock()
						return nil, Result{}, &Error{Code: "local_capacity_exceeded", Status: 503, Message: "Upstream WebSocket session capacity reached", RejectedBeforeExecution: true}
					}
					p.discardLocked(oldest)
				}
				if key.Session == "" {
					key.Session = "request:" + rand.Text()
				}
				entry = &websocketSession{key: key, gate: make(chan struct{}, 1), readerDone: make(chan struct{})}
				p.entries[key] = append(p.entries[key], entry)
				p.lanes++
			}
		}
		entry.refs++
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		// Reserve an idle socket before unlocking so an exact continuation
		// cannot overtake an independent request that already leased it.
		acquired := false
		select {
		case entry.gate <- struct{}{}:
			acquired = true
		default:
		}
		p.mu.Unlock()

		if !acquired {
			timer := time.NewTimer(15 * time.Second)
			select {
			case entry.gate <- struct{}{}:
				acquired = true
			case <-ctx.Done():
			case <-timer.C:
				p.release(entry, true, "", false)
				return nil, Result{}, &Error{Code: "local_capacity_exceeded", Status: 503, Message: "Conversation already has an active upstream response", RejectedBeforeExecution: true}
			}
			timer.Stop()
		}
		if ctx.Err() != nil {
			p.release(entry, true, "", acquired)
			return nil, Result{}, cancelledBeforeCreate(ctx)
		}
		p.mu.Lock()
		actualDead := entry.dead
		lost := actualDead
		if previous != "" {
			mapped, found := p.responses[websocketResponseKey{scope, previous}]
			lost = lost || !found || mapped != entry || entry.latest != previous
		}
		connection, idle := entry.connection, time.Since(entry.last)
		p.mu.Unlock()
		if lost {
			p.release(entry, !actualDead, "", true)
			if previous == "" && actualDead {
				continue
			}
			return nil, Result{}, &Error{Code: ErrorCodeContinuationNotFound, Message: "Upstream session is no longer available", RejectedBeforeExecution: true}
		}
		if connection != nil {
			err := p.fenceReuse(ctx, entry, connection, idle, fence)
			if err == nil {
				return entry, Result{}, nil
			}
			if ctx.Err() != nil {
				p.release(entry, true, "", true)
				return nil, Result{}, cancelledBeforeCreate(ctx)
			}
			// response.create was not written, so replacing the socket is safe;
			// an exact continuation cannot move to another connection.
			p.release(entry, false, "", true)
			if previous == "" {
				continue
			}
			return nil, Result{}, &Error{Code: ErrorCodeContinuationNotFound, Message: "Upstream session is no longer available", RejectedBeforeExecution: true}
		}

		headers := make(http.Header)
		if target.Credential != "" {
			headers.Set("Authorization", "Bearer "+target.Credential)
		}
		copyAllowedTargetHeaders(headers, target.Headers)
		connection, response, err := websocket.Dial(ctx, scope.Endpoint, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers})
		if err != nil {
			p.release(entry, false, "", true)
			if response != nil && response.StatusCode >= 400 {
				payload, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
				response.Body.Close()
				result, failure := rejectedResult(response.StatusCode, payload, target.Capabilities.Protocol)
				if readErr == nil {
					result, failure = rejectedHTTPResult(response.StatusCode, payload, target.Capabilities.Protocol)
				}
				classifyImageLimit(response.StatusCode, response.Header, &result, failure)
				var classified *Error
				if errors.As(failure, &classified) {
					protocolRejection := classified.Code == "http_426" || classified.Code == "upgrade_required" || classified.Code == "http_403"
					classified.WebSocketHTTPFallback = protocolRejection && websocketHTTPFallback(response, payload)
				}
				return nil, result, failure
			}
			return nil, Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: "Upstream WebSocket handshake failed"})
		}
		connection.SetReadLimit(maxSSEEventBytes)
		readCtx, cancel := context.WithCancel(context.Background())
		p.mu.Lock()
		if entry.dead || p.closed {
			p.mu.Unlock()
			cancel()
			connection.CloseNow()
			p.release(entry, false, "", true)
			return nil, Result{}, errTransportClosed
		}
		entry.connection, entry.cancel = connection, cancel
		p.readers.Add(1)
		p.mu.Unlock()
		go p.read(readCtx, entry, connection)
		return entry, Result{}, nil
	}
}

func cancelledBeforeCreate(ctx context.Context) error {
	failure := &Error{Code: "request_cancelled", Status: 499, Message: "Request cancelled before upstream response creation", RejectedBeforeExecution: true, cause: ctx.Err()}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		failure.Code, failure.Status = "upstream_timeout", http.StatusGatewayTimeout
	}
	return failure
}

// fenceReuse proves that a pooled socket can carry a new response.create.
// It runs under the lease, without p.mu and before the lease is armed, so the
// reader drops anything the peer sends and still processes the pong. A socket
// that ever sent unsolicited frames is always fenced: the pong also proves
// every earlier peer frame was read and dropped. Recently used clean sockets
// skip the round trip.
func (p *websocketSessions) fenceReuse(ctx context.Context, entry *websocketSession, connection *websocket.Conn, idle time.Duration, fence websocketReuseFence) error {
	if fence.timeout <= 0 || !entry.unsolicited.Load() && idle < fence.after {
		return nil
	}
	select {
	case err := <-p.ping(connection, fence.timeout):
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// read is the only reader of an upstream socket. It hands frames to the armed
// lease and never blocks while the socket is idle, so control frames (pong,
// close) are processed between responses.
func (p *websocketSessions) read(ctx context.Context, entry *websocketSession, connection *websocket.Conn) {
	defer p.readers.Done()
	defer close(entry.readerDone)
	defer func() { p.mu.Lock(); p.discardLocked(entry); p.mu.Unlock() }()
	for {
		kind, data, err := connection.Read(ctx)
		if err == nil {
			entry.lastFrame.Store(time.Now().UnixNano())
		}
		lease := entry.lease.Load()
		if lease == nil {
			if err != nil {
				return
			}
			entry.unsolicited.Store(true)
			continue
		}
		entry.delivering.Store(true)
		select {
		case lease.frames <- websocketFrame{kind, data, err}:
		case <-lease.done:
			if err == nil {
				entry.unsolicited.Store(true)
			}
		case <-ctx.Done():
			entry.delivering.Store(false)
			return
		}
		entry.delivering.Store(false)
		if err != nil {
			return
		}
	}
}

func (p *websocketSessions) release(entry *websocketSession, keep bool, responseID string, acquired bool) {
	p.mu.Lock()
	entry.refs--
	if !keep && acquired {
		p.discardLocked(entry)
	}
	if entry.retireWhenIdle && entry.refs == 0 {
		p.discardLocked(entry)
	}
	if !entry.dead && responseID != "" {
		if entry.latest != "" {
			oldKey := websocketResponseKey{entry.key.websocketScope, entry.latest}
			if p.responses[oldKey] == entry {
				delete(p.responses, oldKey)
			}
		}
		entry.latest = responseID
		p.responses[websocketResponseKey{entry.key.websocketScope, responseID}] = entry
	}
	if acquired {
		<-entry.gate
		entry.last = time.Now()
	}
	if !entry.dead && entry.refs == 0 {
		entry.last = time.Now()
		if entry.connection == nil {
			p.discardLocked(entry)
		} else {
			entry.timer = time.AfterFunc(30*time.Minute, func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				if entry.refs == 0 {
					p.discardLocked(entry)
				}
			})
		}
	}
	p.mu.Unlock()
}

func (a *HTTPAdapter) RetireRequiredCapability(owner Owner) {
	a.sessions.mu.Lock()
	defer a.sessions.mu.Unlock()
	for _, lanes := range a.sessions.entries {
		// Discard shortens this slice; walk backwards to visit every sibling.
		for i := len(lanes) - 1; i >= 0; i-- {
			entry := lanes[i]
			if entry.key.ProviderID != owner.ProviderID || entry.key.AccountID != owner.AccountID || entry.key.KeyID != owner.KeyID || !entry.key.RequiredCapability {
				continue
			}
			entry.retireWhenIdle = true
			if entry.refs == 0 {
				a.sessions.discardLocked(entry)
			}
		}
	}
}

func (p *websocketSessions) discardLocked(entry *websocketSession) {
	if entry.dead {
		return
	}
	entry.dead = true
	lanes := p.entries[entry.key]
	for i, candidate := range lanes {
		if candidate != entry {
			continue
		}
		lanes = slices.Delete(lanes, i, i+1)
		if len(lanes) == 0 {
			delete(p.entries, entry.key)
		} else {
			p.entries[entry.key] = lanes
		}
		break
	}
	p.lanes--
	if entry.latest != "" {
		responseKey := websocketResponseKey{entry.key.websocketScope, entry.latest}
		if p.responses[responseKey] == entry {
			delete(p.responses, responseKey)
		}
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.connection != nil {
		entry.connection.CloseNow()
	}
}

func (p *websocketSessions) close() {
	p.mu.Lock()
	p.closed = true
	entries := make([]*websocketSession, 0, p.lanes)
	for _, lanes := range p.entries {
		entries = append(entries, lanes...)
	}
	for _, entry := range entries {
		p.discardLocked(entry)
	}
	p.mu.Unlock()
	p.readers.Wait()
	p.pings.Wait()
}
