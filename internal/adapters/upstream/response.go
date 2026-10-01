package upstream

import (
	"encoding/json"
	"strings"
)

func mapRequestBody(chat ChatRequest, cap Capabilities) (json.RawMessage, error) {
	value, err := json.Marshal(chat)
	if err != nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "could not encode Chat Completions request"}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "Chat Completions request was not an object"}
	}
	for _, field := range cap.DropBodyFields {
		delete(object, field)
	}
	for field, value := range cap.ExtraBody {
		object[field] = value
	}
	if raw, ok := object["n"]; ok {
		var n uint64
		if err := json.Unmarshal(raw, &n); err != nil || n != 1 {
			return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "only one upstream completion (n=1) is supported"}
		}
	}
	NormalizeWireReasoning(object)
	return mustJSON(object), nil
}

func responsesResponseFromChat(responseID string, model string, chat chatWireResponse, catalog toolCatalog, requireUsage bool) (Result, ChatMessage, error) {
	result := Result{ResponseID: responseID, ServiceTier: chat.ServiceTier}
	result.readUsage(chat.Usage, ProtocolChatCompletions)
	if chat.Error != nil {
		result.Failed, result.ErrorCode, result.ErrorMessage = true, wireErrorCode(chat.Error), chat.Error.Message
		if result.ErrorCode == "" {
			result.ErrorCode = ErrorCodeUpstreamError
		}
		return result, ChatMessage{}, &Error{Code: result.ErrorCode, Status: 200, Message: chat.Error.Message}
	}
	if result.UsageReported && !result.UsageKnown {
		return result, ChatMessage{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream Chat response contains partial usage"}
	}
	if len(chat.Choices) != 1 || chat.Choices[0].Index != nil && *chat.Choices[0].Index != 0 {
		return result, ChatMessage{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream must return exactly one choice at index 0"}
	}
	if !result.UsageKnown && requireUsage {
		return result, ChatMessage{}, &Error{Code: ErrorCodeMissingUsage, Message: "upstream response omitted valid terminal usage"}
	}
	message := chat.Choices[0].Message
	if message.ReasoningContent == "" {
		message.ReasoningContent = message.Reasoning
	}
	message.Reasoning = ""
	completeHistoryToolArguments([]ChatMessage{message})
	output := make([]json.RawMessage, 0, 2)

	if strings.TrimSpace(message.ReasoningContent) != "" {
		output = append(output, mustJSON(map[string]any{
			"type": "reasoning", "id": newID("rs_"),
			"summary": []map[string]string{{"type": "summary_text", "text": message.ReasoningContent}},
		}))
	}
	text := chatText(message.Content)
	if text != "" || len(message.ToolCalls) == 0 {
		output = append(output, mustJSON(map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]string{{"type": "output_text", "text": text}},
		}))
	}
	for _, call := range message.ToolCalls {
		if call.ID == "" || !catalog.allowed[call.Function.Name] {
			return result, ChatMessage{}, &Error{Code: ErrorCodeInvalidToolCall, Message: "upstream returned an empty or undeclared tool call"}
		}
		item, err := toolCallResponseItem(call, catalog, "")
		if err != nil {
			return result, ChatMessage{}, err
		}
		output = append(output, item)
	}
	var wireUsage any
	if result.UsageKnown {
		wireUsage = result.Usage
	}
	payload := mustJSON(map[string]any{
		"id": responseID, "object": "response", "model": model,
		"output": output, "usage": wireUsage,
	})
	result.Response = payload
	return result, message, nil
}

func toolCallResponseItem(call ChatToolCall, catalog toolCatalog, itemID string) (json.RawMessage, error) {
	if itemID == "" {
		if _, custom := catalog.custom[call.Function.Name]; custom {
			itemID = newID("ctc_")
		} else {
			itemID = newID("fc_")
		}
	}
	if custom, ok := catalog.custom[call.Function.Name]; ok {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(call.Function.Arguments), &object) != nil {
			return nil, &Error{Code: ErrorCodeInvalidToolCall, Message: "custom tool call arguments are not a JSON object"}
		}
		input := call.Function.Arguments
		var text string
		if json.Unmarshal(object[custom.ArgumentField], &text) != nil || text == "" {
			input = call.Function.Arguments
		} else {
			input = text
		}
		return mustJSON(map[string]any{
			"type": "custom_tool_call", "id": itemID, "call_id": call.ID,
			"name": custom.Name, "input": input, "status": "completed",
		}), nil
	}
	name := call.Function.Name
	namespace := ""
	if mapped, ok := catalog.namespace[name]; ok {
		name = mapped.Name
		namespace = mapped.Namespace
	}
	item := map[string]any{
		"type": "function_call", "id": itemID, "call_id": call.ID,
		"name": name, "arguments": completedArguments(call.Function.Arguments), "status": "completed",
	}
	if namespace == "collaboration" && (name == "spawn_agent" || name == "send_message" || name == "followup_task") {
		item["encrypted_function_args"] = []string{}
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	return mustJSON(item), nil
}

func continuationFromTurn(requestMessages []ChatMessage, assistant ChatMessage, responseID string) Continuation {
	state := Continuation{
		Messages:        append(append([]ChatMessage(nil), requestMessages...), assistant),
		ReasoningByID:   make(map[string]string),
		ReasoningByTurn: make(map[uint64]string),
	}
	if assistant.ReasoningContent != "" {
		if text := chatText(assistant.Content); text != "" {
			state.ReasoningByTurn[turnReasoningKey(requestMessages, text)] = assistant.ReasoningContent
		}
		for _, call := range assistant.ToolCalls {
			state.ReasoningByID[call.ID] = assistant.ReasoningContent
		}
	}
	return state
}
