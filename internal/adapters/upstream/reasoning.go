package upstream

import (
	"encoding/json"
	"strings"
)

// WireReasoningEffort aliases the Codex client-plane mode, not key policy.
func WireReasoningEffort(effort string) string {
	if strings.EqualFold(strings.TrimSpace(effort), "ultra") {
		return "max"
	}
	return effort
}

// NormalizeWireReasoning changes only effort fields in an outgoing JSON object.
// Callers own this object; request policy, reports and configuration stay intact.
func NormalizeWireReasoning(object map[string]json.RawMessage) bool {
	changed := false
	rewrite := func(object map[string]json.RawMessage, field string) bool {
		var effort string
		if json.Unmarshal(object[field], &effort) != nil || WireReasoningEffort(effort) == effort {
			return false
		}
		object[field] = json.RawMessage(`"max"`)
		return true
	}
	for _, field := range []string{"reasoning_effort", "reasoningEffort", "thinking"} {
		changed = rewrite(object, field) || changed
	}
	for _, field := range []string{"reasoning", "thinking"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(object[field], &nested) == nil && rewrite(nested, "effort") {
			object[field] = mustJSON(nested)
			changed = true
		}
	}
	return changed
}
