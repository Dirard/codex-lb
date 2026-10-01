package application

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
)

// trimCompactHistory selects budget-fitting history before account/file
// resolution. Unchanged context is preferred to compact-only image elision.
func trimCompactHistory(ctx context.Context, input []json.RawMessage, anchored bool) ([]json.RawMessage, error) {
	if compactInputSize(input) <= compactTokenBudget*4 {
		return input, nil
	}
	for pass := 0; pass < 2; pass++ {
		h, err := newCompactHistory(ctx, input)
		if err != nil {
			return nil, err
		}
		selected, required, err := h.selectHistory(anchored)
		if err != nil {
			return nil, err
		}
		if selected != nil {
			result := h.withMarkers(selected)
			if compactInputSize(result) <= compactTokenBudget*4 {
				return result, nil
			}
			return nil, compactTooLarge()
		}
		if pass == 1 {
			break
		}
		rewritten := append([]json.RawMessage(nil), input...)
		changed := false
		for i := range required {
			if len(h.items[i].elided) != 0 {
				rewritten[i], changed = h.items[i].elided, true
			}
		}
		if !changed {
			break
		}
		input = rewritten
		if compactInputSize(input) <= compactTokenBudget*4 {
			return input, nil
		}
	}
	return nil, compactTooLarge()
}

func compactTooLarge() error {
	return &ProxyError{Code: "responses_compact_input_too_large", Status: 400, Message: "Compact input exceeds the upstream size limit and cannot be trimmed without removing required state anchors", Param: "input"}
}

func compactTrimMarker(items, tokens int) json.RawMessage {
	text := fmt.Sprintf("[compact trim] Omitted %d input items (~%d estimated tokens) before forwarding this oversized compact request upstream. Required compact state anchors and retained input items remain in their original order; omitted items may include terminal context.", items, tokens)
	encoded, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}})
	return encoded
}

func (h *compactHistory) withMarkers(selected map[int]bool) []json.RawMessage {
	if len(selected) == len(h.input) {
		return h.input
	}
	var result []json.RawMessage
	items, tokens := 0, 0
	for i, raw := range h.input {
		if !selected[i] {
			items++
			tokens += h.tokens[i]
			continue
		}
		if items > 0 {
			result = append(result, compactTrimMarker(items, tokens))
			items, tokens = 0, 0
		}
		result = append(result, raw)
	}
	if items > 0 {
		result = append(result, compactTrimMarker(items, tokens))
	}
	return result
}

// selectedSize includes all omission markers and array framing without
// repeatedly serializing retained megabytes during exact-budget backtracking.
func (h *compactHistory) selectedSize(selected map[int]bool) int {
	size, items, tokens := 0, 0, 0
	markerSize := func() int { return h.markerChars + len(strconv.Itoa(items)) + len(strconv.Itoa(tokens)) }
	for i := range h.input {
		if !selected[i] {
			items++
			tokens += h.tokens[i]
			continue
		}
		if items > 0 {
			size += markerSize()
			items, tokens = 0, 0
		}
		size += h.chars[i] + 2
	}
	if items > 0 {
		size += markerSize()
	}
	return max(2, size)
}

func (h *compactHistory) terminalRequired(anchored bool) (map[int]bool, bool, bool) {
	indices := map[int]bool{}
	latest := len(h.input) - 1
	if latest < 0 {
		return indices, false, false
	}
	trigger := h.items[latest].kind == "compaction_trigger"
	if trigger {
		indices[latest] = true
		latest--
		if latest < 0 {
			return indices, true, false
		}
	}
	item := h.items[latest]
	kind, output := compactCallKind(item.kind)
	if kind == "" {
		indices[latest] = true
		return indices, true, false
	}
	paired := func() map[int]bool {
		set := map[int]bool{latest: true}
		set = h.reconcile(set, set, h.totalTokens, true)
		maps.Copy(set, indices)
		return set
	}
	if item.state || len(item.elided) != 0 {
		return paired(), true, true
	}
	call, hasCall := h.callForOutput[latest]
	sideEffect := item.sideEffect || item.kind == "apply_patch_call_output" || hasCall && h.items[call].sideEffect
	if sideEffect {
		return paired(), true, true
	}
	if output && anchored && !hasCall {
		indices[latest] = true
		return indices, true, false
	}
	if !output {
		return paired(), true, true
	}
	if trigger {
		return indices, true, false
	}
	pair := h.reconcile(map[int]bool{latest: true}, nil, compactTokenBudget, true)
	if pair[latest] {
		return pair, false, false
	}
	return indices, false, false
}

