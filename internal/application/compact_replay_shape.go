package application

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

// Unknown payload fields may encode owner-local state. The compact replay gate
// only projects the self-contained message/tool shapes it understands.
func compactNeutralItems(items []json.RawMessage) bool {
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return false
		}
		kind := compactString(item["type"])
		if kind == "reasoning" {
			continue // replayBody drops the provider reasoning cache.
		}
		status := compactString(item["status"])
		if status != "" && status != "completed" && status != "failed" {
			return false
		}
		if raw := item["internal_chat_message_metadata_passthrough"]; len(raw) != 0 && string(raw) != "null" {
			var metadata map[string]string
			if json.Unmarshal(raw, &metadata) != nil || len(metadata) != 1 || metadata["turn_id"] == "" {
				return false
			}
		}
		switch kind {
		case "additional_tools":
			if !replayLiteBundle(item) {
				return false
			}
		case "", "message":
			role := compactString(item["role"])
			phase := compactString(item["phase"])
			if !replayFields(item, "type", "role", "id", "content", "status", "phase", "internal_chat_message_metadata_passthrough") ||
				!slices.Contains([]string{"user", "assistant", "developer", "system"}, role) ||
				!slices.Contains([]string{"", "commentary", "final_answer"}, phase) || !compactNeutralContent(item["content"], role == "assistant") {
				return false
			}
		case "function_call", "custom_tool_call":
			field := "arguments"
			if kind == "custom_tool_call" {
				field = "input"
			}
			var argument string
			if !replayFields(item, "type", "id", "name", "call_id", field, "status", "caller", "internal_chat_message_metadata_passthrough") || !replayDirectCaller(item) ||
				strings.TrimSpace(compactString(item["name"])) == "" || json.Unmarshal(item[field], &argument) != nil || string(item[field]) == "null" || status == "failed" {
				return false
			}
		case "apply_patch_call":
			if !replayPatchCall(item) || status == "failed" {
				return false
			}
		case "function_call_output", "custom_tool_call_output", "apply_patch_call_output":
			if !replayFields(item, "type", "id", "call_id", "output", "status", "caller", "internal_chat_message_metadata_passthrough") || !replayDirectCaller(item) {
				return false
			}
			if kind == "apply_patch_call_output" && (len(item["output"]) == 0 || string(item["output"]) == "null") && (status == "completed" || status == "failed") {
				continue
			}
			var output string
			if json.Unmarshal(item["output"], &output) != nil && !compactNeutralContent(item["output"], false) || string(item["output"]) == "null" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func compactNeutralContent(raw json.RawMessage, assistant bool) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		kind := compactString(part["type"])
		if assistant && kind != "output_text" && kind != "refusal" {
			return false
		}
		switch kind {
		case "input_text", "output_text", "text", "refusal":
			field := "text"
			if kind == "refusal" {
				field = "refusal"
			}
			if !replayFields(part, "type", field, "annotations", "logprobs") || strings.TrimSpace(compactString(part[field])) == "" {
				return false
			}
			for _, field := range []string{"annotations", "logprobs"} {
				if value := part[field]; len(value) != 0 && string(value) != "null" && string(value) != "[]" {
					return false
				}
			}
		case "input_image":
			if !replayFields(part, "type", "detail", "image_url") || !compactNeutralURL(compactString(part["image_url"]), true) {
				return false
			}
		case "input_file":
			if !replayFields(part, "type", "filename", "file_data", "file_url") {
				return false
			}
			if compactString(part["file_data"]) == "" && !compactNeutralURL(compactString(part["file_url"]), false) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func compactNeutralURL(value string, image bool) bool {
	if image && strings.HasPrefix(value, "data:image/") {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}
