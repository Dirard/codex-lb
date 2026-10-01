package application

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"sort"
	"strings"
)

const compactTokenBudget = 100_000
const compactHeadBudget = 12_000

type compactItem struct {
	kind, callID      string
	state, sideEffect bool
	elided            json.RawMessage
}

type compactPair struct {
	callID, kind string
}

type compactPairGroup struct {
	calls, outputs []int
}

// compactHistory indexes typed occurrences once; retained payloads stay raw.
type compactHistory struct {
	ctx                          context.Context
	input                        []json.RawMessage
	items                        []compactItem
	tokens, chars                []int
	groups                       map[compactPair]*compactPairGroup
	callOrder, outputOrder       []compactPair
	callForOutput, outputForCall map[int]int
	totalTokens                  int
	markerChars                  int
}

func newCompactHistory(ctx context.Context, input []json.RawMessage) (*compactHistory, error) {
	h := &compactHistory{ctx: ctx, input: input, groups: map[compactPair]*compactPairGroup{}, callForOutput: map[int]int{}, outputForCall: map[int]int{}}
	h.markerChars = compactInputSize([]json.RawMessage{compactTrimMarker(0, 0)}) - 2
	for index, raw := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		item := compactItem{kind: compactString(object["type"]), callID: compactString(object["call_id"])}
		role := compactString(object["role"])
		item.state = role == "system" || role == "developer" || item.kind == "additional_tools"
		if item.kind == "function_call" {
			var function struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(object["function"], &function)
			item.state = item.state || compactStateTool(compactString(object["name"])) || compactStateTool(function.Name)
		}
		for _, value := range compactContentTexts(object["content"]) {
			value = strings.TrimSpace(value)
			item.state = item.state || strings.HasPrefix(value, `<codex_internal_context source="goal">`) || strings.HasPrefix(value, "<collaboration_mode># Plan Mode")
		}
		item.sideEffect = compactSideEffect(item.kind, object)
		kind, output := compactCallKind(item.kind)
		if output {
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if decoder.Decode(&decoded) == nil {
				if rewritten, changed := elideCompactImages(decoded); changed {
					item.elided, _ = json.Marshal(rewritten)
				}
			}
		}
		h.items = append(h.items, item)
		size := compactInputSize([]json.RawMessage{raw})
		h.chars = append(h.chars, size-2)
		tokens := max(1, (size+3)/4)
		h.tokens = append(h.tokens, tokens)
		h.totalTokens += tokens
		if kind == "" || item.callID == "" {
			continue
		}
		key := compactPair{item.callID, kind}
		group := h.groups[key]
		if group == nil {
			group = &compactPairGroup{}
			h.groups[key] = group
		}
		if output {
			if len(group.outputs) == 0 {
				h.outputOrder = append(h.outputOrder, key)
			}
			group.outputs = append(group.outputs, index)
		} else {
			if len(group.calls) == 0 {
				h.callOrder = append(h.callOrder, key)
			}
			group.calls = append(group.calls, index)
		}
	}
	for _, group := range h.groups {
		var pending []int
		callCursor := 0
		for _, output := range group.outputs {
			for callCursor < len(group.calls) && group.calls[callCursor] < output {
				pending = append(pending, group.calls[callCursor])
				callCursor++
			}
			if len(pending) > 0 {
				h.callForOutput[output] = pending[len(pending)-1]
				pending = pending[:len(pending)-1]
			}
		}
		for i, call := range group.calls {
			output := sort.SearchInts(group.outputs, call+1)
			if output < len(group.outputs) && (i+1 == len(group.calls) || group.outputs[output] < group.calls[i+1]) {
				h.outputForCall[call] = group.outputs[output]
			}
		}
	}
	return h, nil
}

func compactString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func compactStateTool(name string) bool {
	switch name {
	case "create_goal", "get_goal", "update_goal", "update_plan":
		return true
	}
	return false
}

func compactContentTexts(content json.RawMessage) []string {
	if len(content) == 0 {
		return nil
	}
	if content[0] == '"' {
		return []string{compactString(content)}
	}
	parts := []json.RawMessage{content}
	if content[0] == '[' && json.Unmarshal(content, &parts) != nil {
		return nil
	}
	var texts []string
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if part[0] == '"' {
			texts = append(texts, compactString(part))
			continue
		}
		var object struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &object) == nil {
			texts = append(texts, object.Text)
		}
	}
	return texts
}

func compactCallKind(kind string) (string, bool) {
	switch kind {
	case "function_call", "custom_tool_call", "apply_patch_call":
		return kind, false
	case "function_call_output":
		return "function_call", true
	case "custom_tool_call_output":
		return "custom_tool_call", true
	case "apply_patch_call_output":
		return "apply_patch_call", true
	}
	return "", false
}

func compactDirectSideEffect(name string) bool {
	switch strings.TrimPrefix(name, "functions.") {
	case "apply_patch", "close_agent", "create_goal", "exec_command", "request_user_input", "resume_agent", "send_input", "spawn_agent", "update_goal", "update_plan", "wait_agent", "write_stdin", "collaboration", "exec":
		return true
	}
	return false
}

func compactSideEffect(kind string, object map[string]json.RawMessage) bool {
	if kind == "apply_patch_call" {
		return true
	}
	field := "arguments"
	if kind == "custom_tool_call" {
		field = "input"
	} else if kind != "function_call" {
		return false
	}
	var argument *string
	if json.Unmarshal(object[field], &argument) != nil || argument == nil {
		return false
	}
	name := compactString(object["name"])
	if name != "multi_tool_use.parallel" {
		return compactDirectSideEffect(name)
	}
	var batch struct {
		Tools []struct {
			Recipient string `json:"recipient_name"`
		} `json:"tool_uses"`
	}
	if json.Unmarshal([]byte(*argument), &batch) != nil {
		return false
	}
	for _, tool := range batch.Tools {
		if tool.Recipient == "multi_tool_use.parallel" || strings.HasPrefix(tool.Recipient, "functions.") && compactDirectSideEffect(tool.Recipient) {
			return true
		}
	}
	return false
}

func (h *compactHistory) tokenCount(indices map[int]bool) int {
	total := 0
	for index := range indices {
		total += h.tokens[index]
	}
	return total
}

// reconcile retains compatible occurrences together. Protected items may be
// unmatched because their peer belongs to a previous response, not this array.
func (h *compactHistory) reconcile(indices, protected map[int]bool, budget int, allowAdd bool) map[int]bool {
	selected := maps.Clone(indices)
	used := h.tokenCount(selected)
	remove := func(indices ...int) {
		for _, i := range indices {
			if selected[i] && !protected[i] {
				delete(selected, i)
				used -= h.tokens[i]
			}
		}
	}
	add := func(index int) bool {
		missing := 0
		if !selected[index] {
			missing = h.tokens[index]
		}
		if used+missing > budget {
			return false
		}
		selected[index] = true
		used += missing
		return true
	}
	for _, key := range h.outputOrder {
		for _, output := range h.groups[key].outputs {
			if !selected[output] {
				continue
			}
			call, exists := h.callForOutput[output]
			if !exists || !allowAdd && !selected[call] || !add(call) {
				remove(output)
			}
		}
	}
	for _, key := range h.callOrder {
		for _, call := range h.groups[key].calls {
			if !selected[call] {
				continue
			}
			output, exists := h.outputForCall[call]
			if !exists {
				remove(call)
				continue
			}
			if !allowAdd && !selected[output] || !add(output) {
				remove(call, output)
			}
		}
	}
	return selected
}
