package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/wirejson"
	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func (p *ProxyHandler) websocket(w http.ResponseWriter, r *http.Request) {
	key, err := p.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if !p.writeQuotaHeaders(w, r, key) {
		return
	}
	turnState := conversationHeader(r, "X-Codex-Turn-State")
	synthesizedTurnState := turnState == ""
	if turnState == "" {
		turnState = "turn_" + rand.Text()
	}
	w.Header().Set("X-Codex-Turn-State", turnState)
	select {
	case p.websockets <- struct{}{}:
		defer func() { <-p.websockets }()
	default:
		writeProxyError(w, &application.ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Too many open client WebSockets"})
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(32 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	type createFrame struct {
		body    json.RawMessage
		route   application.CapabilityRoute
		release func()
	}
	messages := make(chan createFrame, 1)
	readerDone := make(chan struct{})
	var activeMu sync.Mutex
	var active context.CancelFunc
	var activeRequired bool
	// activeTerminal: the active turn already sent its terminal frame and only
	// finalizes; it can no longer be changed by a following frame.
	var activeTerminal bool
	var activeResponseID string
	var activeInterrupts chan<- string
	go func() {
		defer close(readerDone)
		defer cancel()
		for {
			kind, body, releaseBody, err := p.readWebSocketFrame(ctx, connection, key.ID)
			if err != nil {
				releaseBody()
				var capacity *application.ProxyError
				if errors.As(err, &capacity) {
					if sendWebSocketError(ctx, connection, err) == nil {
						continue
					}
				}
				return
			}
			if kind != websocket.MessageText {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Binary Responses WebSocket frames are not supported"})
				return
			}
			current, err := p.authenticate(r.WithContext(ctx))
			if err != nil || current.ID != key.ID {
				releaseBody()
				if err == nil {
					err = invalidKey()
				}
				_ = sendWebSocketError(ctx, connection, err)
				return
			}
			var object map[string]json.RawMessage
			var eventType string
			if json.Unmarshal(body, &object) != nil || object == nil || json.Unmarshal(object["type"], &eventType) != nil {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses WebSocket frame"})
				continue
			}
			_, parseErr := application.ParseCapabilitySignal(application.CapabilitySignalRequest{Transport: application.CapabilityTransportWebSocket, FrameType: eventType, APIKeyID: key.ID, Body: body})
			if parseErr != nil {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, parseErr)
				continue
			}
			if eventType == "response.interrupt" {
				responseID, err := parseWebSocketInterrupt(object)
				releaseBody()
				if err != nil {
					_ = sendWebSocketError(ctx, connection, err)
					continue
				}
				sent := false
				activeMu.Lock()
				if activeInterrupts != nil && responseID == activeResponseID {
					select {
					case activeInterrupts <- responseID:
						sent = true
					default:
					}
				}
				activeMu.Unlock()
				if !sent {
					_ = sendWebSocketInterruptFailure(ctx, connection, responseID)
				}
				continue
			}
			if eventType != "response.create" {
				releaseBody()
				if eventType != "response.cancel" {
					_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "unsupported_websocket_event", Status: 400, Message: "Only response.create, response.cancel and response.interrupt are supported"})
					continue
				}
				activeMu.Lock()
				if active != nil {
					active()
				}
				activeMu.Unlock()
				continue
			}
			options := p.responseOptions(r, key.ID)
			options.Transport = application.CapabilityTransportWebSocket
			options.TurnState = turnState
			options.SynthesizedTurnState = synthesizedTurnState
			signal, route, err := p.proxy.PrepareCapability(ctx, options, body)
			if err != nil {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, err)
				continue
			}
			activeMu.Lock()
			conflict := active != nil && !activeTerminal && route.RequireSecurityWorkAuthorized && !activeRequired
			activeMu.Unlock()
			if conflict {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "capability_routing_unavailable", Status: 503, Message: "Required capability cannot be selected while ordinary work is pending"})
				continue
			}
			body, err = websocketCreateBody(object, body, signal.Payload)
			if err != nil {
				releaseBody()
				_ = sendWebSocketError(ctx, connection, err)
				continue
			}
			select {
			case messages <- createFrame{body: body, route: route, release: releaseBody}:
			case <-ctx.Done():
				releaseBody()
				return
			default:
				releaseBody()
				_ = connection.Close(websocket.StatusPolicyViolation, "Too many queued response.create frames")
				return
			}
		}
	}()
	var turnActive atomic.Bool
	pingerDone := make(chan struct{})
	go func() {
		defer close(pingerDone)
		interval := p.websocketPingInterval
		if interval <= 0 {
			interval = downstreamPingInterval
		}
		pingWhileActive(ctx, connection, interval, &turnActive)
	}()
	var releaseActiveBody func()
	defer func() {
		cancel()
		connection.CloseNow()
		<-readerDone
		<-pingerDone
		if releaseActiveBody != nil {
			releaseActiveBody()
		}
		select {
		case pending := <-messages:
			pending.release()
		default:
		}
	}()
	idle := time.NewTimer(120 * time.Second)
	defer idle.Stop()
	var liteState application.ResponsesLiteState
	for {
		// A closing process ends the session between turns. The separate check
		// keeps a queued response.create from winning the select below and
		// starting another turn here; that frame was never dispatched.
		closing := p.proxy.SessionsClosing()
		select {
		case <-closing:
			p.closeMovedWebSocket(connection)
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-closing:
			p.closeMovedWebSocket(connection)
			return
		case <-idle.C:
			_ = connection.Close(websocket.StatusNormalClosure, "Idle connection")
			return
		case message := <-messages:
			releaseActiveBody = message.release
			idle.Stop()
			// Revocation or key rotation takes effect on the next admission, not
			// merely on the initial upgrade handshake.
			current, err := p.authenticate(r.WithContext(ctx))
			if err != nil || current.ID != key.ID {
				if err == nil {
					err = invalidKey()
				}
				_ = sendWebSocketError(ctx, connection, err)
				return
			}
			callCtx, cancelCall := context.WithCancel(ctx)
			activeMu.Lock()
			active = cancelCall
			activeRequired = message.route.RequireSecurityWorkAuthorized
			activeTerminal = false
			activeMu.Unlock()
			options := p.responseOptions(r, key.ID)
			options.Transport = application.CapabilityTransportWebSocket
			options.TurnState = turnState
			options.SynthesizedTurnState = synthesizedTurnState
			options.CapabilityRoute = &message.route
			options.LiteState = &liteState
			options.OnResponseID = func(id string) {
				activeMu.Lock()
				activeResponseID = id
				activeMu.Unlock()
			}
			options.OnInterruptReady = func(queue chan<- string) {
				activeMu.Lock()
				activeInterrupts = queue
				activeMu.Unlock()
			}
			turnActive.Store(true)
			terminal, err := p.respondWebSocket(callCtx, connection, options, message.body, func() {
				activeMu.Lock()
				activeTerminal = true
				activeMu.Unlock()
			})
			turnActive.Store(false)
			releaseActiveBody()
			releaseActiveBody = nil
			cancelCall()
			activeMu.Lock()
			active = nil
			activeRequired, activeTerminal = false, false
			activeResponseID, activeInterrupts = "", nil
			activeMu.Unlock()
			if err != nil && !terminal {
				if sendWebSocketError(ctx, connection, err) != nil {
					return
				}
			}
			idle.Reset(120 * time.Second)
		}
	}
}

