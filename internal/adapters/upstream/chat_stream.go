package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

type chatStreamState struct {
	target         Target
	model          string
	catalog        toolCatalog
	emit           func(Event) error
	responseID     string
	messageID      string
	reasoningID    string
	text           strings.Builder
	reasoning      strings.Builder
	toolCalls      map[int]ChatToolCall
	toolItemIDs    map[int]string
	outputIndexes  map[string]int
	nextIndex      int
	usage          Usage
	usageSeen      bool
	usageReported  bool
	serviceTier    string
	finishReason   string
	terminalChoice bool
	failed         bool
	errorCode      string
}

func (a *HTTPAdapter) streamChat(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog, emit func(Event) error) (Result, error) {
	continuation, err := a.loadContinuation(target, req)
	if err != nil {
		return Result{}, err
	}
	chat, requestMessages, err := translateToChat(req, items, continuation, catalog, target.Capabilities, true)
	if err != nil {
		return Result{}, err
	}
	body, err := mapRequestBody(chat, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	state := &chatStreamState{
		target: target, model: req.Model, catalog: catalog, emit: emit,
		responseID: newID("resp_"), messageID: newID("msg_"), reasoningID: newID("rs_"),
		toolCalls: make(map[int]ChatToolCall), toolItemIDs: make(map[int]string),
		outputIndexes: make(map[string]int),
	}
	req.Raw, req.Input, req.Tools = nil, nil, nil
	items = nil
	response, rejected, err := a.beginStream(ctx, target, endpoint(target.BaseURL, "chat/completions"), body)
	if err != nil {
		return rejected, state.fail(wrapContext(ctx, err))
	}
	defer response.Body.Close()
	if err := state.emitJSON("response.created", map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": state.responseID, "status": "in_progress", "model": state.model},
	}); err != nil {
		return state.result(), err
	}

	doneSeen := false
	eventCount := 0
	err = readSSE(response.Body, func(eventName string, data []byte) error {
		eventCount++
		if eventCount > maxSSEEvents {
			return &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream event count exceeds bounded limit"}
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			doneSeen = true
			return io.EOF
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		var chunk chatStreamChunk
		if err := decodeExactJSON(data, &chunk); err != nil {
			return err
		}
		return state.push(chunk)
	})
	if err != nil && !(doneSeen && errors.Is(err, io.EOF)) {
		return state.result(), state.fail(wrapContext(ctx, err))
	}
	if !doneSeen && target.Capabilities.AllowNoSSEDone && state.finishReason != "" {
		doneSeen = true
	}
	if !doneSeen {
		return state.result(), state.fail(&Error{Code: ErrorCodeStreamIncomplete, Message: "upstream stream disconnected before [DONE]"})
	}
	if !target.Capabilities.AllowMissingUsage && !state.usageSeen {
		return state.result(), state.fail(&Error{Code: ErrorCodeMissingUsage, Message: "upstream stream omitted terminal usage"})
	}
	if err := state.validateToolCalls(); err != nil {
		return state.result(), state.fail(err)
	}

	terminal, _, continuation, err := state.complete(requestMessages)
	if err != nil {
		return state.result(), state.fail(err)
	}
	if state.usageSeen {
		if err := a.store.Save(ownerOf(target), state.responseID, continuation); err != nil {
			return terminal, state.fail(err)
		}
	}
	if err := state.emitTerminal(terminal); err != nil {
		return terminal, err
	}
	return terminal, nil
}

func (s *chatStreamState) result() Result {
	return Result{ResponseID: s.responseID, Usage: s.usage, UsageKnown: s.usageSeen, UsageReported: s.usageReported, ServiceTier: s.serviceTier, Failed: s.errorCode != "", ErrorCode: s.errorCode}
}

func (s *chatStreamState) push(chunk chatStreamChunk) error {
	var result Result
	result.readUsage(chunk.Usage, ProtocolChatCompletions)
	if result.UsageReported {
		s.usage, s.usageSeen, s.usageReported = result.Usage, result.UsageKnown, true
	}
	if chunk.Error != nil {
		code := wireErrorCode(chunk.Error)
		if code == "" {
			code = ErrorCodeUpstreamError
		}
		s.errorCode = code
		return &Error{Code: code, Message: chunk.Error.Message}
	}
	if result.UsageReported && !result.UsageKnown {
		return &Error{Code: ErrorCodeInvalidStream, Message: "upstream Chat stream contains partial usage"}
	}
	if chunk.ServiceTier != "" {
		s.serviceTier = chunk.ServiceTier
	}
	if len(chunk.Choices) > 1 || len(chunk.Choices) == 1 && chunk.Choices[0].Index != nil && *chunk.Choices[0].Index != 0 {
		return &Error{Code: ErrorCodeInvalidStream, Message: "only upstream choice index 0 is supported"}
	}
	if s.terminalChoice && len(chunk.Choices) != 0 {
		return &Error{Code: ErrorCodeInvalidStream, Message: "upstream sent choice data after finish_reason"}
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != "" {
			s.finishReason = choice.FinishReason
			s.terminalChoice = true
		}
		if choice.Delta.ReasoningContent != "" || choice.Delta.Reasoning != "" {
			delta := choice.Delta.ReasoningContent
			if delta == "" {
				delta = choice.Delta.Reasoning
			}
			if err := s.emitText("reasoning", delta); err != nil {
				return err
			}
		}
		if choice.Delta.Content != "" {
			if err := s.emitText("message", choice.Delta.Content); err != nil {
				return err
			}
		}
		for index, call := range choice.Delta.ToolCalls {
			callIndex := index
			if call.Index != nil {
				callIndex = *call.Index
			}
			existing := s.toolCalls[callIndex]
			if call.ID != "" {
				existing.ID = call.ID
			}
			if call.Function != nil {
				if call.Function.Name != "" {
					existing.Function.Name += call.Function.Name
				}
				existing.Function.Arguments += call.Function.Arguments
			}
			existing.Type = "function"
			s.toolCalls[callIndex] = existing
		}
	}
	return nil
}

func (s *chatStreamState) emitText(kind string, delta string) error {
	itemID := s.messageID
	itemType := "message"
	if kind == "reasoning" {
		itemID = s.reasoningID
		itemType = "reasoning"
	}
	outputIndex, exists := s.outputIndexes[kind]
	if !exists {
		outputIndex = s.nextIndex
		s.nextIndex++
		s.outputIndexes[kind] = outputIndex
		item := map[string]any{"type": itemType, "id": itemID}
		if itemType == "message" {
			item["role"], item["status"], item["content"] = "assistant", "in_progress", []any{}
		} else {
			item["summary"] = []any{}
		}
		if err := s.emitJSON("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": outputIndex, "item": item,
		}); err != nil {
			return err
		}
	}
	if kind == "reasoning" {
		s.reasoning.WriteString(delta)
		return s.emitJSON("response.reasoning_text.delta", map[string]any{
			"type": "response.reasoning_text.delta", "item_id": itemID,
			"output_index": outputIndex, "content_index": 0, "delta": delta,
		})
	}
	s.text.WriteString(delta)
	return s.emitJSON("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "item_id": itemID,
		"output_index": outputIndex, "delta": delta,
	})
}

