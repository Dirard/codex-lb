package provider

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"codex-lb/internal/domain"
)

type operationCounter struct {
	value   uint64
	present bool
}

func readOperationCounter(fields map[string]json.RawMessage, name string) (operationCounter, bool) {
	raw, present := fields[name]
	if !present {
		return operationCounter{}, true
	}
	var value uint64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return operationCounter{}, false
	}
	return operationCounter{value: value, present: true}, true
}

func readOperationDetail(fields map[string]json.RawMessage, name, counter string) (operationCounter, bool) {
	raw, present := fields[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return operationCounter{}, true
	}
	var details map[string]json.RawMessage
	if json.Unmarshal(raw, &details) != nil || details == nil {
		return operationCounter{}, false
	}
	return readOperationCounter(details, counter)
}

func agreeOperationCounters(counters ...operationCounter) (operationCounter, bool) {
	var chosen operationCounter
	for _, counter := range counters {
		if !counter.present {
			continue
		}
		if chosen.present && chosen.value != counter.value {
			return operationCounter{}, false
		}
		chosen = counter
	}
	return chosen, true
}

// operationUsage accepts either Chat or Responses names, but never replaces
// a reported zero with another alias or treats a partial report as free usage.
func operationUsage(object map[string]json.RawMessage) (domain.UsageAmount, bool) {
	raw := bytes.TrimSpace(object["usage"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return domain.UsageAmount{}, true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return domain.UsageAmount{}, false
	}
	prompt, ok := readOperationCounter(fields, "prompt_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	inputAlias, ok := readOperationCounter(fields, "input_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	input, ok := agreeOperationCounters(prompt, inputAlias)
	if !ok {
		return domain.UsageAmount{}, false
	}
	if !input.present && (len(fields["seconds"]) != 0 || len(fields["duration"]) != 0) {
		// Audio usage is validated separately by transcriptionResult.
		return domain.UsageAmount{}, true
	}
	if !input.present || input.value > math.MaxInt64 {
		return domain.UsageAmount{}, false
	}
	completion, ok := readOperationCounter(fields, "completion_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	outputAlias, ok := readOperationCounter(fields, "output_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	output, ok := agreeOperationCounters(completion, outputAlias)
	if !ok || output.value > math.MaxInt64 {
		return domain.UsageAmount{}, false
	}
	total, ok := readOperationCounter(fields, "total_tokens")
	if !ok || !output.present && (!total.present || total.value != input.value) {
		return domain.UsageAmount{}, false
	}
	if total.present && total.value != input.value+output.value {
		return domain.UsageAmount{}, false
	}
	hit, ok := readOperationCounter(fields, "prompt_cache_hit_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	miss, ok := readOperationCounter(fields, "prompt_cache_miss_tokens")
	if !ok || miss.present && miss.value > input.value {
		return domain.UsageAmount{}, false
	}
	derivedHit := operationCounter{}
	if miss.present {
		derivedHit = operationCounter{value: input.value - miss.value, present: true}
	}
	promptCache, ok := readOperationDetail(fields, "prompt_tokens_details", "cached_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	inputCache, ok := readOperationDetail(fields, "input_tokens_details", "cached_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	cached, ok := agreeOperationCounters(hit, derivedHit, promptCache, inputCache)
	if !ok || cached.value > input.value {
		return domain.UsageAmount{}, false
	}
	completionReasoning, ok := readOperationDetail(fields, "completion_tokens_details", "reasoning_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	outputReasoning, ok := readOperationDetail(fields, "output_tokens_details", "reasoning_tokens")
	if !ok {
		return domain.UsageAmount{}, false
	}
	reasoning, ok := agreeOperationCounters(completionReasoning, outputReasoning)
	if !ok || reasoning.value > output.value {
		return domain.UsageAmount{}, false
	}
	amount := domain.UsageAmount{
		InputTokens: int64(input.value), OutputTokens: int64(output.value),
		CachedInputTokens: int64(cached.value), ReasoningTokens: int64(reasoning.value),
	}
	return amount, amount.Validate() == nil
}

func operationServiceTier(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "auto", "default", "standard", "priority", "fast", "flex":
		return value, true
	default:
		return "", false
	}
}
