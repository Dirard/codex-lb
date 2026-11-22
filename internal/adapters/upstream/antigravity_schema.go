package upstream

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	// antigravitySchemaMaxDepth bounds schema nesting; deeper nodes become
	// plain strings so a hostile schema cannot grow the translation.
	antigravitySchemaMaxDepth = 32
	// antigravityRefMaxDepth bounds nested $ref inlining; a deeper or cyclic
	// reference becomes a plain object.
	antigravityRefMaxDepth = 8
	// antigravityPlaceholderProperty fills empty Claude object schemas, which
	// the Claude backend rejects in VALIDATED mode.
	antigravityPlaceholderProperty = "_placeholder"
)

// antigravitySchemaScalars are the constraint keywords Cloud Code's
// OpenAPI-style Schema accepts; every other JSON Schema keyword is dropped,
// because the backend refuses unknown fields with 400.
var antigravitySchemaScalars = map[string]bool{
	"nullable": true, "minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true,
	"minLength": true, "maxLength": true, "pattern": true, "minimum": true, "maximum": true,
}

var antigravitySchemaTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true, "array": true, "object": true,
}

var antigravitySchemaFormats = map[string]map[string]bool{
	"string":  {"enum": true, "date-time": true},
	"integer": {"int32": true, "int64": true},
	"number":  {"float": true, "double": true},
}

type antigravitySchemaCleaner struct {
	defs   map[string]any
	claude bool
}

// cleanAntigravitySchema turns a JSON Schema tool parameter object into the
// schema subset Cloud Code accepts: local $ref inlined, allOf merged, const
// turned into a single-value enum, nullable type unions collapsed, arrays
// given items, and unsupported keywords dropped. For Claude models anyOf and
// oneOf collapse to their first non-null branch and empty objects get a
// placeholder property. It returns nil when the function takes no
// parameters on a Gemini model. Replayed functionCall.args never pass here.
func cleanAntigravitySchema(raw json.RawMessage, claude bool) (json.RawMessage, error) {
	var root any
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "tool parameters are not valid JSON"}
		}
	}
	cleaner := antigravitySchemaCleaner{defs: antigravitySchemaDefs(root), claude: claude}
	object, _ := root.(map[string]any)
	if object == nil {
		object = map[string]any{}
	}
	if _, typed := object["type"]; !typed {
		object = shallowCopy(object)
		object["type"] = "object"
	}
	cleaned := cleaner.clean(object, 0, nil)
	if cleaned["type"] != "object" {
		cleaned = cleaner.finish(map[string]any{"type": "object"})
	}
	if properties, _ := cleaned["properties"].(map[string]any); len(properties) == 0 && !claude {
		return nil, nil
	}
	return encodeAntigravityJSON(cleaned)
}

func antigravitySchemaDefs(root any) map[string]any {
	defs := make(map[string]any)
	object, _ := root.(map[string]any)
	for _, section := range []string{"$defs", "definitions"} {
		entries, _ := object[section].(map[string]any)
		for name, value := range entries {
			defs["#/"+section+"/"+name] = value
		}
	}
	return defs
}

func (c antigravitySchemaCleaner) clean(node any, depth int, refs []string) map[string]any {
	object, ok := node.(map[string]any)
	if !ok || depth > antigravitySchemaMaxDepth {
		return c.finish(map[string]any{})
	}
	if ref, ok := object["$ref"].(string); ok {
		ref = strings.NewReplacer("~1", "/", "~0", "~").Replace(ref)
		target, found := c.defs[ref]
		cyclic := false
		for _, seen := range refs {
			cyclic = cyclic || seen == ref
		}
		var resolved map[string]any
		if !found || cyclic || len(refs) >= antigravityRefMaxDepth {
			resolved = c.finish(map[string]any{"type": "object"})
		} else {
			resolved = c.clean(target, depth+1, append(refs[:len(refs):len(refs)], ref))
		}
		if description, ok := object["description"].(string); ok && description != "" {
			resolved["description"] = description
		}
		return resolved
	}

	merged := map[string]any{}
	if branches, ok := object["allOf"].([]any); ok {
		for _, branch := range branches {
			mergeAntigravitySchema(merged, c.clean(branch, depth+1, refs), false)
		}
	}
	own := map[string]any{}
	for key, value := range object {
		switch key {
		case "type":
			if kind := antigravitySchemaType(value); kind != "" {
				own["type"] = kind
			}
		case "description":
			if text, ok := value.(string); ok && text != "" {
				own["description"] = text
			}
		case "format":
			if text, ok := value.(string); ok {
				own["format"] = text
			}
		case "properties":
			entries, _ := value.(map[string]any)
			properties := make(map[string]any, len(entries))
			for name, property := range entries {
				if property == false {
					continue
				}
				properties[name] = c.clean(property, depth+1, refs)
			}
			own["properties"] = properties
		case "items":
			if list, ok := value.([]any); ok {
				if len(list) == 0 {
					continue
				}
				value = list[0]
			}
			own["items"] = c.clean(value, depth+1, refs)
		case "required":
			own["required"] = antigravityStrings(value)
		case "enum":
			if values := antigravityEnum(value); len(values) != 0 {
				own["enum"] = values
			}
		case "const":
			if values := antigravityEnum([]any{value}); len(values) != 0 {
				own["enum"] = values
			}
		case "anyOf", "oneOf":
			list, _ := value.([]any)
			branches, _ := own["anyOf"].([]any)
			for _, branch := range list {
				if antigravityNullSchema(branch) {
					continue
				}
				branches = append(branches, c.clean(branch, depth+1, refs))
			}
			own["anyOf"] = branches
		default:
			if antigravitySchemaScalars[key] {
				switch value.(type) {
				case json.Number, string, bool:
					own[key] = value
				}
			}
		}
	}
	mergeAntigravitySchema(merged, own, true)
	return c.finish(merged)
}