func (s *chatStreamState) validateToolCalls() error {
	seen := make(map[string]bool)
	for _, index := range s.toolCallIndexes() {
		call := s.toolCalls[index]
		if call.ID == "" || seen[call.ID] {
			return &Error{Code: ErrorCodeInvalidToolCall, Message: "upstream returned an empty or duplicate tool call ID"}
		}
		seen[call.ID] = true
		if !s.catalog.allowed[call.Function.Name] {
			return &Error{Code: ErrorCodeInvalidToolCall, Message: "upstream returned an undeclared tool call"}
		}
	}
	return nil
}

func (s *chatStreamState) toolCallIndexes() []int {
	indexes := make([]int, 0, len(s.toolCalls))
	for index := range s.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func (s *chatStreamState) complete(requestMessages []ChatMessage) (Result, ChatMessage, Continuation, error) {
	text := s.text.String()
	assistant := ChatMessage{Role: "assistant", Content: mustJSON(text), ReasoningContent: s.reasoning.String()}
	for _, index := range s.toolCallIndexes() {
		call := s.toolCalls[index]
		call.Function.Arguments = completedArguments(call.Function.Arguments)
		s.toolCalls[index] = call
		assistant.ToolCalls = append(assistant.ToolCalls, call)
	}
	type indexedOutput struct {
		index int
		value json.RawMessage
	}
	outputs := make([]indexedOutput, 0, len(s.toolCalls)+2)
	if assistant.ReasoningContent != "" {
		outputs = append(outputs, indexedOutput{s.outputIndexes["reasoning"], mustJSON(map[string]any{
			"type": "reasoning", "id": s.reasoningID,
			"summary": []map[string]string{{"type": "summary_text", "text": assistant.ReasoningContent}},
		})})
	}
	if text != "" || len(assistant.ToolCalls) == 0 {
		outputs = append(outputs, indexedOutput{s.outputIndexes["message"], mustJSON(map[string]any{
			"type": "message", "id": s.messageID, "role": "assistant", "status": "completed",
			"content": []map[string]string{{"type": "output_text", "text": text}},
		})})
	}
	for offset, index := range s.toolCallIndexes() {
		call := assistant.ToolCalls[offset]
		itemID := newID("fc_")
		if _, custom := s.catalog.custom[call.Function.Name]; custom {
			itemID = newID("ctc_")
		}
		s.toolItemIDs[index] = itemID
		item, err := toolCallResponseItem(call, s.catalog, itemID)
		if err != nil {
			return Result{}, ChatMessage{}, Continuation{}, err
		}
		outputs = append(outputs, indexedOutput{s.nextIndex + offset, item})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].index < outputs[j].index })
	output := make([]json.RawMessage, len(outputs))
	for i, item := range outputs {
		output[i] = item.value
	}
	var wireUsage any
	if s.usageSeen {
		wireUsage = s.usage
	}
	payload := mustJSON(map[string]any{
		"id": s.responseID, "object": "response", "model": s.model,
		"output": output, "usage": wireUsage,
	})
	return Result{
		ResponseID: s.responseID, Response: payload, Usage: s.usage, UsageKnown: s.usageSeen, UsageReported: s.usageReported, ServiceTier: s.serviceTier,
	}, assistant, continuationFromTurn(requestMessages, assistant, s.responseID), nil
}

