package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
)

const defaultLiveBaseURL = "https://api.openai.com/v1"

func (a *Adapter) Realtime(ctx context.Context, target application.CodexOperationTarget, request application.CodexRealtimeRequest, downstream application.CodexRealtimeConnection) error {
	if err := application.ValidateCodexRealtimeRequest(request); err != nil {
		return err
	}
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return providerFailure("chatgpt_credential_unavailable", 0, false)
	}
	token, err := a.chatgpt.AccessToken(ctx, target.Account, credential)
	if err != nil {
		return providerFailure("chatgpt_token_unavailable", 0, false)
	}
	headers, err := a.chatGPTHeaders(ctx)
	if err != nil {
		return err
	}
	headers.Set("Authorization", "Bearer "+token)
	if target.Account.ChatGPTAccountID != "" {
		headers.Set("ChatGPT-Account-ID", target.Account.ChatGPTAccountID)
	}
	for name, values := range realtimeProtocolHeaders(request.Headers) {
		headers[name] = values
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
	upstream, _, err := websocket.Dial(ctx, websocketEndpointURL(endpoint), &websocket.DialOptions{HTTPClient: a.config.HTTPClient, HTTPHeader: headers})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return providerFailure("realtime_live_unavailable", 502, true)
	}
	defer upstream.Close(websocket.StatusNormalClosure, "")
	upstream.SetReadLimit(application.MaxRealtimeMessageBytes)
	requestContext := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
			if err := downstream.Write(ctx, application.CodexRealtimeMessage{Binary: messageType == websocket.MessageBinary, Data: data}); err != nil {
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
			err = upstream.Write(ctx, kind, message.Data)
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
	case <-ctx.Done():
		cancel()
		<-upstreamDone
		<-downstreamErr
		_ = downstream.Close("1000", "")
		return ctx.Err()
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

func websocketEndpointURL(endpoint string) string {
	if strings.HasPrefix(endpoint, "https://") {
		return "wss://" + strings.TrimPrefix(endpoint, "https://")
	}
	if strings.HasPrefix(endpoint, "http://") {
		return "ws://" + strings.TrimPrefix(endpoint, "http://")
	}
	return endpoint
}

var _ application.CodexOperationProvider = (*Adapter)(nil)
