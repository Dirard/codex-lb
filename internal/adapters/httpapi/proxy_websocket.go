package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

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
	case p.reading <- struct{}{}:
		defer func() { <-p.reading }()
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
		body  json.RawMessage
		route application.CapabilityRoute
	}
	messages := make(chan createFrame, 1)
	readerDone := make(chan struct{})
	var activeMu sync.Mutex
	var active context.CancelFunc
	var activeRequired bool
	go func() {
		defer close(readerDone)
		defer cancel()
		for {
			kind, body, err := connection.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText {
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Binary Responses WebSocket frames are not supported"})
				return
			}
			current, err := p.authenticate(r.WithContext(ctx))
			if err != nil || current.ID != key.ID {
				if err == nil {
					err = invalidKey()
				}
				_ = sendWebSocketError(ctx, connection, err)
				return
			}
			var object map[string]json.RawMessage
			var eventType string
			if json.Unmarshal(body, &object) != nil || object == nil || json.Unmarshal(object["type"], &eventType) != nil {
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses WebSocket frame"})
				continue
			}
			_, parseErr := application.ParseCapabilitySignal(application.CapabilitySignalRequest{Transport: application.CapabilityTransportWebSocket, FrameType: eventType, APIKeyID: key.ID, Body: body})
			if parseErr != nil {
				_ = sendWebSocketError(ctx, connection, parseErr)
				continue
			}
			if eventType != "response.create" {
				if eventType != "response.cancel" {
					_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "unsupported_websocket_event", Status: 400, Message: "Only response.create and response.cancel are supported"})
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
				_ = sendWebSocketError(ctx, connection, err)
				continue
			}
			activeMu.Lock()
			conflict := active != nil && route.RequireSecurityWorkAuthorized && !activeRequired
			activeMu.Unlock()
			if conflict {
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "capability_routing_unavailable", Status: 503, Message: "Required capability cannot be selected while ordinary work is pending"})
				continue
			}
			if json.Unmarshal(signal.Payload, &object) != nil {
				_ = sendWebSocketError(ctx, connection, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses WebSocket frame"})
				continue
			}
			delete(object, "type")
			object["stream"] = json.RawMessage("true")
			body, err = json.Marshal(object)
			if err != nil {
				return
			}
			select {
			case messages <- createFrame{body: body, route: route}:
			case <-ctx.Done():
				return
			default:
				_ = connection.Close(websocket.StatusPolicyViolation, "Too many queued response.create frames")
				return
			}
		}
	}()
	defer func() { cancel(); connection.CloseNow(); <-readerDone }()
	idle := time.NewTimer(120 * time.Second)
	defer idle.Stop()
	var liteState application.ResponsesLiteState
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.proxy.DrainStarted():
			_ = connection.Close(websocket.StatusGoingAway, "Server is shutting down")
			return
		case <-idle.C:
			_ = connection.Close(websocket.StatusNormalClosure, "Idle connection")
			return
		case message := <-messages:
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
			activeMu.Unlock()
			terminal := false
			options := p.responseOptions(r, key.ID)
			options.Transport = application.CapabilityTransportWebSocket
			options.TurnState = turnState
			options.SynthesizedTurnState = synthesizedTurnState
			options.CapabilityRoute = &message.route
			options.LiteState = &liteState
			_, err = p.proxy.Respond(callCtx, options, message.body, func(event application.ResponseEvent) error {
				writeCtx, cancelWrite := context.WithTimeout(callCtx, 30*time.Second)
				defer cancelWrite()
				err := connection.Write(writeCtx, websocket.MessageText, event.Data)
				if err == nil {
					terminal = terminal || terminalEvent(event.Type)
				}
				return err
			})
			cancelCall()
			activeMu.Lock()
			active = nil
			activeRequired = false
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
