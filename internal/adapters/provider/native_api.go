package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

var _ application.NativeEmbeddingProvider = (*Adapter)(nil)
var _ application.NativeChatProvider = (*Adapter)(nil)

func (a *Adapter) NativeChat(ctx context.Context, target application.ResponseTarget, body json.RawMessage, stream bool, emit func(application.NativeAPIEvent) error) (application.NativeAPIResult, error) {
	source, key, err := a.externalTarget(ctx, target.Account)
	if err != nil {
		return application.NativeAPIResult{}, err
	}
	sourceModel, ok := source.Model(modelFromBody(body))
	if !ok || !source.Enabled || !source.Chat {
		return application.NativeAPIResult{}, providerFailure("model_source_model_unavailable", 404, false)
	}
	if err := validateNativeChatCapabilities(body, sourceModel, stream); err != nil {
		return application.NativeAPIResult{}, err
	}
	ctx, release, err := a.beginSource(ctx, source)
	if err != nil {
		return application.NativeAPIResult{}, err
	}
	defer release()
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return application.NativeAPIResult{}, providerFailure("invalid_request", 400, false)
	}
	changed := upstream.NormalizeWireReasoning(object)
	if sourceModel.UpstreamModel != "" && sourceModel.UpstreamModel != sourceModel.Model {
		object["model"] = providerMustJSON(sourceModel.UpstreamModel)
		changed = true
	}
	if changed {
		body, _ = json.Marshal(object)
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	endpoint := strings.TrimSuffix(source.BaseURL, "/") + "/chat/completions"
	if !stream {
		result, err := a.operation(ctx, http.MethodPost, endpoint, body, "application/json", headers, nil)
		if err != nil {
			return application.NativeAPIResult{}, err
		}
		parsed := nativeResult(result)
		if !parsed.UsageKnown && reportedOperationBilling(result.Body) {
			return parsed, providerFailure("invalid_upstream_usage", 502, true)
		}
		if parsed.Failed || parsed.Status < 200 || parsed.Status >= 300 {
			return parsed, nil
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(result.Body, &object) != nil || object == nil {
			return parsed, providerFailure("invalid_upstream_response", 502, true)
		}
		parsed.UsageKnown = nativeChatUsageKnown(object)
		if !parsed.UsageKnown && !source.ProviderConfig.AllowMissingUsage {
			return parsed, providerFailure("usage_unavailable", 502, true)
		}
		return parsed, nil
	}
	return a.nativeChatStream(ctx, source, endpoint, body, headers, emit)
}

type nativeChatMessage struct {
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Content      json.RawMessage   `json:"content"`
	ToolCalls    []json.RawMessage `json:"tool_calls"`
	FunctionCall json.RawMessage   `json:"function_call"`
}

func validateNativeChatCapabilities(body json.RawMessage, model domain.ModelSourceModel, stream bool) error {
	if stream && !model.Streaming {
		return providerFailure("streaming_unsupported", http.StatusBadRequest, false)
	}
	var request struct {
		Tools             []json.RawMessage   `json:"tools"`
		ToolChoice        json.RawMessage     `json:"tool_choice"`
		Functions         []json.RawMessage   `json:"functions"`
		FunctionCall      json.RawMessage     `json:"function_call"`
		ParallelToolCalls *bool               `json:"parallel_tool_calls"`
		Messages          []nativeChatMessage `json:"messages"`
		Input             json.RawMessage     `json:"input"`
		ReasoningEffort   string              `json:"reasoning_effort"`
		ReasoningAlias    string              `json:"reasoningEffort"`
		Reasoning         json.RawMessage     `json:"reasoning"`
		Thinking          json.RawMessage     `json:"thinking"`
		EnableThinking    bool                `json:"enable_thinking"`
		IncludeReasoning  bool                `json:"include_reasoning"`
		SeparateReasoning bool                `json:"separate_reasoning"`
		StreamReasoning   bool                `json:"stream_reasoning"`
	}
	if json.Unmarshal(body, &request) != nil {
		return providerFailure("invalid_request", 400, false)
	}
	var input []nativeChatMessage
	if json.Unmarshal(request.Input, &input) == nil {
		request.Messages = append(request.Messages, input...)
	}
	toolRequested := len(request.Tools) > 0 || len(request.Functions) > 0 || request.ParallelToolCalls != nil && *request.ParallelToolCalls ||
		nativeToolChoiceRequested(request.ToolChoice) || nativeToolChoiceRequested(request.FunctionCall)
	imageRequested := false
	for _, message := range request.Messages {
		toolRequested = toolRequested || message.Role == "tool" || len(message.ToolCalls) > 0 || nativeControlRequested(message.FunctionCall) ||
			message.Type == "function_call" || message.Type == "custom_tool_call" || message.Type == "function_call_output" || message.Type == "custom_tool_call_output"
		imageRequested = imageRequested || message.Type == "image_url" || message.Type == "input_image"
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				imageRequested = imageRequested || part.Type == "image_url" || part.Type == "input_image"
			}
		}
	}
	if toolRequested && !model.Tools || imageRequested && !model.Vision {
		return providerFailure(upstream.ErrorCodeUnsupportedCapability, http.StatusBadRequest, false)
	}
	var metadata modelMetadata
	if strings.TrimSpace(model.RawMetadataJSON) != "" && json.Unmarshal([]byte(model.RawMetadataJSON), &metadata) != nil {
		return providerFailure("invalid_model_metadata", 0, false)
	}
	reasoningRequested := request.ReasoningEffort != "" || request.ReasoningAlias != "" || nativeControlRequested(request.Reasoning) || nativeThinkingRequested(request.Thinking) ||
		request.EnableThinking || request.IncludeReasoning || request.SeparateReasoning || request.StreamReasoning
	if !reasoningRequested {
		return nil
	}
	if !metadata.SupportsReasoning {
		return providerFailure(upstream.ErrorCodeUnsupportedCapability, http.StatusBadRequest, false)
	}
	efforts := []string{request.ReasoningEffort, request.ReasoningAlias}
	reasoningObjects := []json.RawMessage{request.Reasoning}
	if nativeThinkingRequested(request.Thinking) {
		reasoningObjects = append(reasoningObjects, request.Thinking)
	}
	for _, raw := range reasoningObjects {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil && object != nil {
			if value, ok := object["effort"]; ok {
				var effort string
				if json.Unmarshal(value, &effort) != nil {
					return providerFailure("invalid_request", 400, false)
				}
				efforts = append(efforts, effort)
			}
		}
	}
	var thinking string
	if nativeThinkingRequested(request.Thinking) && json.Unmarshal(request.Thinking, &thinking) == nil &&
		thinking != "enabled" && thinking != "adaptive" && thinking != "auto" {
		efforts = append(efforts, thinking)
	}
	supported := metadata.reasoningEfforts()
	if len(supported) == 0 {
		supported = []string{"low", "medium", "high"}
	}
	for _, effort := range efforts {
		effort = strings.TrimSpace(effort)
		if effort == "" {
			continue
		}
		matched := false
		for _, candidate := range supported {
			matched = matched || upstream.WireReasoningEffort(candidate) == upstream.WireReasoningEffort(effort)
		}
		if !matched {
			return providerFailure("reasoning_effort_unsupported", http.StatusBadRequest, false)
		}
	}
	return nil
}