// finish fixes one cleaned node: unions, implied types, array items, object
// properties and formats the backend accepts for the type.
func (c antigravitySchemaCleaner) finish(node map[string]any) map[string]any {
	if branches, ok := node["anyOf"].([]any); ok {
		delete(node, "anyOf")
		switch {
		case len(branches) == 0:
		case c.claude || len(branches) == 1:
			first, _ := branches[0].(map[string]any)
			mergeAntigravitySchema(node, first, false)
		default:
			node["anyOf"] = branches
		}
	}
	kind, _ := node["type"].(string)
	if kind == "" && node["anyOf"] == nil {
		switch {
		case node["properties"] != nil:
			kind = "object"
		case node["items"] != nil:
			kind = "array"
		default:
			kind = "string"
		}
		node["type"] = kind
	}
	if format, ok := node["format"].(string); ok && !antigravitySchemaFormats[kind][format] {
		delete(node, "format")
	}
	if kind != "array" {
		delete(node, "items")
	} else if node["items"] == nil {
		node["items"] = map[string]any{"type": "string"}
	}
	if kind != "object" {
		delete(node, "properties")
		delete(node, "required")
		return node
	}
	properties, _ := node["properties"].(map[string]any)
	if len(properties) == 0 {
		delete(node, "properties")
		delete(node, "required")
		if c.claude {
			node["properties"] = map[string]any{antigravityPlaceholderProperty: map[string]any{"type": "boolean"}}
		}
		return node
	}
	required := make([]any, 0)
	seen := make(map[string]bool)
	for _, name := range antigravityStrings(node["required"]) {
		if _, ok := properties[name.(string)]; ok && !seen[name.(string)] {
			seen[name.(string)] = true
			required = append(required, name)
		}
	}
	if len(required) == 0 {
		delete(node, "required")
	} else {
		node["required"] = required
	}
	return node
}

// mergeAntigravitySchema merges src into dst: properties and required are
// combined, other keywords are taken from src only when override is set or
// dst lacks them.
func mergeAntigravitySchema(dst, src map[string]any, override bool) {
	for key, value := range src {
		switch key {
		case "properties":
			properties, _ := dst["properties"].(map[string]any)
			if properties == nil {
				properties = make(map[string]any)
			}
			for name, property := range value.(map[string]any) {
				if _, exists := properties[name]; override || !exists {
					properties[name] = property
				}
			}
			dst["properties"] = properties
		case "required":
			existing, _ := dst["required"].([]any)
			dst["required"] = append(existing, value.([]any)...)
		default:
			if _, exists := dst[key]; override || !exists {
				dst[key] = value
			}
		}
	}
}

func antigravitySchemaType(value any) string {
	switch typed := value.(type) {
	case string:
		kind := strings.ToLower(typed)
		if antigravitySchemaTypes[kind] {
			return kind
		}
	case []any:
		for _, item := range typed {
			if kind := antigravitySchemaType(item); kind != "" {
				return kind
			}
		}
	}
	return ""
}

func antigravityNullSchema(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	kind, _ := object["type"].(string)
	return strings.EqualFold(kind, "null")
}

func antigravityStrings(value any) []any {
	list, _ := value.([]any)
	out := make([]any, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// antigravityEnum renders enum members as strings, as the backend's Schema
// enum requires; null and composite members are dropped.
func antigravityEnum(value any) []any {
	list, _ := value.([]any)
	out := make([]any, 0, len(list))
	for _, item := range list {
		switch typed := item.(type) {
		case string:
			out = append(out, typed)
		case json.Number:
			out = append(out, typed.String())
		case bool:
			if typed {
				out = append(out, "true")
			} else {
				out = append(out, "false")
			}
		}
	}
	return out
}

func shallowCopy(object map[string]any) map[string]any {
	out := make(map[string]any, len(object)+1)
	for key, value := range object {
		out[key] = value
	}
	return out
}

// encodeAntigravityJSON encodes without HTML escaping, so prompt and schema
// text reach Google byte-for-byte.
func encodeAntigravityJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "could not encode the Antigravity request"}
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
