package application

import (
	"encoding/json"
	"slices"
	"strings"
)

// replayLiteBundle admits only self-contained tool declarations. Function JSON
// schemas are data, not account references; hosted storage/MCP tools stay pinned.
func replayLiteBundle(object map[string]json.RawMessage) bool {
	if compactString(object["role"]) != "developer" || !replayFields(object, "type", "role", "tools", "id") {
		return false
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(object["tools"], &tools) != nil || tools == nil {
		return false
	}
	for _, tool := range tools {
		kind := compactString(tool["type"])
		switch kind {
		case "function":
			if !replayFields(tool, "type", "name", "description", "parameters", "strict") || !replayNamedTool(tool) {
				return false
			}
			var schema map[string]json.RawMessage
			var strict *bool
			if raw := tool["parameters"]; len(raw) != 0 && json.Unmarshal(raw, &schema) != nil {
				return false
			}
			if raw := tool["strict"]; len(raw) != 0 && json.Unmarshal(raw, &strict) != nil {
				return false
			}
		case "custom":
			if !replayFields(tool, "type", "name", "description", "format") || !replayNamedTool(tool) {
				return false
			}
			var format map[string]json.RawMessage
			if raw := tool["format"]; len(raw) != 0 && json.Unmarshal(raw, &format) != nil {
				return false
			}
			if format != nil {
				switch compactString(format["type"]) {
				case "text":
					if !replayFields(format, "type") {
						return false
					}
				case "grammar":
					var definition string
					if !replayFields(format, "type", "syntax", "definition") ||
						!slices.Contains([]string{"lark", "regex"}, compactString(format["syntax"])) ||
						json.Unmarshal(format["definition"], &definition) != nil || string(format["definition"]) == "null" {
						return false
					}
				default:
					return false
				}
			}
		case "web_search", "web_search_preview":
			if !replayWebSearch(tool, kind) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func replayFields(object map[string]json.RawMessage, allowed ...string) bool {
	for key := range object {
		if !slices.Contains(allowed, key) {
			return false
		}
	}
	return true
}

func replayPatchCall(object map[string]json.RawMessage) bool {
	if !replayFields(object, "type", "call_id", "id", "input", "operation", "patch", "status", "caller", "internal_chat_message_metadata_passthrough") || !replayDirectCaller(object) {
		return false
	}
	forms := 0
	for _, field := range []string{"input", "operation", "patch"} {
		if _, exists := object[field]; exists {
			forms++
		}
	}
	if forms != 1 {
		return false
	}
	var operation map[string]json.RawMessage
	if raw := object["operation"]; len(raw) != 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &operation) != nil || !replayFields(operation, "type", "path", "diff") {
			return false
		}
		return slices.Contains([]string{"create_file", "delete_file", "update_file"}, compactString(operation["type"])) &&
			strings.TrimSpace(compactString(operation["path"])) != ""
	}
	return strings.TrimSpace(compactString(object["patch"])) != "" || strings.TrimSpace(compactString(object["input"])) != ""
}

func replayDirectCaller(object map[string]json.RawMessage) bool {
	var caller map[string]string
	if raw := object["caller"]; len(raw) != 0 && json.Unmarshal(raw, &caller) != nil {
		return false
	}
	return caller == nil || len(caller) == 1 && caller["type"] == "direct"
}

func replayNamedTool(tool map[string]json.RawMessage) bool {
	if strings.TrimSpace(compactString(tool["name"])) == "" {
		return false
	}
	var description *string
	return len(tool["description"]) == 0 || json.Unmarshal(tool["description"], &description) == nil
}

func replayWebSearch(tool map[string]json.RawMessage, kind string) bool {
	if !replayFields(tool, "type", "filters", "search_context_size", "user_location") {
		return false
	}
	if raw := tool["search_context_size"]; len(raw) != 0 && string(raw) != "null" &&
		!slices.Contains([]string{"low", "medium", "high"}, compactString(raw)) {
		return false
	}
	var filters map[string]json.RawMessage
	if raw := tool["filters"]; len(raw) != 0 && json.Unmarshal(raw, &filters) != nil {
		return false
	}
	if !replayFields(filters, "allowed_domains") {
		return false
	}
	var domains []string
	if raw := filters["allowed_domains"]; len(raw) != 0 && json.Unmarshal(raw, &domains) != nil {
		return false
	}
	for _, domain := range domains {
		if strings.TrimSpace(domain) == "" {
			return false
		}
	}
	var location map[string]json.RawMessage
	if raw := tool["user_location"]; len(raw) != 0 && json.Unmarshal(raw, &location) != nil {
		return false
	}
	if !replayFields(location, "type", "city", "country", "region", "timezone") {
		return false
	}
	for name, raw := range location {
		var value *string
		if json.Unmarshal(raw, &value) != nil || name == "type" && value != nil && *value != "approximate" {
			return false
		}
	}
	return location == nil || kind != "web_search_preview" || compactString(location["type"]) == "approximate"
}
