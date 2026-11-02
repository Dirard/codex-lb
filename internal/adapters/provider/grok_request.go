package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/adapters/wirejson"
	"codex-lb/internal/domain"
)

// grokMaxToolName is the longest function name sent to xAI, as at OpenAI.
const grokMaxToolName = 64

// grokNamespacedTool is the namespace and child a flat function name stands for.
type grokNamespacedTool struct {
	Namespace string
	Name      string
}

// grokToolNames maps the flat function names sent to xAI back to the
// namespaced tools Codex declared.
type grokToolNames map[string]grokNamespacedTool

// grokEffortOrder ranks every effort Codex may send, lowest first.
var grokEffortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// grokWebSearchFields are the web_search tool fields xAI accepts.
var grokWebSearchFields = []string{"type", "filters", "enable_image_understanding", "enable_image_search"}

// grokFlatName names a namespace child as one function; a name over xAI's
// limit keeps its start and gains a hash of the whole name.
func grokFlatName(namespace, name string) string {
	flat := namespace + "-" + name
	if len(flat) <= grokMaxToolName {
		return flat
	}
	sum := sha256.Sum256([]byte(flat))
	return flat[:grokMaxToolName-9] + "-" + hex.EncodeToString(sum[:4])
}

// adaptGrokRequest turns a Codex Responses request into what xAI accepts:
// it flattens namespace tools and their history calls, keeps only xAI's web
// search fields and fits the reasoning effort to the model. Everything else
// is forwarded unchanged. The returned names restore the namespaced calls.
func adaptGrokRequest(body json.RawMessage, model string) (json.RawMessage, grokToolNames, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, nil, providerFailure("invalid_response_request", 0, false)
	}
	names := grokToolNames{}
	if raw, ok := object["tools"]; ok && string(raw) != "null" {
		tools, err := adaptGrokTools(raw, names)
		if err != nil {
			return nil, nil, err
		}
		object["tools"] = tools
	}
	if raw, ok := object["input"]; ok {
		input, err := flattenGrokHistory(raw)
		if err != nil {
			return nil, nil, err
		}
		object["input"] = input
	}
	if raw, ok := object["reasoning"]; ok {
		levels := domain.GrokReasoningEfforts(model)
		var reasoning map[string]json.RawMessage
		switch {
		case levels == nil:
			delete(object, "reasoning")
		case json.Unmarshal(raw, &reasoning) == nil && reasoning != nil:
			var effort string
			if json.Unmarshal(reasoning["effort"], &effort) == nil && effort != "" {
				reasoning["effort"] = providerMustJSON(fitGrokEffort(effort, levels))
				object["reasoning"] = wirejson.EncodeObject(reasoning)
			}
		}
	}
	return wirejson.EncodeObject(object), names, nil
}

