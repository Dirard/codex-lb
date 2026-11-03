package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const defaultLiveBaseURL = "https://api.openai.com/v1"

// realtimeBounds limit how long one realtime write may block and how an
// upstream realtime socket proves transport liveness.
type realtimeBounds struct {
	write        time.Duration
	pingInterval time.Duration
	pingTimeout  time.Duration
}

var defaultRealtimeBounds = realtimeBounds{write: 30 * time.Second, pingInterval: 30 * time.Second, pingTimeout: 10 * time.Second}

func (a *Adapter) Realtime(ctx context.Context, target application.CodexOperationTarget, request application.CodexRealtimeRequest, downstream application.CodexRealtimeConnection) error {
	if err := application.ValidateCodexRealtimeRequest(request); err != nil {
		return err
	}
	endpoint := liveBaseURL(a.config.ChatGPTBaseURL) + "/live/" + url.PathEscape(request.CallID)
	queryValues := request.Query
	if request.Protocol == application.CodexRealtimeLegacy {
		endpoint = liveBaseURL(a.config.ChatGPTBaseURL) + "/realtime"
		queryValues = append(append([][2]string(nil), request.Query...), [2]string{"call_id", request.CallID})
	}
	if query := encodeQuery(queryValues); query != "" {
		endpoint += "?" + query
	}
	protocol := request.Headers
	if request.Protocol != application.CodexRealtimeLegacy && strings.TrimSpace(protocol.Alpha) == "" {
		// The v3 path speaks the frameless GPT-Live protocol; an app that
		// does not name it still attaches with the alpha Codex sends.
		protocol.Alpha = domain.LiveAlpha
	}
	upstream, _, err := a.dialRealtime(ctx, target, protocol, endpoint)
	if err != nil {
		var failure *application.ProviderFailure
		if errors.As(err, &failure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return providerFailure("realtime_live_unavailable", 502, true)
	}
	return a.relayRealtime(ctx, upstream, downstream, nil, nil)
}

// RealtimeSession dials the account's own Realtime session for an app, the
// standard /realtime?model= socket, sends the app's first message ahead of
// the client and lets observe see every upstream text frame.
func (a *Adapter) RealtimeSession(ctx context.Context, target application.CodexOperationTarget, request application.CodexRealtimeSessionRequest,
	downstream application.CodexRealtimeConnection, observe func([]byte)) error {
	query := append([][2]string{{"model", request.Model}}, request.Query...)
	endpoint := liveBaseURL(a.config.ChatGPTBaseURL) + "/realtime?" + encodeQuery(query)
	upstream, refusal, err := a.dialRealtime(ctx, target, request.Headers, endpoint)
	if err != nil {
		var failure *application.ProviderFailure
		if errors.As(err, &failure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		status := http.StatusBadGateway
		if refusal == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		// Nothing ran before the upgrade, so the session costs nothing.
		return providerFailure("realtime_session_unavailable", status, false)
	}
	return a.relayRealtime(ctx, upstream, downstream, request.FirstMessage, observe)
}

// dialRealtime opens an upstream Realtime socket with the account's token,
// ChatGPT account and Codex identity. A failure before the dial is a
// ProviderFailure; a refused upgrade also gives its HTTP status.
func (a *Adapter) dialRealtime(ctx context.Context, target application.CodexOperationTarget, protocol application.CodexRealtimeHeaders, endpoint string) (*websocket.Conn, int, error) {
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return nil, 0, providerFailure("chatgpt_credential_unavailable", 0, false)
	}
	token, err := a.chatgpt.AccessToken(ctx, target.Account, credential)
	if err != nil {
		return nil, 0, providerFailure("chatgpt_token_unavailable", 0, false)
	}
	headers, err := a.chatGPTHeaders(ctx)
	if err != nil {
		return nil, 0, err
	}
	headers.Set("Authorization", "Bearer "+token)
	if target.Account.ChatGPTAccountID != "" {
		headers.Set("ChatGPT-Account-ID", target.Account.ChatGPTAccountID)
	}
	for name, values := range realtimeProtocolHeaders(protocol) {
		headers[name] = values
	}
	upstream, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: a.config.HTTPClient, HTTPHeader: headers})
	status := 0
	if response != nil {
		status = response.StatusCode
		if response.Body != nil {
			_ = response.Body.Close() // A refusal body is never read or shown.
		}
	}
	return upstream, status, err
}

