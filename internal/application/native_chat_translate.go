package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func ChatCompletionsToResponses(body json.RawMessage) (json.RawMessage, error) {
	var source map[string]json.RawMessage
	if json.Unmarshal(body, &source) != nil || source == nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Chat Completions request"}
	}
	rawModel := source["model"]
	if len(rawModel) == 0 {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "model is required"}
	}
	var model string
	if json.Unmarshal(rawModel, &model) != nil || strings.TrimSpace(model) == "" {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "model is required"}
	}
	if rawN := source["n"]; len(rawN) > 0 && !bytes.Equal(rawN, []byte("null")) {
		var count uint64
		if json.Unmarshal(rawN, &count) != nil || count != 1 {
			return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "only n=1 is supported"}
		}
	}

	messagesPresent := len(source["messages"]) != 0 && !bytes.Equal(source["messages"], []byte("null"))
	inputPresent := len(source["input"]) != 0 && !bytes.Equal(source["input"], []byte("null"))
	if !messagesPresent && !inputPresent {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Provide either messages or input"}
	}
	target := make(map[string]json.RawMessage, len(source)+3)
	for key, value := range source {
		switch key {
		case "messages", "tools", "tool_choice", "response_format", "max_tokens", "max_completion_tokens",
			"stream_options", "store", "n", "frequency_penalty", "presence_penalty", "logprobs",
			"top_logprobs", "seed", "stop":
		default:
			target[key] = value
		}
	}
	if raw, ok := source["max_completion_tokens"]; ok && !bytes.Equal(raw, []byte("null")) {
		target["max_output_tokens"] = raw
	} else if raw, ok := source["max_tokens"]; ok && !bytes.Equal(raw, []byte("null")) {
		target["max_output_tokens"] = raw
	}
	if raw, ok := source["response_format"]; ok && !bytes.Equal(raw, []byte("null")) {
		format, err := chatResponseFormatToText(raw)
		if err != nil {
			return nil, err
		}
		target["text"] = nativeMustJSON(map[string]json.RawMessage{"format": format})
	}
	if raw, ok := source["tools"]; ok && !bytes.Equal(raw, []byte("null")) {
		tools, err := chatToolsToResponses(raw)
		if err != nil {
			return nil, err
		}
		target["tools"] = tools
	}
	if raw, ok := source["tool_choice"]; ok && !bytes.Equal(raw, []byte("null")) {
		choice, err := chatToolChoiceToResponses(raw)
		if err != nil {
			return nil, err
		}
		target["tool_choice"] = choice
	}
	target["store"] = json.RawMessage("false")

	if !messagesPresent {
		return json.Marshal(target)
	}
	var messages []json.RawMessage
	if json.Unmarshal(source["messages"], &messages) != nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "messages must be an array"}
	}
	input := make([]json.RawMessage, 0, len(messages))
	for _, rawMessage := range messages {
		converted, err := chatMessageToResponseItems(rawMessage)
		if err != nil {
			return nil, err
		}
		input = append(input, converted...)
	}
	if len(input) == 0 && inputPresent {
		return json.Marshal(target)
	}
	delete(target, "input")
	target["input"] = nativeMustJSON(input)
	return json.Marshal(target)
}

func chatMessageToResponseItems(raw json.RawMessage) ([]json.RawMessage, error) {
	var message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &message) != nil || strings.TrimSpace(message.Role) == "" {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Each message must include a role"}
	}
	switch message.Role {
	case "system", "developer", "user", "assistant":
		content, err := chatContentToResponses(message.Role, message.Content)
		if err != nil {
			return nil, err
		}
		result := make([]json.RawMessage, 0, len(message.ToolCalls)+1)
		if len(content) != 0 || len(message.ToolCalls) == 0 {
			result = append(result, nativeMustJSON(map[string]json.RawMessage{
				"type": nativeMustJSON("message"), "role": nativeMustJSON(message.Role), "content": content,
			}))
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.Function.Name) == "" {
				return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Assistant tool calls require a function name"}
			}
			result = append(result, nativeMustJSON(map[string]any{
				"type": "function_call", "call_id": call.ID, "name": call.Function.Name,
				"arguments": completedJSONArguments(call.Function.Arguments), "status": "completed",
			}))
		}
		return result, nil
	case "tool":
		if strings.TrimSpace(message.ToolCallID) == "" {
			return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Tool messages require tool_call_id"}
		}
		return []json.RawMessage{nativeMustJSON(map[string]json.RawMessage{
			"type": nativeMustJSON("function_call_output"), "call_id": nativeMustJSON(message.ToolCallID),
			"output": chatToolOutput(message.Content),
		})}, nil
	default:
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Unsupported Chat Completions role: " + message.Role}
	}
}

