package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func translateToChat(req responsesWireRequest, items []responsesItem, state Continuation, catalog toolCatalog, cap Capabilities, stream bool) (ChatRequest, []ChatMessage, error) {
	history := append([]ChatMessage(nil), state.Messages...)
	for i := range history {
		if history[i].ReasoningContent == "" {
			history[i].ReasoningContent = history[i].Reasoning
		}
		history[i].Reasoning = ""
	}
	completeHistoryToolArguments(history)

	latestContentless := latestDroppableMessage(history)
	messages := retainContentMessages(history)

	systemText := strings.TrimSpace(req.Instructions)
	if systemText == "" {
		systemText = strings.TrimSpace(req.System)
	}
	if systemText != "" && (len(messages) == 0 || messages[0].Role != "system") {
		messages = append([]ChatMessage{{Role: "system", Content: mustJSON(systemText)}}, messages...)
	} else if systemText != "" {
		messages[0].Content = mustJSON(systemText)
	}

	seenCalls, seenOutputs := historyCallIDs(history)
	pendingReasoning := ""

	for i := 0; i < len(items); {
		item := items[i]
		switch item.Type {
		case "message", "agent_message":
			if isContentlessResponseMessage(item) {
				latestContentless = appendContentlessFallback(latestContentless, item)
				i++
				continue
			}
			msg, err := responseMessageToChat(item, state, messages)
			if err != nil {
				return ChatRequest{}, nil, err
			}
			if msg.Role == "system" {
				if len(messages) > 0 && messages[0].Role == "system" {
					messages[0] = msg
				} else {
					messages = append([]ChatMessage{msg}, messages...)
				}
			} else {
				if msg.Role == "assistant" {
					msg.ReasoningContent = state.ReasoningByTurn[turnReasoningKey(messages, chatText(msg.Content))]
					if msg.ReasoningContent == "" {
						msg.ReasoningContent = pendingReasoning
					}
					pendingReasoning = ""
				}
				messages = append(messages, msg)
			}
			i++
		case "function_call", "custom_tool_call":
			group := []responsesItem{}
			for i < len(items) {
				current := items[i]
				if isContentlessResponseMessage(current) {
					latestContentless = appendContentlessFallback(latestContentless, current)
					i++
					continue
				}
				if current.Type != "function_call" && current.Type != "custom_tool_call" {
					break
				}
				group = append(group, current)
				i++
			}
			msg, ok, err := toolCallsToChat(group, seenCalls, catalog, state, pendingReasoning)
			if err != nil {
				return ChatRequest{}, nil, err
			}
			if ok {
				messages = append(messages, msg)
				pendingReasoning = ""
			}
		case "function_call_output", "custom_tool_call_output":
			if item.CallID == "" {
				return ChatRequest{}, nil, &Error{Code: ErrorCodeInvalidRequest, Message: "tool output has an empty call_id"}
			}
			if seenOutputs[item.CallID] {
				i++
				continue
			}
			if !seenCalls[item.CallID] {
				return ChatRequest{}, nil, &Error{Code: ErrorCodeInvalidRequest, Message: "tool output has no matching tool call"}
			}
			seenOutputs[item.CallID] = true
			messages = append(messages, ChatMessage{Role: "tool", Content: outputContent(item.Output), ToolCallID: item.CallID})
			i++
		case "reasoning":
			// Chat providers cannot consume Responses replay summaries directly.
			// Retained provider reasoning is restored above and by call ID below.
			i++
			if summary := reasoningSummary(item.Summary); summary != "" {
				pendingReasoning = summary
			}
		default:
			return ChatRequest{}, nil, unsupported(fmt.Sprintf("Responses input item type %q is not supported", item.Type))
		}
	}

	messages = retainContentMessages(messages)
	if len(messages) == 0 {
		if len(latestContentless) == 0 {
			latestContentless = []ChatMessage{{Role: "user", Content: mustJSON("")}}
		}
		messages = append(messages, latestContentless[len(latestContentless)-1])
	}

	model := req.Model
	if mapped, ok := cap.ModelMappings[model]; ok && strings.TrimSpace(mapped) != "" {
		model = mapped
	}
	chatReq := ChatRequest{
		Model:             model,
		Messages:          messages,
		Tools:             catalog.tools,
		Temperature:       req.Temperature,
		MaxTokens:         req.MaxOutputTokens,
		StreamOptions:     nil,
		ParallelToolCalls: req.ParallelToolCalls,
		Stream:            stream,
	}
	if stream {
		chatReq.StreamOptions = &ChatStreamOptions{IncludeUsage: true}
	}
	if cap.EnableGLMThinking {
		chatReq.Thinking = &ChatThinking{Type: "enabled"}
	}
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		chatReq.ReasoningEffort = req.Reasoning.Effort
	}
	return chatReq, messages, nil
}

