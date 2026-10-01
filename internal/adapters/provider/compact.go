package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
)

func (a *Adapter) Compact(ctx context.Context, target application.CodexOperationTarget, body json.RawMessage) (application.CodexOperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	wire := upstream.Target{Headers: http.Header{}}
	body, err := a.chatGPTWireBody(&wire, body, false)
	if err != nil {
		return application.CodexOperationResult{}, err
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(body, &object)
	var input []json.RawMessage
	if raw := object["input"]; len(raw) != 0 && json.Unmarshal(raw, &input) != nil {
		return application.CodexOperationResult{}, providerFailure("invalid_response_request", 400, false)
	}
	var last struct {
		Type string `json:"type"`
	}
	if len(input) != 0 {
		_ = json.Unmarshal(input[len(input)-1], &last)
	}
	if last.Type != "compaction_trigger" {
		input = append(input, json.RawMessage(`{"type":"compaction_trigger"}`))
	}
	object["input"], _ = json.Marshal(input)
	body, _ = json.Marshal(object)
	wire.Headers.Set("Accept", "text/event-stream")
	result, err := a.chatGPTBody(ctx, target, http.MethodPost, a.chatGPTBaseURL()+"/responses", body, "application/json", wire.Headers, func(response *http.Response) (application.CodexOperationResult, error) {
		return readCompactResponse(response, target.OnFirstUpstreamEvent)
	})
	if err == nil && !result.UsageKnown && result.UsageReported {
		return result, providerFailure("invalid_upstream_usage", 502, true)
	}
	if err != nil || result.Failed {
		return result, err
	}
	var item json.RawMessage
	result.Body, item = application.NormalizeCodexCompactOutput(result.Body)
	if item == nil {
		return result, providerFailure("invalid_upstream_response", 502, true)
	}
	if !result.UsageKnown {
		return result, providerFailure("usage_unavailable", 502, true)
	}
	return result, nil
}

var errCompactTerminal = errors.New("compact terminal reached")

// Keep only the latest output items, not every delta in a potentially long
// compact stream. The existing SSE reader also bounds individual events.
func readCompactResponse(response *http.Response, firstEvent func()) (application.CodexOperationResult, error) {
	mediaType, _, _ := strings.Cut(strings.ToLower(response.Header.Get("Content-Type")), ";")
	mediaType = strings.TrimSpace(mediaType)
	if mediaType != "" && mediaType != "text/event-stream" {
		result, err := readOperationResponse(response)
		result.ContentType = "application/json"
		result.OutputObserved = compactHasOutput(result.Body)
		if err == nil && firstEvent != nil {
			firstEvent()
		}
		return result, err
	}
	result := application.CodexOperationResult{Status: response.StatusCode, ContentType: "application/json", Headers: map[string][]string(response.Header.Clone())}
	var output responseOutput
	events := 0
	err := upstream.ReadSSE(response.Body, func(eventName string, data []byte) error {
		events++
		if events > 100000 {
			return providerFailure("upstream_response_too_large", 502, true)
		}
		if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		var event struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			Item        json.RawMessage `json:"item"`
			OutputIndex *int            `json:"output_index"`
			Status      int             `json:"status"`
		}
		if json.Unmarshal(data, &event) != nil {
			return providerFailure("invalid_upstream_response", 502, true)
		}
		if event.Type == "" {
			event.Type = eventName
		}
		if event.Type == "" {
			return providerFailure("invalid_upstream_response", 502, true)
		}
		if firstEvent != nil {
			firstEvent()
			firstEvent = nil
		}
		switch event.Type {
		case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete", "error":
		default:
			result.OutputObserved = true
		}
		if event.Type == "response.output_item.added" || event.Type == "response.output_item.done" {
			if len(event.Item) == 0 || event.Item[0] != '{' {
				return providerFailure("invalid_upstream_response", 502, true)
			}
			if event.OutputIndex != nil || event.Type == "response.output_item.done" {
				if err := output.add(event.OutputIndex, event.Item); err != nil {
					return err
				}
			}
		}
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete", "error":
			payload := event.Response
			terminalHTTP := *response
			if event.Type == "error" {
				payload = data
				if event.Status >= 400 && event.Status <= 599 {
					terminalHTTP.StatusCode = event.Status
				}
			}
			var terminal map[string]json.RawMessage
			if json.Unmarshal(payload, &terminal) != nil || terminal == nil {
				return providerFailure("invalid_upstream_response", 502, true)
			}
			observed := result.OutputObserved || compactHasOutput(payload)
			result = operationResult(&terminalHTTP, payload)
			result.ContentType, result.OutputObserved = "application/json", observed
			var status string
			_ = json.Unmarshal(terminal["status"], &status)
			if event.Type != "response.completed" || result.Failed || status != "" && status != "completed" {
				result.Failed = true
				if result.ErrorCode == "" {
					result.ErrorCode = "upstream_compaction_failed"
				}
				return errCompactTerminal
			}
			var err error
			result.Body, err = output.fill(result.Body)
			if err != nil {
				return err
			}
			return errCompactTerminal
		}
		return nil
	})
	if errors.Is(err, errCompactTerminal) {
		return result, nil
	}
	if err == nil {
		err = providerFailure("upstream_stream_incomplete", 502, true)
	}
	return result, err
}

func compactHasOutput(body []byte) bool {
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	return json.Unmarshal(body, &response) == nil && len(response.Output) != 0
}
