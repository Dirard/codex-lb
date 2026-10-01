package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBillingCountsDeriveOnlyRedundantTotalAndRejectContradictions(t *testing.T) {
	for _, test := range []struct {
		body  string
		known bool
	}{
		{`{"input_tokens":2,"output_tokens":1}`, true},
		{`{"input_tokens":0,"output_tokens":0}`, true},
		{`{"input_tokens":2,"total_tokens":3}`, false},
		{`{"input_tokens":2,"output_tokens":1,"total_tokens":4}`, false},
		{`{"input_tokens":18446744073709551615,"output_tokens":1}`, false},
		{`{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"cached_tokens":3}}`, false},
		{`{"input_tokens":2,"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`, false},
	} {
		var response responsesWireUsage
		var chat chatWireUsage
		if err := json.Unmarshal([]byte(test.body), &response); err != nil {
			t.Fatal(err)
		}
		chatBody := strings.ReplaceAll(strings.ReplaceAll(test.body, "input_tokens", "prompt_tokens"), "output_tokens", "completion_tokens")
		if err := json.Unmarshal([]byte(chatBody), &chat); err != nil {
			t.Fatal(err)
		}
		if response.known() != test.known || chat.known() != test.known {
			t.Fatalf("wrong certainty for %s", test.body)
		}
		if test.known {
			a, b := response.responseUsage(), chat.responseUsage()
			if a.TotalTokens != a.InputTokens+a.OutputTokens || b.TotalTokens != a.TotalTokens {
				t.Fatal("derived total differs from billable counters")
			}
		}
	}
	for _, test := range []struct {
		detail string
		known  bool
		cached uint64
	}{
		{`"prompt_cache_miss_tokens":3`, true, 2},
		{`"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":3`, true, 2},
		{`"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":4`, false, 0},
		{`"prompt_cache_miss_tokens":6`, false, 0},
		{`"prompt_cache_hit_tokens":2,"prompt_tokens_details":{"cached_tokens":3}`, false, 0},
	} {
		var chat chatWireUsage
		if err := json.Unmarshal([]byte(`{"prompt_tokens":5,"completion_tokens":1,`+test.detail+`}`), &chat); err != nil {
			t.Fatal(err)
		}
		if chat.known() != test.known || test.known && chat.responseUsage().CachedTokens != test.cached {
			t.Fatalf("inconsistent cache details accepted: %s", test.detail)
		}
	}
}

func TestTranslatedUsageUsesResponsesTokenDetails(t *testing.T) {
	for _, known := range []bool{true, false} {
		usage := Usage{InputTokens: 10, OutputTokens: 5, CachedTokens: 4, ReasoningTokens: 2, ReasoningTokensKnown: known}
		body, err := json.Marshal(usage)
		if err != nil {
			t.Fatal(err)
		}
		var wire responsesWireUsage
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if !wire.known() || *wire.InputTokens != 10 || *wire.TotalTokens != 15 || wire.InputTokensDetails == nil || wire.InputTokensDetails.CachedTokens != 4 {
			t.Fatal("translated cached usage shape is not Responses-compatible")
		}
		if known && (wire.OutputTokensDetails == nil || wire.OutputTokensDetails.ReasoningTokens != 2) {
			t.Fatal("reasoning details lost")
		}
		if !known && wire.OutputTokensDetails != nil {
			t.Fatal("unknown reasoning count reported as known")
		}
	}
}
