package provider

import (
	"encoding/json"
	"testing"
)

func TestOperationUsageRequiresConsistentReportedCounters(t *testing.T) {
	tests := []struct {
		name, raw string
		valid     bool
		input     int64
		output    int64
		cached    int64
		reasoning int64
	}{
		{name: "missing usage", raw: `{}`, valid: true},
		{name: "explicit zero", raw: `{"usage":{"prompt_tokens":0,"input_tokens":0,"completion_tokens":0,"output_tokens":0,"total_tokens":0}}`, valid: true},
		{name: "total is optional", raw: `{"usage":{"input_tokens":0,"output_tokens":0}}`, valid: true},
		{name: "consistent aliases and details", raw: `{"usage":{"prompt_tokens":5,"input_tokens":5,"completion_tokens":2,"output_tokens":2,"total_tokens":7,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":3,"prompt_tokens_details":{"cached_tokens":2},"input_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`, valid: true, input: 5, output: 2, cached: 2, reasoning: 1},
		{name: "cache misses derive hits", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":2,"prompt_cache_miss_tokens":3}}`, valid: true, input: 5, output: 2, cached: 2},
		{name: "embedding input only", raw: `{"usage":{"prompt_tokens":2,"total_tokens":2}}`, valid: true, input: 2},
		{name: "audio duration only", raw: `{"usage":{"seconds":3}}`, valid: true},
		{name: "conflicting input alias", raw: `{"usage":{"prompt_tokens":0,"input_tokens":5,"completion_tokens":1}}`},
		{name: "conflicting output alias", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":0,"output_tokens":2}}`},
		{name: "missing input", raw: `{"usage":{"completion_tokens":1,"total_tokens":1}}`},
		{name: "missing output and total", raw: `{"usage":{"prompt_tokens":2}}`},
		{name: "mismatched total", raw: `{"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":2}}`},
		{name: "conflicting cached details", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0},"input_tokens_details":{"cached_tokens":2}}}`},
		{name: "inconsistent hit and miss", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":1,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":1}}`},
		{name: "cache exceeds input", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":1,"prompt_cache_hit_tokens":6}}`},
		{name: "conflicting reasoning details", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":0},"output_tokens_details":{"reasoning_tokens":1}}}`},
		{name: "reasoning exceeds output", raw: `{"usage":{"prompt_tokens":5,"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":2}}}`},
		{name: "null alias", raw: `{"usage":{"prompt_tokens":null,"input_tokens":5,"output_tokens":1}}`},
		{name: "negative counter", raw: `{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`},
		{name: "overflow counter", raw: `{"usage":{"prompt_tokens":18446744073709551615,"completion_tokens":1}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var object map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.raw), &object); err != nil {
				t.Fatal(err)
			}
			usage, valid := operationUsage(object)
			if valid != test.valid || valid && (usage.InputTokens != test.input || usage.OutputTokens != test.output ||
				usage.CachedInputTokens != test.cached || usage.ReasoningTokens != test.reasoning) {
				t.Fatalf("usage=%+v valid=%v", usage, valid)
			}
		})
	}
}
