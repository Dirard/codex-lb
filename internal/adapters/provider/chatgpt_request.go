package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
)

var codexCompatibilityMetadataHeaders = [...]string{
	"x-codex-turn-metadata", "x-openai-subagent", "x-codex-parent-thread-id", "x-codex-window-id",
}

func (a *Adapter) callChatGPT(ctx context.Context, target *upstream.Target, body json.RawMessage, clientStream, trustedLite bool, emit func(application.ResponseEvent) error, firstUpstreamEvent func()) (application.ResponseResult, error) {
	started := time.Now()
	send := func() (application.ResponseResult, error) {
		normalized, err := a.chatGPTWireBody(target, body, trustedLite)
		if err != nil {
			return application.ResponseResult{}, err
		}
		consume := emit
		var output responseOutput
		if !clientStream {
			consume = func(event application.ResponseEvent) error {
				if event.Type != "response.output_item.done" {
					return nil
				}
				var item struct {
					Index *int            `json:"output_index"`
					Item  json.RawMessage `json:"item"`
				}
				if json.Unmarshal(event.Data, &item) != nil {
					return providerFailure("invalid_upstream_response", 502, true)
				}
				return output.add(item.Index, item.Item)
			}
		}
		result, err := a.call(ctx, *target, normalized, true, consume, firstUpstreamEvent)
		if err == nil && !result.Failed && !clientStream {
			result.Response, err = output.fill(result.Response)
		}
		return result, err
	}
	result, callErr := send()
	var failure *application.ProviderFailure
	uncharged := !result.UsageKnown && !result.UsageReported || result.UsageKnown && result.Usage.InputTokens == 0 && result.Usage.OutputTokens == 0
	if target.AllowHTTPFallback && target.Capabilities.StreamTransport == upstream.TransportWebSocket && !target.RequiredCapability &&
		!result.OutputObserved && uncharged && errors.As(callErr, &failure) && failure.WebSocketHTTPFallback && !failure.QuotaRefused && failure.Status != 401 {
		// Keep the effective HTTP mode for any later same-owner auth refresh.
		target.AllowHTTPFallback = false
		target.Capabilities.StreamTransport = upstream.TransportHTTP
		beforeHTTP := time.Since(started).Milliseconds()
		httpResult, httpErr := send()
		httpResult.ConnectLatencyMS += result.ConnectLatencyMS
		if httpResult.FirstEventMS > 0 {
			httpResult.FirstEventMS += beforeHTTP
		}
		return httpResult, httpErr
	}
	return result, callErr
}

func (a *Adapter) chatGPTWireBody(target *upstream.Target, body json.RawMessage, trustedLite bool) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, providerFailure("invalid_response_request", 400, false)
	}
	// Subscription Codex is a stateless, streaming endpoint. Public non-streaming
	// clients receive the completed response collected from that same stream.
	object["store"] = json.RawMessage("false")
	object["stream"] = json.RawMessage("true")
	// The private subscription API rejects the public Responses output cap.
	delete(object, "max_output_tokens")
	a.normalizeChatGPTReasoning(object)
	if _, ok := object["instructions"]; !ok {
		object["instructions"] = json.RawMessage(`""`)
	}
	var text string
	if json.Unmarshal(object["input"], &text) == nil {
		object["input"], _ = json.Marshal([]struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{"user", text}})
	}
	websocket := target.Capabilities.StreamTransport == upstream.TransportWebSocket
	lite, err := application.NormalizeCodexResponsesLite(object, websocket, trustedLite)
	if err != nil {
		return nil, err
	}
	target.Headers.Del(application.ResponsesLiteHeader)
	if lite && !websocket {
		target.Headers.Set(application.ResponsesLiteHeader, "true")
	}
	if websocket {
		metadata := make(map[string]json.RawMessage)
		if raw := object["client_metadata"]; len(raw) > 0 {
			// NormalizeCodexResponsesLite has already validated the metadata object.
			if err := json.Unmarshal(raw, &metadata); err != nil {
				return nil, providerFailure("invalid_response_request", 400, false)
			}
		}
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
		for _, name := range codexCompatibilityMetadataHeaders {
			if _, present := metadata[name]; !present && strings.TrimSpace(target.Headers.Get(name)) != "" {
				metadata[name], _ = json.Marshal(target.Headers.Get(name))
			}
		}
		if len(metadata) != 0 {
			object["client_metadata"], _ = json.Marshal(metadata)
		}
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, providerFailure("invalid_response_request", 400, false)
	}
	return normalized, nil
}

// normalizeChatGPTReasoning applies the subscription-only minimal workaround
// after account selection. The caller's policy/reporting body stays unchanged.
func (a *Adapter) normalizeChatGPTReasoning(object map[string]json.RawMessage) {
	upstream.NormalizeWireReasoning(object)
	var reasoning map[string]json.RawMessage
	var effort string
	if json.Unmarshal(object["reasoning"], &reasoning) != nil || json.Unmarshal(reasoning["effort"], &effort) != nil || !strings.EqualFold(strings.TrimSpace(effort), "minimal") {
		return
	}
	fallback := "low"
	if a.config.ChatGPTReasoningFallback != nil {
		var model string
		_ = json.Unmarshal(object["model"], &model)
		fallback = a.config.ChatGPTReasoningFallback(model)
	}
	reasoning["effort"], _ = json.Marshal(upstream.WireReasoningEffort(fallback))
	object["reasoning"], _ = json.Marshal(reasoning)
}