func nativeToolChoiceRequested(raw json.RawMessage) bool {
	if !nativeControlRequested(raw) {
		return false
	}
	var choice string
	return json.Unmarshal(raw, &choice) != nil || choice != "none" && choice != "auto" && choice != ""
}

func nativeControlRequested(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != "false" && value != `"disabled"` && value != `"off"` && value != `"none"`
}

func nativeThinkingRequested(raw json.RawMessage) bool {
	if !nativeControlRequested(raw) {
		return false
	}
	var control struct {
		Type    string `json:"type"`
		Enabled *bool  `json:"enabled"`
	}
	return json.Unmarshal(raw, &control) != nil || control.Type != "disabled" && (control.Enabled == nil || *control.Enabled)
}

func (a *Adapter) nativeChatStream(ctx context.Context, source domain.ModelSource, endpoint string, body json.RawMessage, headers http.Header, emit func(application.NativeAPIEvent) error) (application.NativeAPIResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return application.NativeAPIResult{}, providerFailure("invalid_upstream_endpoint", 0, false)
	}
	request.Header = headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := a.config.HTTPClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return application.NativeAPIResult{}, err
		}
		return application.NativeAPIResult{}, providerFailure("upstream_unavailable", 502, true)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, maxOperationResponseBytes+1))
		if len(payload) > maxOperationResponseBytes {
			return application.NativeAPIResult{}, providerFailure("upstream_response_too_large", 502, true)
		}
		payload = redactCredential(payload, credentialFromAuthorization(headers.Get("Authorization")))
		result := nativeResult(operationResult(response, payload))
		if !result.UsageKnown && reportedOperationBilling(payload) {
			return result, providerFailure("invalid_upstream_usage", 502, true)
		}
		return result, nil
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return application.NativeAPIResult{}, providerFailure("invalid_stream", 502, true)
	}
	result := application.NativeAPIResult{Status: response.StatusCode}
	usageSeen, finishSeen, doneSeen := false, false, false
	eventCount := 0
	err = upstream.ReadSSE(response.Body, func(eventName string, payload []byte) error {
		eventCount++
		if eventCount > 100000 {
			return providerFailure("stream_incomplete", 502, true)
		}
		if len(bytes.TrimSpace(payload)) == 0 {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			if !usageSeen && !source.ProviderConfig.AllowMissingUsage {
				return providerFailure("usage_unavailable", 502, true)
			}
			if err := emit(application.NativeAPIEvent{Type: "chat.done", Data: payload, Wire: "chat"}); err != nil {
				return err
			}
			doneSeen = true
			return io.EOF
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(payload, &object) != nil || object == nil {
			return providerFailure("invalid_stream", 502, true)
		}
		if eventName == "error" || len(object["error"]) != 0 && string(object["error"]) != "null" {
			result.Response = redactCredential(payload, credentialFromAuthorization(headers.Get("Authorization")))
			result.Failed, result.ErrorCode = true, upstreamHTTPCode(502, result.Response)
		}
		if nativeUsagePresent(object) {
			usage, valid := operationUsage(object)
			if !valid {
				result.UsageKnown = false
				return providerFailure("invalid_upstream_usage", 502, true)
			}
			if !nativeChatUsageKnown(object) {
				result.UsageKnown = false
				return providerFailure("usage_unavailable", 502, true)
			}
			result.Usage, usageSeen, result.UsageKnown = usage, true, true
		}
		if tierRaw := object["service_tier"]; len(tierRaw) != 0 {
			tier, valid := operationServiceTier(tierRaw)
			if !valid || tier != "" && result.ServiceTier != "" && tier != result.ServiceTier {
				result.UsageKnown = false
				return providerFailure("invalid_upstream_usage", 502, true)
			}
			if tier != "" {
				result.ServiceTier = tier
			}
		}
		if result.Failed {
			return providerFailure(result.ErrorCode, 502, true)
		}
		var choices []struct {
			FinishReason string `json:"finish_reason"`
		}
		if raw := object["choices"]; len(raw) != 0 && json.Unmarshal(raw, &choices) != nil {
			return providerFailure("invalid_stream", 502, true)
		}
		if len(choices) != 0 && choices[0].FinishReason != "" {
			finishSeen = true
		}
		payload = redactCredential(payload, credentialFromAuthorization(headers.Get("Authorization")))
		return emit(application.NativeAPIEvent{Type: "chat.chunk", Data: payload, Wire: "chat"})
	})
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil && !(doneSeen && errors.Is(err, io.EOF)) {
		var failure *application.ProviderFailure
		if errors.As(err, &failure) {
			return result, err
		}
		return result, wrapUpstreamFailure(err)
	}
	if !doneSeen && !(source.ProviderConfig.AllowNoSSEDone && finishSeen) {
		return result, providerFailure("stream_incomplete", 502, true)
	}
	if !usageSeen && !source.ProviderConfig.AllowMissingUsage {
		return result, providerFailure("usage_unavailable", 502, true)
	}
	if !doneSeen {
		if err := emit(application.NativeAPIEvent{Type: "chat.done", Data: []byte("[DONE]"), Wire: "chat"}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func nativeUsagePresent(object map[string]json.RawMessage) bool {
	raw := bytes.TrimSpace(object["usage"])
	return len(raw) != 0 && !bytes.Equal(raw, []byte("null"))
}

func nativeChatUsageKnown(object map[string]json.RawMessage) bool {
	var usage struct {
		Prompt *int64 `json:"prompt_tokens"`
		Output *int64 `json:"completion_tokens"`
		Input  *int64 `json:"input_tokens"`
		Result *int64 `json:"output_tokens"`
	}
	return json.Unmarshal(object["usage"], &usage) == nil &&
		(usage.Prompt != nil || usage.Input != nil) && (usage.Output != nil || usage.Result != nil)
}

func (a *Adapter) Embeddings(ctx context.Context, target application.ResponseTarget, body json.RawMessage) (application.NativeAPIResult, error) {
	source, key, err := a.externalTarget(ctx, target.Account)
	if err != nil {
		return application.NativeAPIResult{}, err
	}
	sourceModel, ok := source.Model(modelFromBody(body))
	if !ok || !source.Enabled || !source.Embeddings || source.Kind != domain.ModelSourceOpenAICompatible {
		return application.NativeAPIResult{}, providerFailure("model_source_model_unavailable", 404, false)
	}
	ctx, release, err := a.beginSource(ctx, source)
	if err != nil {
		return application.NativeAPIResult{}, err
	}
	defer release()

	// Only the validated model is rewritten for an explicitly configured
	// upstream mapping. Every other field, including explicit nulls, remains
	// byte-for-byte present in the outbound JSON object.
	if sourceModel.UpstreamModel != "" && sourceModel.UpstreamModel != sourceModel.Model {
		var object map[string]json.RawMessage
		if json.Unmarshal(body, &object) != nil || object == nil {
			return application.NativeAPIResult{}, providerFailure("invalid_request", 400, false)
		}
		object["model"] = providerMustJSON(sourceModel.UpstreamModel)
		body, _ = json.Marshal(object)
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	result, err := a.operation(ctx, http.MethodPost, strings.TrimSuffix(source.BaseURL, "/")+"/embeddings", body, "application/json", headers, nil)
	if err != nil {
		return application.NativeAPIResult{}, err
	}
	var object map[string]json.RawMessage
	decoded := json.Unmarshal(result.Body, &object) == nil
	_, valid := operationUsage(object)
	known := decoded && valid && nativeEmbeddingsUsageKnown(object)
	parsed := application.NativeAPIResult{Status: result.Status, Response: result.Body, Usage: result.Usage,
		UsageKnown: known, Failed: result.Failed, ErrorCode: result.ErrorCode}
	if !known && reportedOperationBilling(result.Body) {
		return parsed, providerFailure("invalid_upstream_usage", 502, true)
	}
	if result.Failed || result.Status < 200 || result.Status >= 300 {
		return parsed, nil
	}
	if !known || result.Usage.InputTokens == 0 && result.Usage.OutputTokens == 0 {
		return parsed, providerFailure("usage_unavailable", 502, true)
	}
	return parsed, nil
}

func nativeEmbeddingsUsageKnown(object map[string]json.RawMessage) bool {
	var usage struct {
		Prompt *int64 `json:"prompt_tokens"`
		Input  *int64 `json:"input_tokens"`
		Total  *int64 `json:"total_tokens"`
	}
	return json.Unmarshal(object["usage"], &usage) == nil &&
		(usage.Prompt != nil || usage.Input != nil) && usage.Total != nil
}

func providerMustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func modelFromBody(body json.RawMessage) string {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	return request.Model
}

func nativeResult(result application.CodexOperationResult) application.NativeAPIResult {
	return application.NativeAPIResult{
		ResponseID: responseIDFromBody(result.Body), Status: result.Status, Response: result.Body,
		Usage: result.Usage, UsageKnown: result.UsageKnown, Failed: result.Failed, ErrorCode: result.ErrorCode, ServiceTier: result.ServiceTier,
	}
}

func responseIDFromBody(body []byte) string {
	var payload struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.ID
}

func credentialFromAuthorization(value string) string {
	return strings.TrimPrefix(value, "Bearer ")
}
