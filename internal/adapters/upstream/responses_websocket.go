package upstream

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/coder/websocket"
)

func (a *HTTPAdapter) streamResponsesWebSocket(ctx context.Context, target Target, req responsesWireRequest, catalog toolCatalog, emit func(Event) error) (Result, error) {
	_ = catalog
	body, err := passthroughBody(req, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	create, err := websocketResponseCreate(body)
	if err != nil {
		return Result{}, err
	}

	session, rejected, err := a.sessions.acquire(ctx, target, req.PreviousResponseID, a.client)
	if err != nil {
		return rejected, err
	}
	keep, responseID := false, ""
	defer func() { a.sessions.release(session, keep, responseID, true) }()
	connection := session.connection

	if err := connection.Write(ctx, websocket.MessageText, create); err != nil {
		return Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: err.Error()})
	}

	for eventCount := 1; ; eventCount++ {
		if eventCount > maxSSEEvents {
			return Result{}, &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream event count exceeds bounded limit"}
		}
		var frame websocketFrame
		select {
		case received, open := <-session.frames:
			if !open {
				return Result{}, &Error{Code: ErrorCodeStreamIncomplete, Message: "Upstream WebSocket closed before a terminal event"}
			}
			frame = received
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		data, err := frame.data, frame.err
		if err != nil {
			if websocket.CloseStatus(err) != -1 {
				err = &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream WebSocket closed before a terminal event"}
			}
			return Result{}, wrapContext(ctx, err)
		}
		if frame.kind != websocket.MessageText {
			return Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "Expected a text WebSocket event"}
		}
		if len(data) > maxSSEEventBytes {
			return Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream WebSocket event exceeds bounded size"}
		}
		var object map[string]json.RawMessage
		if err := decodeExactJSON(data, &object); err != nil {
			return Result{}, err
		}
		eventType := ""
		_ = json.Unmarshal(object["type"], &eventType)
		if eventType == "" {
			return Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream WebSocket event omitted its type"}
		}
		if eventType == "error" || object["error"] != nil {
			var envelope struct {
				Status int `json:"status"`
			}
			_ = json.Unmarshal(data, &envelope)
			result, _ := rejectedResult(envelope.Status, data, target.Capabilities.Protocol)
			return result, responseEventError(data)
		}
		switch eventType {
		case "response.completed", "response.failed", "response.incomplete":
			result, err := terminalFromResponsesEvent(eventType, data, target.Capabilities.AllowMissingUsage)
			if err != nil {
				return result, err
			}
			if err := emit(Event{Type: eventType, Data: append(json.RawMessage(nil), data...)}); err != nil {
				return result, err
			}
			keep, responseID = true, result.ResponseID
			return result, nil
		}
		if err := emit(Event{Type: eventType, Data: append(json.RawMessage(nil), data...)}); err != nil {
			return Result{}, err
		}
	}
}

func websocketResponseCreate(body json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "Responses request was not a JSON object"}
	}
	var background bool
	if raw, ok := object["background"]; ok && (json.Unmarshal(raw, &background) != nil || background) {
		return nil, &Error{Code: ErrorCodeUnsupportedCapability, Message: "Background Responses are not supported over WebSocket"}
	}
	delete(object, "background")
	delete(object, "stream")
	object["type"] = mustJSON("response.create")
	return mustJSON(object), nil
}

func websocketURL(httpEndpoint string) string {
	if strings.HasPrefix(httpEndpoint, "https://") {
		return "wss://" + strings.TrimPrefix(httpEndpoint, "https://")
	}
	if strings.HasPrefix(httpEndpoint, "http://") {
		return "ws://" + strings.TrimPrefix(httpEndpoint, "http://")
	}
	return httpEndpoint
}
