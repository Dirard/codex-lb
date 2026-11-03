package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
)

// realtimeSubprotocol is the WebSocket protocol Realtime browser clients
// offer; the credential rides in a second, prefixed protocol value.
const (
	realtimeSubprotocol        = "realtime"
	realtimeCredentialProtocol = "openai-insecure-api-key."
)

// realtimeCredential authenticates an app's Realtime request with an API key
// or a client secret, given as a bearer value or, for a browser WebSocket,
// as the openai-insecure-api-key subprotocol offered with realtime.
func (h *CodexOperationsHandler) realtimeCredential(r *http.Request, websocketUpgrade bool) (application.RealtimeCredential, string, error) {
	var value, subprotocol string
	if values := r.Header.Values("Authorization"); len(values) != 0 {
		parts := strings.Fields(values[0])
		if len(values) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return application.RealtimeCredential{}, "", invalidKey()
		}
		value = parts[1]
	} else if websocketUpgrade {
		offered := false
		for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
			for _, protocol := range strings.Split(header, ",") {
				protocol = strings.TrimSpace(protocol)
				offered = offered || protocol == realtimeSubprotocol
				if credential, ok := strings.CutPrefix(protocol, realtimeCredentialProtocol); ok {
					value = credential
				}
			}
		}
		if value != "" {
			if !offered {
				return application.RealtimeCredential{}, "", invalidKey()
			}
			subprotocol = realtimeSubprotocol
		}
	}
	if strings.HasPrefix(value, application.RealtimeClientSecretPrefix) {
		credential, err := h.ops.ResolveRealtimeClientSecret(r.Context(), value)
		return credential, subprotocol, err
	}
	key, err := authenticateKeyValue(r, h.store, value)
	if err != nil {
		return application.RealtimeCredential{}, "", err
	}
	return application.RealtimeCredential{KeyID: key.ID}, subprotocol, nil
}

// realtimeSession serves an app's Realtime WebSocket session: bounded checks
// before the upgrade, then the metered session relay.
func (h *CodexOperationsHandler) realtimeSession(w http.ResponseWriter, r *http.Request) {
	credential, subprotocol, err := h.realtimeCredential(r, true)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	options := h.operationOptions(r, credential.KeyID)
	options.Transport = "websocket"
	query := make([][2]string, 0)
	for name, values := range r.URL.Query() {
		if name == "model" {
			continue
		}
		for _, value := range values {
			query = append(query, [2]string{name, value})
		}
	}
	models := r.URL.Query()["model"]
	if len(models) != 1 {
		writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Provide exactly one realtime model", Param: "model"})
		return
	}
	request := application.CodexRealtimeSessionRequest{Model: models[0], Query: query, Headers: realtimeHeaders(r)}
	plan, err := h.ops.PrepareRealtimeSession(r.Context(), options, credential, request)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	// A page on any origin may open an app session: its credential is always
	// explicit, never a cookie, so a foreign page gains nothing without one.
	accept := &websocket.AcceptOptions{OriginPatterns: []string{"*"}}
	if subprotocol != "" {
		accept.Subprotocols = []string{subprotocol}
	}
	connection, err := websocket.Accept(w, r, accept)
	if err != nil {
		return
	}
	connection.SetReadLimit(application.MaxRealtimeMessageBytes)
	defer connection.CloseNow()
	var active atomic.Bool
	active.Store(true)
	pingCtx, stopPings := context.WithCancel(r.Context())
	pingerDone := make(chan struct{})
	go func() {
		defer close(pingerDone)
		pingWhileActive(pingCtx, connection, downstreamPingInterval, &active)
	}()
	defer func() { stopPings(); connection.CloseNow(); <-pingerDone }()
	if err := h.ops.RealtimeSession(r.Context(), options, plan, websocketConnection{connection}); err != nil {
		// Only a fixed code reaches the client, never an upstream answer.
		_ = connection.Close(websocket.StatusInternalError, realtimeCloseCode(err))
	}
}

func realtimeCloseCode(err error) string {
	var failure *application.ProviderFailure
	var request *application.ProxyError
	switch {
	case errors.As(err, &failure) && failure.Code != "":
		return failure.Code
	case errors.As(err, &request) && request.Code != "":
		return request.Code
	default:
		return "realtime_session_failed"
	}
}

// allowRealtimeCallOrigin lets a browser page on any origin read a call's
// answer, refusals included; the credential travels in Authorization.
func allowRealtimeCallOrigin(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")
}

// realtimeCallPreflight answers the CORS preflight of a browser's call offer.
func (h *CodexOperationsHandler) realtimeCallPreflight(w http.ResponseWriter, _ *http.Request) {
	allowRealtimeCallOrigin(w)
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// createAppRealtimeCall creates a WebRTC call for an app from a raw SDP,
// multipart or JSON offer.
func (h *CodexOperationsHandler) createAppRealtimeCall(w http.ResponseWriter, r *http.Request) {
	allowRealtimeCallOrigin(w)
	credential, _, err := h.realtimeCredential(r, false)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		writeProxyError(w, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"})
		return
	}
	lease := h.bodies.lease(credential.KeyID)
	defer lease.release()
	body, err := readOperationBody(w, r, lease, application.MaxRealtimeCallBytes)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.CreateAppRealtimeCall(r.Context(), h.operationOptions(r, credential.KeyID), credential,
		r.Header.Get("Content-Type"), body, r.URL.Query().Get("model"), realtimeHeaders(r))
	h.writeResult(w, result, err, false)
}

// createAppLiveCall creates a GPT-Live WebRTC call for an app from a raw SDP,
// multipart or JSON offer; codex-lb meters it through its own sideband.
func (h *CodexOperationsHandler) createAppLiveCall(w http.ResponseWriter, r *http.Request) {
	allowRealtimeCallOrigin(w)
	credential, _, err := h.realtimeCredential(r, false)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		writeProxyError(w, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"})
		return
	}
	lease := h.bodies.lease(credential.KeyID)
	defer lease.release()
	body, err := readOperationBody(w, r, lease, application.MaxRealtimeCallBytes)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.CreateAppLiveCall(r.Context(), h.operationOptions(r, credential.KeyID), credential,
		r.Header.Get("Content-Type"), body, r.URL.Query().Get("model"))
	h.writeResult(w, result, err, false)
}

// createRealtimeClientSecret issues a client secret to a server that holds a
// real API key; a client secret cannot mint another.
func (h *CodexOperationsHandler) createRealtimeClientSecret(w http.ResponseWriter, r *http.Request) {
	key, err := authenticateBearerKey(r, h.store)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	lease := h.bodies.lease(key.ID)
	defer lease.release()
	body, err := readOperationBody(w, r, lease, application.MaxRealtimeSecretBytes)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.IssueRealtimeClientSecret(r.Context(), key.ID, body)
	h.writeResult(w, result, err, false)
}