// relayRealtime pumps frames both ways until either side ends. first, when
// set, reaches the upstream before any client frame; observe sees each
// upstream text frame before it is delivered.
func (a *Adapter) relayRealtime(ctx context.Context, upstream *websocket.Conn, downstream application.CodexRealtimeConnection, first []byte, observe func([]byte)) error {
	defer upstream.Close(websocket.StatusNormalClosure, "")
	upstream.SetReadLimit(application.MaxRealtimeMessageBytes)
	requestContext := ctx
	ctx, cancel := context.WithCancel(ctx)
	bounds := a.realtime
	if len(first) != 0 {
		writeCtx, stop := context.WithTimeout(ctx, bounds.write)
		err := upstream.Write(writeCtx, websocket.MessageText, first)
		stop()
		if err != nil {
			cancel()
			_ = downstream.Close(realtimeCloseCode(err), realtimeCloseReason(err))
			if requestContext.Err() != nil {
				return requestContext.Err()
			}
			return providerFailure("realtime_live_disconnected", 502, true)
		}
	}
	var lastUpstream atomic.Int64
	var delivering atomic.Bool
	pingFailed := make(chan error, 1)
	pingerDone := make(chan struct{})
	go func() {
		defer close(pingerDone)
		probeRealtimeUpstream(ctx, upstream, bounds, &lastUpstream, &delivering, pingFailed)
	}()
	defer func() { cancel(); <-pingerDone }()
	upstreamDone := make(chan error, 1)
	go func() {
		for {
			messageType, reader, err := upstream.Reader(ctx)
			var data []byte
			if err == nil {
				data, err = io.ReadAll(io.LimitReader(reader, application.MaxRealtimeMessageBytes+1))
				if err == nil && len(data) > application.MaxRealtimeMessageBytes {
					err = &application.ProxyError{Code: "realtime_message_too_large", Status: 1009, Message: "Realtime message exceeds limit"}
				}
			}
			if err != nil {
				upstreamDone <- err
				return
			}
			lastUpstream.Store(time.Now().UnixNano())
			if observe != nil && messageType == websocket.MessageText {
				observe(data)
			}
			delivering.Store(true)
			writeCtx, stop := context.WithTimeout(ctx, bounds.write)
			err = downstream.Write(writeCtx, application.CodexRealtimeMessage{Binary: messageType == websocket.MessageBinary, Data: data})
			stop()
			delivering.Store(false)
			if err != nil {
				upstreamDone <- err
				return
			}
		}
	}()
	downstreamErr := make(chan error, 1)
	go func() {
		for {
			message, err := downstream.Read(ctx)
			if err != nil {
				downstreamErr <- err
				return
			}
			if len(message.Data) > application.MaxRealtimeMessageBytes {
				downstreamErr <- &application.ProxyError{Code: "realtime_message_too_large", Status: 1009, Message: "Realtime message exceeds limit"}
				return
			}
			kind := websocket.MessageText
			if message.Binary {
				kind = websocket.MessageBinary
			}
			writeCtx, stop := context.WithTimeout(ctx, bounds.write)
			err = upstream.Write(writeCtx, kind, message.Data)
			stop()
			if err != nil {
				downstreamErr <- err
				return
			}
		}
	}()

	select {
	case err := <-upstreamDone:
		cancel()
		<-downstreamErr
		_ = downstream.Close(realtimeCloseCode(err), realtimeCloseReason(err))
		if requestContext.Err() != nil {
			return requestContext.Err()
		}
		if realtimeNormalClose(err) {
			return nil
		}
		return providerFailure("realtime_live_disconnected", 502, true)
	case err := <-downstreamErr:
		cancel()
		<-upstreamDone
		_ = downstream.Close(realtimeCloseCode(err), realtimeCloseReason(err))
		if requestContext.Err() != nil {
			return requestContext.Err()
		}
		if realtimeNormalClose(err) || errors.Is(err, context.Canceled) {
			return nil
		}
		return providerFailure("realtime_live_disconnected", 502, true)
	case err := <-pingFailed:
		cancel()
		<-upstreamDone
		<-downstreamErr
		_ = downstream.Close(realtimeCloseCode(err), realtimeCloseReason(err))
		if requestContext.Err() != nil {
			return requestContext.Err()
		}
		return providerFailure("realtime_live_disconnected", 502, true)
	case <-ctx.Done():
		cancel()
		<-upstreamDone
		<-downstreamErr
		_ = downstream.Close("1000", "")
		return ctx.Err()
	}
}

// probeRealtimeUpstream pings an upstream realtime socket that sent nothing for
// a ping interval. A frame received after the ping, or a frame still being
// written to a slow client (bounded by the write timeout), proves the peer is
// alive; only a silent, unanswered ping ends the session.
func probeRealtimeUpstream(ctx context.Context, upstream *websocket.Conn, bounds realtimeBounds, lastUpstream *atomic.Int64, delivering *atomic.Bool, failed chan<- error) {
	ticker := time.NewTicker(bounds.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, lastUpstream.Load())) < bounds.pingInterval {
				continue
			}
			sent := time.Now().UnixNano()
			pingCtx, stop := context.WithTimeout(ctx, bounds.pingTimeout)
			err := upstream.Ping(pingCtx)
			stop()
			if err == nil || delivering.Load() || lastUpstream.Load() >= sent {
				continue
			}
			if ctx.Err() == nil {
				failed <- err
			}
			return
		}
	}
}

func realtimeProtocolHeaders(protocol application.CodexRealtimeHeaders) http.Header {
	headers := http.Header{}
	if value := strings.TrimSpace(protocol.Alpha); value != "" {
		headers.Set("OpenAI-Alpha", value)
	}
	var beta []string
	for _, value := range strings.Split(protocol.Beta, ",") {
		value = strings.TrimSpace(value)
		name, setting, _ := strings.Cut(strings.ToLower(value), "=")
		name, setting = strings.TrimSpace(name), strings.TrimSpace(setting)
		if value != "" && !(name == "responses" && setting == "experimental") && name != "responses_websockets" {
			beta = append(beta, value)
		}
	}
	if len(beta) > 0 {
		headers.Set("OpenAI-Beta", strings.Join(beta, ", "))
	}
	return headers
}

func liveBaseURL(chatGPTBase string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(chatGPTBase, "/"), "/codex")
	if base == "https://chatgpt.com/backend-api" || base == "https://chatgpt.com" {
		return defaultLiveBaseURL
	}
	return strings.TrimSuffix(base, "/") + "/v1"
}

func realtimeNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return err == nil || errors.Is(err, context.Canceled) || status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}

func realtimeCloseCode(err error) string {
	var tooLarge *application.ProxyError
	if errors.As(err, &tooLarge) && tooLarge.Code == "realtime_message_too_large" || websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
		return "1009"
	}
	if realtimeNormalClose(err) {
		return "1000"
	}
	return "1011"
}

func realtimeCloseReason(err error) string {
	if realtimeNormalClose(err) {
		return ""
	}
	return "realtime upstream failed"
}

var _ application.CodexOperationProvider = (*Adapter)(nil)