func adaptGrokTools(raw json.RawMessage, names grokToolNames) (json.RawMessage, error) {
	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, providerFailure("invalid_response_request", 0, false)
	}
	declared := make(map[string]bool, len(tools))
	declare := func(name string) error {
		if name == "" || declared[name] {
			return providerFailure("invalid_response_request", 0, false)
		}
		declared[name] = true
		return nil
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		var head struct {
			Type  string            `json:"type"`
			Name  string            `json:"name"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(tool, &head); err != nil {
			return nil, providerFailure("invalid_response_request", 0, false)
		}
		switch head.Type {
		case "namespace":
			if head.Name == "" {
				return nil, providerFailure("invalid_response_request", 0, false)
			}
			for _, childRaw := range head.Tools {
				var child map[string]json.RawMessage
				var kind, name string
				if json.Unmarshal(childRaw, &child) != nil || child == nil ||
					json.Unmarshal(child["type"], &kind) != nil || json.Unmarshal(child["name"], &name) != nil || name == "" {
					return nil, providerFailure("invalid_response_request", 0, false)
				}
				if kind != "function" {
					return nil, providerFailure(upstream.ErrorCodeUnsupportedCapability, 0, false)
				}
				flat := grokFlatName(head.Name, name)
				if err := declare(flat); err != nil {
					return nil, err
				}
				names[flat] = grokNamespacedTool{Namespace: head.Name, Name: name}
				child["name"] = providerMustJSON(flat)
				out = append(out, wirejson.EncodeObject(child))
			}
		case "web_search":
			var fields map[string]json.RawMessage
			if json.Unmarshal(tool, &fields) != nil || fields == nil {
				return nil, providerFailure("invalid_response_request", 0, false)
			}
			// xAI searches only the live web, so a cached-only search is not offered.
			if string(fields["external_web_access"]) == "false" {
				continue
			}
			kept := make(map[string]json.RawMessage, len(grokWebSearchFields))
			for _, field := range grokWebSearchFields {
				if value, ok := fields[field]; ok {
					kept[field] = value
				}
			}
			out = append(out, wirejson.EncodeObject(kept))
		default:
			if head.Type == "function" {
				if err := declare(head.Name); err != nil {
					return nil, err
				}
			}
			out = append(out, tool)
		}
	}
	return encodeRawArray(out), nil
}

// flattenGrokHistory gives earlier calls to namespaced tools the flat names
// the tools are declared under.
func flattenGrokHistory(raw json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return raw, nil // A plain-text input has no calls.
	}
	changed := false
	for i, rawItem := range items {
		var item map[string]json.RawMessage
		var kind, namespace, name string
		if json.Unmarshal(rawItem, &item) != nil || item == nil || json.Unmarshal(item["type"], &kind) != nil || kind != "function_call" ||
			json.Unmarshal(item["namespace"], &namespace) != nil || namespace == "" {
			continue
		}
		if json.Unmarshal(item["name"], &name) != nil || name == "" {
			return nil, providerFailure("invalid_response_request", 0, false)
		}
		item["name"] = providerMustJSON(grokFlatName(namespace, name))
		delete(item, "namespace")
		items[i] = wirejson.EncodeObject(item)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return encodeRawArray(items), nil
}

// fitGrokEffort sends an effort the model takes as it is, a higher one as the
// model's highest and a lower one as its lowest.
func fitGrokEffort(effort string, levels []string) string {
	if slices.Contains(levels, effort) {
		return effort
	}
	rank := slices.Index(grokEffortOrder, effort)
	switch {
	case rank < 0:
		return effort // Unknown: xAI answers for itself.
	case rank < slices.Index(grokEffortOrder, levels[0]):
		return levels[0]
	default:
		return levels[len(levels)-1]
	}
}

// grokRestoredEvents are the answer events that can carry a function call.
var grokRestoredEvents = map[string]bool{
	"response.output_item.added": true, "response.output_item.done": true,
	"response.function_call_arguments.done": true,
	"response.completed":                    true, "response.incomplete": true, "response.failed": true,
}

// restoreEvent gives a function call xAI made under a flat name its child
// name and namespace back, so Codex finds the tool it declared.
func (names grokToolNames) restoreEvent(kind string, data json.RawMessage) json.RawMessage {
	if len(names) == 0 || !grokRestoredEvents[kind] {
		return data
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil || event == nil {
		return data
	}
	changed := false
	if item, ok := names.restoreItem(event["item"]); ok {
		event["item"], changed = item, true
	}
	if response, ok := names.restoreOutput(event["response"]); ok {
		event["response"], changed = response, true
	}
	var name string
	if json.Unmarshal(event["name"], &name) == nil {
		if tool, ok := names[name]; ok {
			event["name"], changed = providerMustJSON(tool.Name), true
		}
	}
	if !changed {
		return data
	}
	return wirejson.EncodeObject(event)
}

// restoreResponse applies restoreEvent's rule to a finished response body.
func (names grokToolNames) restoreResponse(response json.RawMessage) json.RawMessage {
	if len(names) == 0 {
		return response
	}
	if restored, ok := names.restoreOutput(response); ok {
		return restored
	}
	return response
}

func (names grokToolNames) restoreOutput(raw json.RawMessage) (json.RawMessage, bool) {
	var response map[string]json.RawMessage
	var output []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &response) != nil || response == nil || json.Unmarshal(response["output"], &output) != nil {
		return nil, false
	}
	changed := false
	for i, rawItem := range output {
		if item, ok := names.restoreItem(rawItem); ok {
			output[i], changed = item, true
		}
	}
	if !changed {
		return nil, false
	}
	response["output"] = encodeRawArray(output)
	return wirejson.EncodeObject(response), true
}

func (names grokToolNames) restoreItem(raw json.RawMessage) (json.RawMessage, bool) {
	var item map[string]json.RawMessage
	var kind, name string
	if len(raw) == 0 || json.Unmarshal(raw, &item) != nil || item == nil ||
		json.Unmarshal(item["type"], &kind) != nil || kind != "function_call" || json.Unmarshal(item["name"], &name) != nil {
		return nil, false
	}
	tool, ok := names[name]
	if !ok {
		return nil, false
	}
	item["name"], item["namespace"] = providerMustJSON(tool.Name), providerMustJSON(tool.Namespace)
	return wirejson.EncodeObject(item), true
}

func encodeRawArray(values []json.RawMessage) json.RawMessage {
	size := 2
	for _, value := range values {
		size += len(value) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, value := range values {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, value...)
	}
	return append(out, ']')
}