func (s *chatStreamState) emitTerminal(terminal Result) error {
	if index, ok := s.outputIndexes["reasoning"]; ok {
		if err := s.emitItemDone(index, mustJSON(map[string]any{
			"type": "reasoning", "id": s.reasoningID,
			"summary": []map[string]string{{"type": "summary_text", "text": s.reasoning.String()}},
		})); err != nil {
			return err
		}
	}
	if index, ok := s.outputIndexes["message"]; ok {
		if err := s.emitItemDone(index, mustJSON(map[string]any{
			"type": "message", "id": s.messageID, "role": "assistant", "status": "completed",
			"content": []map[string]string{{"type": "output_text", "text": s.text.String()}},
		})); err != nil {
			return err
		}
	}
	base := s.nextIndex
	for offset, index := range s.toolCallIndexes() {
		call := s.toolCalls[index]
		itemID := s.toolItemIDs[index]
		done, err := toolCallResponseItem(call, s.catalog, itemID)
		if err != nil {
			return err
		}
		var doneObject map[string]json.RawMessage
		if err := json.Unmarshal(done, &doneObject); err != nil {
			return err
		}
		added := make(map[string]json.RawMessage, len(doneObject))
		for key, value := range doneObject {
			switch key {
			case "status":
				added[key] = mustJSON("in_progress")
			case "arguments", "input":
				added[key] = mustJSON("")
			default:
				added[key] = value
			}
		}
		outputIndex := base + offset
		if err := s.emitJSON("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": outputIndex, "item": added,
		}); err != nil {
			return err
		}
		if _, custom := s.catalog.custom[call.Function.Name]; custom {
			var doneItem map[string]json.RawMessage
			_ = json.Unmarshal(done, &doneItem)
			var input string
			_ = json.Unmarshal(doneItem["input"], &input)
			if input != "" {
				if err := s.emitJSON("response.custom_tool_call_input.delta", map[string]any{
					"type": "response.custom_tool_call_input.delta", "item_id": itemID,
					"output_index": outputIndex, "delta": input,
				}); err != nil {
					return err
				}
			}
		} else if call.Function.Arguments != "" {
			if err := s.emitJSON("response.function_call_arguments.delta", map[string]any{
				"type": "response.function_call_arguments.delta", "item_id": itemID,
				"output_index": outputIndex, "delta": call.Function.Arguments,
			}); err != nil {
				return err
			}
		}
		if err := s.emitItemDone(outputIndex, done); err != nil {
			return err
		}
	}
	var wireUsage any
	if s.usageSeen {
		wireUsage = s.usage
	}
	return s.emitJSON("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": s.responseID, "status": "completed", "model": s.model,
			"output": decodeOutputItems(terminal.Response), "usage": wireUsage,
		},
	})
}

func (s *chatStreamState) emitItemDone(index int, item json.RawMessage) error {
	return s.emitJSON("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": index, "item": item,
	})
}

func decodeOutputItems(response json.RawMessage) []json.RawMessage {
	var payload struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(response, &payload)
	return payload.Output
}

func (s *chatStreamState) fail(err error) error {
	if s.failed {
		return err
	}
	s.failed = true
	code := ErrorCodeUpstreamError
	var upstreamErr *Error
	if errors.As(err, &upstreamErr) && upstreamErr.Code != "" {
		code = upstreamErr.Code
	}
	message := err.Error()
	if errors.Is(err, context.Canceled) {
		code, message = ErrorCodeConnection, context.Canceled.Error()
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = ErrorCodeConnection, context.DeadlineExceeded.Error()
	}
	_ = s.emitJSON("response.failed", map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": s.responseID, "status": "failed",
			"error": map[string]string{"code": code, "message": message},
		},
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if upstreamErr != nil {
		return upstreamErr
	}
	return &Error{Code: code, Message: message}
}

func (s *chatStreamState) emitJSON(eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &Error{Code: ErrorCodeInvalidConfiguration, Message: "could not encode Responses event"}
	}
	return s.emit(Event{Type: eventType, Data: data})
}
