package application

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"codex-lb/internal/domain"
)

// compactReplay verifies complete neutral history before clearing the old
// owner's anchors. It never treats a trimmed delta as a complete conversation.
func (s *CodexOperations) compactReplay(ctx context.Context, prepared, original json.RawMessage, owner *domain.Continuation) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var request responseRequest
	_ = json.Unmarshal(prepared, &request.Object)
	_ = json.Unmarshal(request.Object["input"], &request.Input)
	if !compactReplayFields(request.Object) || accountBoundItems(request.Input) {
		return nil, replayUnavailable()
	}
	trigger := len(request.Input) > 0 && compactItemType(request.Input[len(request.Input)-1]) == "compaction_trigger"
	if trigger {
		request.Input = request.Input[:len(request.Input)-1]
	}
	if !compactNeutralItems(request.Input) {
		return nil, replayUnavailable()
	}
	var history responseContext
	if owner != nil {
		var source map[string]json.RawMessage
		_ = json.Unmarshal(original, &source)
		input, err := responseInput(source["input"])
		if err != nil {
			return nil, replayUnavailable()
		}
		if trigger {
			input = input[:len(input)-1]
		}
		if !compactItemsEqual(input, request.Input) {
			return nil, replayUnavailable() // Trim/image elision severed full-resend evidence.
		}
		if len(owner.ContextEncrypted) != 0 {
			if s.cipher == nil {
				return nil, replayUnavailable()
			}
			plain, err := s.cipher.Decrypt(owner.ContextEncrypted)
			if err != nil || len(plain) > maxReplayBytes || json.Unmarshal(plain, &history) != nil {
				return nil, replayUnavailable()
			}
			if accountBoundItems(history.Items) || !compactNeutralItems(history.Items) {
				return nil, replayUnavailable()
			}
		}
		if len(history.Items) > 0 {
			stored, current := compactComparableItems(history.Items), compactComparableItems(request.Input)
			if len(stored) == 0 {
				return nil, replayUnavailable()
			}
			if len(current) >= len(stored) && compactItemsEqual(current[:len(stored)], stored) {
				history.Items = nil // The client already included the complete retained prefix.
			} else if !compactInputDelta(request.Input) {
				return nil, replayUnavailable()
			}
		} else if !compactFullResend(request.Input, trigger) {
			return nil, replayUnavailable()
		}
	}
	replay, err := replayBody(request, history)
	if err != nil {
		return nil, replayUnavailable()
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(replay, &object)
	var input []json.RawMessage
	_ = json.Unmarshal(object["input"], &input)
	if trigger {
		input = append(input, json.RawMessage(`{"type":"compaction_trigger"}`))
	}
	if compactInputSize(input) > compactTokenBudget*4 {
		return nil, replayUnavailable() // A retry must not silently discard restored history.
	}
	object["input"], _ = json.Marshal(input)
	return json.Marshal(object)
}

func compactReplayFields(object map[string]json.RawMessage) bool {
	if !replayFields(object, "model", "instructions", "input", "reasoning", "store", "stream", "service_tier", "previous_response_id", "turn_state", "prompt_cache_key", "client_metadata", "conversation") {
		return false
	}
	if raw := object["conversation"]; len(raw) != 0 && string(raw) != "null" {
		return false
	}
	var reasoning map[string]json.RawMessage
	if raw := object["reasoning"]; len(raw) != 0 && json.Unmarshal(raw, &reasoning) != nil || !replayFields(reasoning, "effort", "summary", "context") {
		return false
	}
	var metadata map[string]json.RawMessage
	if raw := object["client_metadata"]; len(raw) != 0 && json.Unmarshal(raw, &metadata) != nil {
		return false
	}
	for name, raw := range metadata {
		switch strings.ToLower(name) {
		case ResponsesLiteMetadataKey, "x-codex-installation-id", "x-codex-parent-thread-id", "x-codex-turn-metadata", "x-codex-window-id", "x-openai-subagent":
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func compactItemType(item json.RawMessage) string {
	var object struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(item, &object)
	return object.Type
}

func compactComparableItems(input []json.RawMessage) []json.RawMessage {
	items := make([]json.RawMessage, 0, len(input))
	for _, raw := range input {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		if compactString(object["type"]) == "reasoning" {
			continue
		}
		delete(object, "id")
		if compactString(object["type"]) == "" && object["role"] != nil {
			object["type"] = jsonString("message")
		}
		if compactString(object["status"]) == "completed" {
			delete(object, "status")
		}
		encoded, _ := json.Marshal(object)
		items = append(items, encoded)
	}
	return items
}

func compactItemsEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		var left, right any
		decode := func(raw json.RawMessage, into *any) bool {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			return decoder.Decode(into) == nil
		}
		if !decode(a[i], &left) || !decode(b[i], &right) || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

func compactInputDelta(input []json.RawMessage) bool {
	for _, raw := range input {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(raw, &item)
		switch item.Type {
		case "function_call_output", "custom_tool_call_output", "apply_patch_call_output":
		case "", "message":
			if item.Role != "user" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func compactFullResend(input []json.RawMessage, trigger bool) bool {
	lastAssistant := -1
	finalAnswer := false
	for i, raw := range input {
		var item struct {
			Type, Role, Status, Phase string
			Content                   json.RawMessage
		}
		_ = json.Unmarshal(raw, &item)
		if (item.Type == "" || item.Type == "message") && item.Role == "assistant" &&
			(item.Status == "" || item.Status == "completed") && len(item.Content) > 0 && string(item.Content) != "null" {
			lastAssistant = i
			finalAnswer = item.Phase == "final_answer"
		}
	}
	if lastAssistant <= 0 || lastAssistant == len(input)-1 && !trigger {
		return false
	}
	for index, raw := range input[lastAssistant+1:] {
		var item struct {
			Type, Role, ID, Phase string
			Metadata              map[string]string `json:"internal_chat_message_metadata_passthrough"`
			Content               []struct{ Type string }
		}
		_ = json.Unmarshal(raw, &item)
		if item.Type != "" && item.Type != "message" {
			return false
		}
		if item.Role == "developer" && finalAnswer && index == 1 && lastAssistant+index+2 == len(input) &&
			item.ID == "" && item.Phase == "" && len(item.Metadata) == 1 && item.Metadata["turn_id"] != "" &&
			len(item.Content) == 1 && item.Content[0].Type == "input_text" {
			continue
		}
		if item.Role != "user" {
			return false
		}
	}
	return true
}
