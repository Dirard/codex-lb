package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func realtimeUsage(t *testing.T, raw string) RealtimeUsage {
	t.Helper()
	var usage RealtimeUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestRealtimeUsagePricesTextAudioAndCacheApart(t *testing.T) {
	// The shape gpt-realtime reported on 2026-11-03, with audio and cache added.
	usage := realtimeUsage(t, `{"input_tokens":1700,"output_tokens":205,
"input_token_details":{"text_tokens":700,"audio_tokens":1000,"image_tokens":0,"cached_tokens":500,"cached_tokens_details":{"text_tokens":300,"audio_tokens":200,"image_tokens":0}},
"output_token_details":{"text_tokens":5,"audio_tokens":200}}`)
	amount, err := usage.Amount("gpt-realtime")
	if err != nil {
		t.Fatal(err)
	}
	// 400 text×4 + 300 cached text×0.4 + 800 audio×32 + 200 cached audio×0.4 + 5×16 + 200×64 microdollars per token / 1e6.
	want := ceilMicros(400*4_000_000 + 300*400_000 + 800*32_000_000 + 200*400_000 + 5*16_000_000 + 200*64_000_000)
	if amount.CostMicrodollars != want || amount.InputTokens != 1700 || amount.OutputTokens != 205 || amount.CachedInputTokens != 500 {
		t.Fatalf("amount: %+v, want cost %d", amount, want)
	}
	mini, err := usage.Amount("gpt-realtime-mini-2025-12-15")
	if err != nil || mini.CostMicrodollars >= amount.CostMicrodollars {
		t.Fatalf("mini amount: %+v %v", mini, err)
	}
	text := realtimeUsage(t, `{"input_tokens":17,"output_tokens":5,"input_token_details":{"text_tokens":17},"output_token_details":{"text_tokens":5}}`)
	if amount, err := text.Amount("gpt-realtime"); err != nil || amount.CostMicrodollars != ceilMicros(17*4_000_000+5*16_000_000) {
		t.Fatalf("text-only amount: %+v %v", amount, err)
	}
	if budget := RealtimeSessionBudget("gpt-realtime"); budget.CostMicrodollars != 1_280_000 || budget.InputTokens != 20_000 {
		t.Fatalf("budget: %+v", budget)
	}
}

func TestRealtimeUsageRefusesImpossibleCounts(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":-1}`,
		`{"input_tokens":10,"input_token_details":{"text_tokens":10,"cached_tokens":11}}`,
		`{"input_tokens":10,"input_token_details":{"audio_tokens":5,"cached_tokens_details":{"audio_tokens":6}}}`,
	} {
		if _, err := realtimeUsage(t, raw).Amount("gpt-realtime"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for model, valid := range map[string]bool{"gpt-realtime": true, "gpt-realtime-1.5": true, "": false, "gpt realtime": false, "a/b": false} {
		if ValidRealtimeModel(model) != valid {
			t.Fatalf("model %q", model)
		}
	}
}