func (h *compactHistory) required(anchored bool, state map[int]bool) map[int]bool {
	required := h.reconcile(state, state, h.totalTokens, true)
	terminal, mandatory, reconcile := h.terminalRequired(anchored)
	if len(terminal) == 0 {
		return required
	}
	prospective := maps.Clone(required)
	maps.Copy(prospective, terminal)
	if reconcile {
		prospective = h.reconcile(prospective, prospective, h.totalTokens, true)
	}
	if mandatory || h.selectedSize(prospective) <= compactTokenBudget*4 {
		return prospective
	}
	return required
}

func (h *compactHistory) selectHistory(anchored bool) (map[int]bool, map[int]bool, error) {
	state, priority, unusable := map[int]bool{}, map[int]bool{}, map[int]bool{}
	for i, item := range h.items {
		if item.state {
			state[i] = true
		}
		if item.sideEffect {
			if item.callID == "" {
				unusable[i] = true
			} else {
				priority[i] = true
			}
		}
	}
	priority = h.reconcile(priority, nil, h.totalTokens, true)
	required := h.required(anchored, state)
	for i := range unusable {
		if required[i] {
			return nil, required, compactTooLarge()
		}
	}
	if h.selectedSize(required) > compactTokenBudget*4 {
		return nil, required, nil
	}
	markerTokens := max(1, (h.markerChars+2+3)/4)
	wireBudget := compactTokenBudget - markerTokens
	limitedPriority := maps.Clone(required)
	maps.Copy(limitedPriority, priority)
	limitedPriority = h.reconcile(limitedPriority, required, wireBudget, true)
	for i := range priority {
		if !limitedPriority[i] {
			delete(priority, i)
		}
	}
	selected := maps.Clone(state)
	maps.Copy(selected, priority)
	headCount, headTokens := 0, 0
	for _, tokens := range h.tokens {
		if headTokens+tokens > compactHeadBudget {
			break
		}
		selected[headCount] = true
		headCount++
		headTokens += tokens
	}
	tailBudget := max(0, compactTokenBudget-h.tokenCount(selected)-markerTokens)
	tailTokens, tailCount := 0, 0
	for i := len(h.input) - 1; i >= headCount; i-- {
		if selected[i] {
			continue
		}
		if tailCount > 0 && tailTokens+h.tokens[i] > tailBudget {
			break
		}
		selected[i] = true
		tailCount++
		tailTokens += h.tokens[i]
		if tailTokens > tailBudget {
			break
		}
	}
	maps.Copy(selected, required)
	if anchored {
		h.discardConsumedTailPairs(selected, required)
	}
	for i := range unusable {
		delete(selected, i)
	}
	protected := maps.Clone(required)
	maps.Copy(protected, priority)
	selected = h.reconcile(selected, protected, wireBudget, true)
	var optional []int
	for i := range selected {
		if !required[i] {
			optional = append(optional, i)
		}
	}
	last := len(h.input) - 1
	sort.Slice(optional, func(i, j int) bool {
		a, b := optional[i], optional[j]
		if priority[a] != priority[b] {
			return !priority[a]
		}
		if priority[a] {
			return a < b
		}
		aDistance, bDistance := min(a, last-a), min(b, last-b)
		if aDistance != bDistance {
			return aDistance > bDistance
		}
		return a > b
	})
	for _, i := range optional {
		if err := h.ctx.Err(); err != nil {
			return nil, required, err
		}
		if h.selectedSize(selected) <= compactTokenBudget*4 {
			return selected, required, nil
		}
		if !selected[i] {
			continue
		}
		trial := maps.Clone(selected)
		delete(trial, i)
		selected = h.reconcile(trial, required, wireBudget, false)
	}
	if h.selectedSize(selected) > compactTokenBudget*4 {
		return nil, required, nil
	}
	return selected, required, nil
}

func (h *compactHistory) discardConsumedTailPairs(selected, required map[int]bool) {
	latest := len(h.input) - 1
	if latest >= 0 && h.items[latest].kind == "compaction_trigger" {
		latest--
	}
	if latest < 0 {
		return
	}
	item := h.items[latest]
	kind, output := compactCallKind(item.kind)
	if _, matched := h.callForOutput[latest]; !output || matched {
		return
	}
	for i, previous := range h.items[:latest] {
		if !required[i] && previous.callID == item.callID && (previous.kind == item.kind || previous.kind == kind) {
			delete(selected, i)
		}
	}
}
