package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var chatToolNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type namespaceTool struct {
	Namespace string
	Name      string
}

type customTool struct {
	Name          string
	ArgumentField string
}

type toolCatalog struct {
	tools     []json.RawMessage
	namespace map[string]namespaceTool
	custom    map[string]customTool
	allowed   map[string]bool
}

func decodeResponsesRequest(body []byte, maxBytes int64) (responsesWireRequest, error) {
	if int64(len(body)) > maxBytes {
		return responsesWireRequest{}, &Error{Code: ErrorCodeInvalidRequest, Message: "request body exceeds configured limit"}
	}
	if !json.Valid(body) {
		return responsesWireRequest{}, &Error{Code: ErrorCodeInvalidRequest, Message: "request body is not valid JSON"}
	}
	var req responsesWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return responsesWireRequest{}, &Error{Code: ErrorCodeInvalidRequest, Message: "invalid Responses request: " + err.Error()}
	}
	req.Raw = body
	if strings.TrimSpace(req.Model) == "" {
		return responsesWireRequest{}, &Error{Code: ErrorCodeInvalidRequest, Message: "model is required"}
	}
	if len(req.Input) == 0 || bytes.Equal(req.Input, []byte("null")) {
		return responsesWireRequest{}, &Error{Code: ErrorCodeInvalidRequest, Message: "input is required"}
	}
	return req, nil
}

func parseResponsesInput(raw json.RawMessage) (string, []responsesItem, error) {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", nil, invalidInput(err)
		}
		return text, nil, nil
	}
	var items []responsesItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", nil, invalidInput(err)
	}
	for i := range items {
		if items[i].Type == "" && items[i].Role != "" {
			items[i].Type = "message"
		}
	}
	return "", items, nil
}

func invalidInput(err error) error {
	return &Error{Code: ErrorCodeInvalidRequest, Message: "invalid Responses input: " + err.Error()}
}

func (req responsesWireRequest) items() ([]responsesItem, error) {
	text, items, err := parseResponsesInput(req.Input)
	if err != nil {
		return nil, err
	}
	if req.Input[0] == '"' {
		return []responsesItem{{Type: "message", Role: "user", Content: mustJSON(text)}}, nil
	}
	return items, nil
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func newToolCatalog(req responsesWireRequest, cap Capabilities) (toolCatalog, error) {
	catalog := toolCatalog{
		namespace: make(map[string]namespaceTool),
		custom:    make(map[string]customTool),
		allowed:   make(map[string]bool),
	}
	hosted := make(map[string]bool)
	for _, name := range cap.AllowedHostedTools {
		hosted[name] = true
	}
	if len(req.Tools) > 0 && !cap.Tools {
		return catalog, unsupported("tool declarations are not supported by this provider")
	}
	seen := make(map[string]bool)

	declare := func(name string, custom customTool, ns namespaceTool, raw json.RawMessage) error {
		if !chatToolNameRE.MatchString(name) {
			return &Error{Code: ErrorCodeInvalidRequest, Message: fmt.Sprintf("tool name %q is not a valid Chat Completions name", name)}
		}
		if seen[name] {
			return &Error{Code: ErrorCodeInvalidRequest, Message: fmt.Sprintf("ambiguous tool name %q: declared more than once", name)}
		}
		seen[name] = true
		catalog.allowed[name] = true
		if custom.Name != "" {
			catalog.custom[name] = custom
		}
		if ns.Namespace != "" {
			catalog.namespace[name] = ns
		}
		catalog.tools = append(catalog.tools, raw)
		return nil
	}

	for _, raw := range req.Tools {
		var tool struct {
			Type        string            `json:"type"`
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Parameters  json.RawMessage   `json:"parameters"`
			Strict      *bool             `json:"strict"`
			Tools       []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "invalid tool declaration: " + err.Error()}
		}
		switch tool.Type {
		case "function":
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "function tool name is required"}
			}
			fn := map[string]any{"name": name}
			if tool.Description != "" {
				fn["description"] = tool.Description
			}
			if tool.Parameters != nil {
				fn["parameters"] = tool.Parameters
			}
			if tool.Strict != nil {
				fn["strict"] = *tool.Strict
			}
			custom := customTool{}
			if name == "apply_patch" {
				custom = customTool{Name: name, ArgumentField: functionStringField(tool.Parameters)}
				if custom.ArgumentField == "" {
					custom.ArgumentField = "patch"
				}
			}
			if err := declare(name, custom, namespaceTool{}, mustJSON(map[string]any{
				"type": "function", "function": fn,
			})); err != nil {
				return catalog, err
			}
		case "custom":
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "custom tool name is required"}
			}
			field := "input"
			if name == "apply_patch" {
				field = "patch"
			}
			description := tool.Description
			if description == "" {
				description = "Provide the raw custom tool input."
			}
			custom := customTool{Name: name, ArgumentField: field}
			if err := declare(name, custom, namespaceTool{}, mustJSON(map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name, "description": description,
					"parameters": map[string]any{
						"type":                 "object",
						"properties":           map[string]any{field: map[string]string{"type": "string"}},
						"required":             []string{field},
						"additionalProperties": false,
					},
				},
			})); err != nil {
				return catalog, err
			}
		case "namespace":
			if tool.Name == "" || len(tool.Tools) == 0 {
				return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "namespace tool requires name and child tools"}
			}
			for _, childRaw := range tool.Tools {
				var child struct {
					Type        string          `json:"type"`
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
					Strict      *bool           `json:"strict"`
				}
				if err := json.Unmarshal(childRaw, &child); err != nil {
					return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "invalid namespace child tool: " + err.Error()}
				}
				if child.Type != "function" {
					return catalog, unsupported(fmt.Sprintf("namespace child tool type %q is not translatable to Chat Completions", child.Type))
				}
				if strings.TrimSpace(child.Name) == "" {
					return catalog, &Error{Code: ErrorCodeInvalidRequest, Message: "namespace child tool name is required"}
				}
				childName := strings.TrimSpace(child.Name)
				toolName := strings.TrimSpace(tool.Name)
				name := toolName + "-" + childName
				fn := map[string]any{"name": name}
				if child.Description != "" {
					fn["description"] = child.Description
				}
				if child.Parameters != nil {
					fn["parameters"] = child.Parameters
				}
				if child.Strict != nil {
					fn["strict"] = *child.Strict
				}
				if err := declare(name, customTool{}, namespaceTool{Namespace: toolName, Name: childName}, mustJSON(map[string]any{
					"type": "function", "function": fn,
				})); err != nil {
					return catalog, err
				}
			}
		default:
			if hosted[tool.Type] && cap.Protocol == ProtocolResponses {
				continue
			}
			return catalog, unsupported(fmt.Sprintf("tool type %q is not supported by this provider", tool.Type))
		}
	}
	return catalog, nil
}