func appendContentlessFallback(current []ChatMessage, item responsesItem) []ChatMessage {
	msg, err := responseMessageToChat(item, Continuation{}, nil)
	if err == nil {
		return append(current, msg)
	}
	return current
}

func responseMessageToChat(item responsesItem, state Continuation, prior []ChatMessage) (ChatMessage, error) {
	role := item.Role
	if item.Type == "agent_message" || role == "" {
		role = "user"
	}
	if item.Type == "agent_message" {
		role = "assistant"
	}
	switch role {
	case "user", "assistant":
	case "developer":
		role = "system"
	case "system":
	default:
		return ChatMessage{}, &Error{Code: ErrorCodeInvalidRequest, Message: fmt.Sprintf("unsupported Responses message role %q", item.Role)}
	}
	content, err := chatContent(item.Content)
	if err != nil {
		return ChatMessage{}, err
	}
	return ChatMessage{Role: role, Content: content}, nil
}

func toolCallsToChat(items []responsesItem, seen map[string]bool, catalog toolCatalog, state Continuation, pendingReasoning string) (ChatMessage, bool, error) {
	calls := make([]ChatToolCall, 0, len(items))
	reasoning := ""
	for _, item := range items {
		if item.CallID == "" {
			return ChatMessage{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: "tool call has an empty call_id"}
		}
		if seen[item.CallID] {
			continue
		}
		seen[item.CallID] = true
		name := item.Name
		if item.Namespace != "" {
			name = item.Namespace + "-" + item.Name
		}
		if !catalog.allowed[name] {
			return ChatMessage{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: fmt.Sprintf("replayed tool call %q was not declared in this request", name)}
		}
		var arguments string
		if item.Type == "custom_tool_call" {
			var input string
			if len(item.Input) > 0 && !bytes.Equal(item.Input, []byte("null")) {
				if err := json.Unmarshal(item.Input, &input); err != nil {
					return ChatMessage{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: "custom_tool_call.input must be a string"}
				}
			}
			field := "input"
			if custom, ok := catalog.custom[name]; ok {
				field = custom.ArgumentField
			}
			arguments = string(mustJSON(map[string]string{field: input}))
		} else {
			if len(item.Arguments) == 0 || bytes.Equal(item.Arguments, []byte("null")) {
				arguments = "{}"
			} else if err := json.Unmarshal(item.Arguments, &arguments); err != nil {
				return ChatMessage{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: "function_call.arguments must be a JSON string"}
			} else {
				var object map[string]json.RawMessage
				if json.Unmarshal([]byte(arguments), &object) != nil {
					return ChatMessage{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: "function_call.arguments must contain a JSON object"}
				}
			}
			arguments = completedArguments(arguments)
		}
		if reasoning == "" {
			reasoning = state.ReasoningByID[item.CallID]
		}
		calls = append(calls, ChatToolCall{
			ID: item.CallID, Type: "function",
			Function: ChatToolCallFunction{Name: name, Arguments: arguments},
		})
	}
	if len(calls) == 0 {
		return ChatMessage{}, false, nil
	}
	if reasoning == "" && pendingReasoning != "" {
		reasoning = pendingReasoning
	}
	return ChatMessage{Role: "assistant", ToolCalls: calls, ReasoningContent: reasoning}, true, nil
}

