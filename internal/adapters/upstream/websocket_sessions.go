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
	frames         chan websocketFrame
	cancel         context.CancelFunc
	latest         string
	refs           int
	dead           bool
	retireWhenIdle bool
	last           time.Time
	timer          *time.Timer
}

// A live upstream connection is part of Responses continuation state. The
// store is bounded and isolated by provider/account/key/endpoint/credential;
// independent requests never share a response.create lock.
// ponytail: scans are bounded by 256 sockets; index idle lanes only if that cap grows.
type websocketSessions struct {
	mu        sync.Mutex
	entries   map[websocketKey][]*websocketSession
	responses map[websocketResponseKey]*websocketSession
	lanes     int
	closed    bool
	readers   sync.WaitGroup
}

func (p *websocketSessions) acquire(ctx context.Context, target Target, previous string, client *http.Client) (*websocketSession, Result, error) {
	scope := websocketScope{Owner: ownerOf(target), Endpoint: endpoint(target.BaseURL, "responses"), CredentialHash: sha256.Sum256([]byte(target.Credential)), RequiredCapability: target.RequiredCapability}
	key := websocketKey{websocketScope: scope, Session: target.SessionID}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, Result{}, errors.New("upstream transport is closed")
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
				if p.lanes >= 256 {
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
				entry = &websocketSession{key: key, gate: make(chan struct{}, 1), frames: make(chan websocketFrame, 1)}
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
			failure := &Error{Code: "request_cancelled", Status: 499, Message: "Request cancelled before upstream response creation", RejectedBeforeExecution: true, cause: ctx.Err()}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				failure.Code, failure.Status = "upstream_timeout", http.StatusGatewayTimeout
			}
			return nil, Result{}, failure
		}
		p.mu.Lock()
		actualDead := entry.dead
		lost := actualDead
		if previous != "" {
			mapped, found := p.responses[websocketResponseKey{scope, previous}]
			lost = lost || !found || mapped != entry || entry.latest != previous
		}
		connection := entry.connection
		p.mu.Unlock()
		if lost {
			p.release(entry, !actualDead, "", true)
			if previous == "" && actualDead {
				continue
			}
			return nil, Result{}, &Error{Code: ErrorCodeContinuationNotFound, Message: "Upstream session is no longer available", RejectedBeforeExecution: true}
		}
		if connection != nil {
			return entry, Result{}, nil
		}

		headers := make(http.Header)
		if target.Credential != "" {
			headers.Set("Authorization", "Bearer "+target.Credential)
		}
		copyAllowedTargetHeaders(headers, target.Headers)
		connection, response, err := websocket.Dial(ctx, websocketURL(scope.Endpoint), &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers})
		if err != nil {
			p.release(entry, false, "", true)
			if response != nil && response.StatusCode >= 400 {
				payload, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
				response.Body.Close()
				result, failure := rejectedResult(response.StatusCode, payload, target.Capabilities.Protocol)
				if readErr == nil {
					result, failure = rejectedHTTPResult(response.StatusCode, payload, target.Capabilities.Protocol)
				}
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
			return nil, Result{}, errors.New("upstream transport is closed")
		}
		entry.connection, entry.cancel = connection, cancel
		p.readers.Add(1)
		p.mu.Unlock()
		go p.read(readCtx, entry, connection)
		return entry, Result{}, nil
	}
}

func (p *websocketSessions) read(ctx context.Context, entry *websocketSession, connection *websocket.Conn) {
	defer p.readers.Done()
	defer close(entry.frames)
	defer func() { p.mu.Lock(); p.discardLocked(entry); p.mu.Unlock() }()
	for {
		kind, data, err := connection.Read(ctx)
		frame := websocketFrame{kind, data, err}
		select {
		case entry.frames <- frame:
		case <-ctx.Done():
			return
		}
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
}