func functionStringField(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	var value struct {
		Properties map[string]struct{ Type string } `json:"properties"`
	}
	if err := json.Unmarshal(schema, &value); err != nil || len(value.Properties) != 1 {
		return ""
	}
	for field, property := range value.Properties {
		if property.Type == "string" {
			return field
		}
	}
	return ""
}

func validateRequestCapabilities(req responsesWireRequest, items []responsesItem, cap Capabilities) error {
	if len(req.Tools) > 0 && !cap.Tools {
		return unsupported("tools are not supported by this provider")
	}
	if req.Reasoning != nil && !cap.Reasoning {
		return unsupported("reasoning is not supported by this provider")
	}
	if req.ParallelToolCalls != nil && *req.ParallelToolCalls && !cap.ParallelToolCalls {
		return unsupported("parallel tool calls are not supported by this provider")
	}
	if containsImageInput(items) && !cap.ImageInput {
		return unsupported("image input is not supported by this provider")
	}
	if cap.Protocol == ProtocolResponses {
		for _, raw := range req.Tools {
			var tool struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &tool) != nil || tool.Type == "" {
				return &Error{Code: ErrorCodeInvalidRequest, Message: "invalid Responses tool"}
			}
			if tool.Type == "function" || tool.Type == "custom" || tool.Type == "namespace" {
				continue
			}
			allowed := false
			for _, kind := range cap.AllowedHostedTools {
				allowed = allowed || kind == tool.Type
			}
			if !allowed {
				return unsupported("hosted tool is not configured for this provider")
			}
		}
		return nil
	}
	for _, item := range items {
		switch item.Type {
		case "message", "agent_message", "function_call", "custom_tool_call",
			"function_call_output", "custom_tool_call_output", "reasoning":
		default:
			return unsupported(fmt.Sprintf("Responses input item type %q is not supported", item.Type))
		}
	}
	return nil
}

func containsImageInput(items []responsesItem) bool {
	for _, item := range items {
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(item.Content, &parts); err != nil || parts == nil {
			continue
		}
		for _, part := range parts {
			if string(part["type"]) == `"input_image"` || string(part["type"]) == `"image_url"` {
				return true
			}
		}
	}
	return false
}

func unsupported(message string) error {
	return &Error{Code: ErrorCodeUnsupportedCapability, Message: message}
}