func chatContent(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, invalidInput(err)
		}
		return raw, nil
	}
	var source []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "message content must be a string or an array of parts"}
	}
	textOnly := true
	text := strings.Builder{}
	for _, part := range source {
		switch string(part["type"]) {
		case `"input_text"`, `"text"`, `"output_text"`:
			var value string
			_ = json.Unmarshal(part["text"], &value)
			text.WriteString(value)
		default:
			textOnly = false
		}
	}
	if textOnly {
		return mustJSON(text.String()), nil
	}
	out := make([]json.RawMessage, 0, len(source))
	for _, part := range source {
		switch string(part["type"]) {
		case `"input_text"`, `"text"`, `"output_text"`:
			var value string
			_ = json.Unmarshal(part["text"], &value)
			out = append(out, mustJSON(map[string]string{"type": "text", "text": value}))
		case `"input_image"`:
			var url string
			if err := json.Unmarshal(part["image_url"], &url); err != nil || url == "" {
				return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "input_image.image_url must be a non-empty string"}
			}
			out = append(out, mustJSON(map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}))
		case `"image_url"`:
			var url string
			if err := json.Unmarshal(part["image_url"], &url); err == nil && url != "" {
				out = append(out, mustJSON(map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}))
				continue
			}
			var object json.RawMessage
			if json.Unmarshal(part["image_url"], &object) == nil && len(object) > 0 && object[0] == '{' {
				out = append(out, mustJSON(map[string]any{"type": "image_url", "image_url": object}))
				continue
			}
			return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "image_url must contain a url"}
		default:
			return nil, unsupported(fmt.Sprintf("content part type %s is not translatable", string(part["type"])))
		}
	}
	return mustJSON(out), nil
}

func outputContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return mustJSON("")
	}
	if raw[0] == '"' {
		return raw
	}
	return mustJSON(string(raw))
}

func reasoningSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var summaries []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &summaries) != nil {
		return ""
	}
	values := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Text != "" {
			values = append(values, summary.Text)
		}
	}
	return strings.Join(values, "\n")
}

func historyCallIDs(messages []ChatMessage) (calls map[string]bool, outputs map[string]bool) {
	calls, outputs = make(map[string]bool), make(map[string]bool)
	for _, message := range messages {
		if message.ToolCallID != "" {
			outputs[message.ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			calls[call.ID] = true
		}
	}
	return calls, outputs
}

func latestDroppableMessage(messages []ChatMessage) []ChatMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if isDroppableContentMessage(messages[i]) {
			return []ChatMessage{messages[i]}
		}
	}
	return nil
}

func retainContentMessages(messages []ChatMessage) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages))
	for _, message := range messages {
		if !isDroppableContentMessage(message) {
			out = append(out, message)
		}
	}
	return out
}

func isContentlessResponseMessage(item responsesItem) bool {
	switch item.Role {
	case "user", "system", "developer":
	default:
		return false
	}
	if item.Type != "message" {
		return false
	}
	if len(item.Content) == 0 || bytes.Equal(item.Content, []byte("null")) {
		return true
	}
	var value any
	if json.Unmarshal(item.Content, &value) != nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		for _, partValue := range typed {
			part, _ := partValue.(map[string]any)
			kind, _ := part["type"].(string)
			switch kind {
			case "input_text", "text", "output_text":
				text, _ := part["text"].(string)
				if strings.TrimSpace(text) != "" {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isDroppableContentMessage(message ChatMessage) bool {
	if message.Role != "user" && message.Role != "system" {
		return false
	}
	if len(message.ToolCalls) != 0 || message.ToolCallID != "" {
		return false
	}
	content := message.Content
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return true
	}
	if content[0] == '"' {
		var text string
		_ = json.Unmarshal(content, &text)
		return strings.TrimSpace(text) == ""
	}
	if content[0] != '[' {
		return false
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return false
	}
	for _, part := range parts {
		switch string(part["type"]) {
		case `"text"`, `"input_text"`, `"output_text"`:
			var text string
			_ = json.Unmarshal(part["text"], &text)
			if strings.TrimSpace(text) != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func chatText(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var text string
	_ = json.Unmarshal(raw, &text)
	return text
}

func completedArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

func completeHistoryToolArguments(messages []ChatMessage) {
	for i := range messages {
		for j := range messages[i].ToolCalls {
			messages[i].ToolCalls[j].Function.Arguments = completedArguments(messages[i].ToolCalls[j].Function.Arguments)
		}
	}
}
