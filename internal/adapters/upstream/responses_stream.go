package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func (a *HTTPAdapter) streamResponses(ctx context.Context, target Target, req responsesWireRequest, catalog toolCatalog, emit func(Event) error) (Result, error) {
	_ = catalog
	if target.Capabilities.StreamTransport == TransportWebSocket {
		return a.streamResponsesWebSocket(ctx, target, req, catalog, emit)
	}
	body, err := passthroughBody(req, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	response, rejected, err := a.beginStream(ctx, target, endpoint(target.BaseURL, "responses"), body)
	if err != nil {
		return rejected, err
	}
	defer response.Body.Close()

	var terminal Result
	terminalSeen := false
	eventCount := 0
	err = readSSE(response.Body, func(eventName string, data []byte) error {
		eventCount++
		if eventCount > maxSSEEvents {
			return &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream event count exceeds bounded limit"}
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		var object map[string]json.RawMessage
		if err := decodeExactJSON(data, &object); err != nil {
			return err
		}
		eventType := strings.TrimSpace(eventName)
		if raw, ok := object["type"]; ok {
			var typeName string
			if json.Unmarshal(raw, &typeName) == nil {
				eventType = typeName
			}
		}
		if eventType == "" {
			return &Error{Code: ErrorCodeInvalidStream, Message: "upstream SSE event omitted its type"}
		}
		if eventType == "error" {
			var envelope struct {
				Status int `json:"status"`
			}
			_ = json.Unmarshal(data, &envelope)
			terminal, _ = rejectedResult(envelope.Status, data, target.Capabilities.Protocol)
			return responseEventError(data)
		}
		switch eventType {
		case "response.completed", "response.failed", "response.incomplete":
			if terminalSeen {
				return &Error{Code: ErrorCodeInvalidStream, Message: "upstream sent more than one terminal Responses event"}
			}
			terminalSeen = true
			terminal, err = terminalFromResponsesEvent(eventType, data, target.Capabilities.AllowMissingUsage)
			if err != nil {
				return err
			}
			if err := emit(Event{Type: eventType, Data: append(json.RawMessage(nil), data...)}); err != nil {
				return err
			}
			return errResponsesTerminalReached
		}
		return emit(Event{Type: eventType, Data: append(json.RawMessage(nil), data...)})
	})
	if err != nil && !errors.Is(err, errResponsesTerminalReached) {
		return terminal, wrapContext(ctx, err)
	}
	if !terminalSeen {
		return Result{}, &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream stream disconnected before a terminal event"}
	}
	return terminal, nil
}

var errResponsesTerminalReached = errors.New("responses terminal event reached")

func terminalFromResponsesEvent(eventType string, data []byte, allowMissingUsage bool) (Result, error) {
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := decodeExactJSON(data, &envelope); err != nil {
		return Result{}, err
	}
	var payload struct {
		Response *struct {
			ID          string          `json:"id"`
			Status      string          `json:"status"`
			Error       *wireError      `json:"error"`
			WireUsage   json.RawMessage `json:"usage"`
			ServiceTier string          `json:"service_tier"`
		} `json:"response"`
	}
	if err := decodeExactJSON(data, &payload); err != nil {
		return Result{}, err
	}
	if payload.Response == nil || payload.Response.ID == "" {
		return Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "terminal Responses event omitted response id"}
	}
	result := Result{
		ResponseID:  payload.Response.ID,
		Response:    envelope.Response,
		ServiceTier: payload.Response.ServiceTier,
	}
	result.readUsage(payload.Response.WireUsage, ProtocolResponses)
	if eventType != "response.completed" || payload.Response.Error != nil || payload.Response.Status != "" && payload.Response.Status != "completed" {
		result.Failed = true
		if payload.Response.Error != nil {
			result.ErrorCode = wireErrorCode(payload.Response.Error)
			result.ErrorMessage = payload.Response.Error.Message
		}
	}
	if result.UsageReported && !result.UsageKnown {
		return result, &Error{Code: ErrorCodeInvalidStream, Message: "terminal Responses event contains partial usage"}
	}
	if eventType == "response.completed" && (payload.Response.Error != nil || payload.Response.Status != "" && payload.Response.Status != "completed") {
		return result, &Error{Code: ErrorCodeInvalidStream, Message: "completed Responses event contains an inconsistent terminal status"}
	}
	if !result.UsageKnown && eventType == "response.completed" && !allowMissingUsage {
		return result, &Error{Code: ErrorCodeMissingUsage, Message: "completed Responses event omitted valid terminal usage"}
	}
	return result, nil
}