func chatContentToResponses(role string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		return raw, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Message content must be a string or array"}
	}
	out := make([]json.RawMessage, 0, len(parts))
	textKind, imageKind := "input_text", "input_image"
	if role == "assistant" {
		textKind = "output_text"
	}
	for _, part := range parts {
		switch string(part["type"]) {
		case `"text"`, `"input_text"`, `"output_text"`:
			out = append(out, nativeMustJSON(map[string]json.RawMessage{"type": nativeMustJSON(textKind), "text": part["text"]}))
		case `"image_url"`:
			var image struct {
				ImageURL json.RawMessage `json:"image_url"`
			}
			if json.Unmarshal(nativeMustJSON(part), &image) != nil || len(image.ImageURL) == 0 {
				return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "image_url is required"}
			}
			var urlValue string
			if json.Unmarshal(image.ImageURL, &urlValue) == nil {
				out = append(out, nativeMustJSON(map[string]any{"type": imageKind, "image_url": urlValue}))
				continue
			}
			out = append(out, nativeMustJSON(map[string]json.RawMessage{"type": nativeMustJSON(imageKind), "image_url": image.ImageURL}))
		default:
			return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Unsupported Chat Completions content type"}
		}
	}
	return nativeMustJSON(out), nil
}

func chatToolsToResponses(raw json.RawMessage) (json.RawMessage, error) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "tools must be an array"}
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if nested, ok := tool["function"]; ok {
			var function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      *bool           `json:"strict"`
			}
			if json.Unmarshal(nested, &function) != nil || strings.TrimSpace(function.Name) == "" {
				return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Function tools require a name"}
			}
			item := map[string]any{"type": "function", "name": function.Name}
			if function.Description != "" {
				item["description"] = function.Description
			}
			if function.Parameters != nil {
				item["parameters"] = function.Parameters
			}
			if function.Strict != nil {
				item["strict"] = *function.Strict
			}
			out = append(out, nativeMustJSON(item))
			continue
		}
		if _, ok := tool["name"]; ok {
			out = append(out, nativeMustJSON(tool))
		}
	}
	return nativeMustJSON(out), nil
}

func chatToolChoiceToResponses(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid tool_choice"}
	}
	if _, ok := value.(string); ok {
		return raw, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid tool_choice"}
	}
	if function, ok := object["function"].(map[string]any); ok {
		if name, ok := function["name"].(string); ok && name != "" {
			return nativeMustJSON(map[string]string{"type": "function", "name": name}), nil
		}
	}
	return raw, nil
}

func chatResponseFormatToText(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid response_format"}
	}
	switch value.(type) {
	case string:
		var kind string
		_ = json.Unmarshal(raw, &kind)
		if kind == "json_object" || kind == "text" {
			return nativeMustJSON(map[string]string{"type": kind}), nil
		}
	case map[string]any:
		var format struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
				Strict *bool           `json:"strict"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(raw, &format) == nil && format.Type == "json_schema" && len(format.JSONSchema.Schema) != 0 {
			item := map[string]any{"type": "json_schema", "schema": format.JSONSchema.Schema}
			if format.JSONSchema.Name != "" {
				item["name"] = format.JSONSchema.Name
			}
			if format.JSONSchema.Strict != nil {
				item["strict"] = *format.JSONSchema.Strict
			}
			return nativeMustJSON(item), nil
		}
	}
	return nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "Unsupported response_format"}
}

func completedJSONArguments(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

func chatToolOutput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nativeMustJSON("")
	}
	return raw
}

func ResponsesToChatCompletion(response json.RawMessage) (json.RawMessage, error) {
	var payload struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
		Usage struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			TotalTokens        int64 `json:"total_tokens"`
			InputTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(response, &payload) != nil {
		return nil, errors.New("invalid Responses payload")
	}
	content := strings.Builder{}
	toolCalls := make([]map[string]any, 0)
	for _, item := range payload.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					content.WriteString(part.Text)
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, map[string]any{
				"id": item.CallID, "type": "function",
				"function": map[string]any{"name": item.Name, "arguments": completedJSONArguments(item.Arguments)},
			})
		}
	}
	message := map[string]any{"role": "assistant"}
	if len(toolCalls) == 0 {
		message["content"] = content.String()
	} else {
		message["tool_calls"] = toolCalls
	}
	usage := map[string]any{
		"prompt_tokens": payload.Usage.InputTokens, "completion_tokens": payload.Usage.OutputTokens,
		"total_tokens": payload.Usage.TotalTokens,
	}
	if payload.Usage.InputTokensDetails != nil {
		usage["prompt_tokens_details"] = map[string]int64{"cached_tokens": payload.Usage.InputTokensDetails.CachedTokens}
	}
	if payload.Usage.OutputTokensDetails != nil {
		usage["completion_tokens_details"] = map[string]int64{"reasoning_tokens": payload.Usage.OutputTokensDetails.ReasoningTokens}
	}
	return nativeMustJSON(map[string]any{
		"id": payload.ID, "object": "chat.completion", "created": timeNowUnix(),
		"model": payload.Model, "choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": finishReason(len(toolCalls) != 0),
		}}, "usage": usage,
	}), nil
}

func timeNowUnix() int64 { return time.Now().Unix() }

func nativeMustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func finishReason(tools bool) string {
	if tools {
		return "tool_calls"
	}
	return "stop"
}