// closeMovedWebSocket sends 1001 so the client reconnects: to the replacement
// server on drain, to the active process of the same installation on retirement.
func (p *ProxyHandler) closeMovedWebSocket(connection *websocket.Conn) {
	reason := "Server is restarting; reconnect"
	select {
	case <-p.proxy.DrainStarted():
		reason = "Server is shutting down"
	default:
	}
	_ = connection.Close(websocket.StatusGoingAway, reason)
}

const (
	downstreamPingInterval = 30 * time.Second
	downstreamPingTimeout  = 10 * time.Second
)

// pingWhileActive sends a protocol ping every interval while active is set so
// idle-timing intermediaries keep a long silent turn open. A ping is transport
// traffic, not an application event, and its outcome is ignored. It is never
// cancelled by turn completion (a cancelled control-frame write would close the
// client connection); it is bounded by its own timeout and ends when the
// connection handler closes the connection. The caller reads the connection
// concurrently, so pongs are processed.
func pingWhileActive(ctx context.Context, connection *websocket.Conn, interval time.Duration, active *atomic.Bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !active.Load() {
				continue
			}
			pingCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), downstreamPingTimeout)
			_ = connection.Ping(pingCtx)
			stop()
		}
	}
}

// websocketCreateBody turns a prepared response.create frame into a streaming
// Responses body. frame is the already decoded original frame; it is reused
// only when preparation returned the original bytes, otherwise the prepared
// payload is decoded into a fresh object so fields removed by preparation
// cannot reappear. Values are re-assembled verbatim.
func websocketCreateBody(frame map[string]json.RawMessage, original, prepared json.RawMessage) (json.RawMessage, error) {
	object := frame
	if len(prepared) != len(original) || len(prepared) != 0 && &prepared[0] != &original[0] {
		object = nil
		if json.Unmarshal(prepared, &object) != nil || object == nil {
			return nil, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses WebSocket frame"}
		}
	}
	delete(object, "type")
	object["stream"] = json.RawMessage("true")
	return wirejson.EncodeObject(object), nil
}

func sendWebSocketError(ctx context.Context, connection *websocket.Conn, err error) error {
	status, code, message := proxyError(err)
	detail := map[string]any{"code": code, "message": message}
	var request *application.ProxyError
	if errors.As(err, &request) && request.Param != "" {
		detail["type"], detail["param"] = "invalid_request_error", request.Param
	}
	payload, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": detail})
	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return connection.Write(writeCtx, websocket.MessageText, payload)
}
