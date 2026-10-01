package pricing

import (
	"math"
	"testing"

	"codex-lb/internal/domain"
)

func TestCodexPricesAndExternalIsolation(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "GPT-6-ASTRA", "gpt-6-astra-2026-09-01"} {
		_, price, err := LookupCodex(model)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []int64{272_000, 272_001, 300_000, 1_000_000} {
			usage := domain.UsageAmount{InputTokens: input, CachedInputTokens: 50_000, OutputTokens: 100_000}
			for tier, multiplier := range map[string]int64{"default": 1000, "fast": 2500, "priority": 2500, "flex": 500} {
				cost, err := price.Cost(usage, tier)
				want := ((input-50_000)*10 + 50_000 + 5_000_000) * multiplier / 1000
				if err != nil || cost != want {
					t.Fatalf("%s input=%d tier=%s cost=%d want=%d err=%v", model, input, tier, cost, want, err)
				}
			}
		}
	}
	for _, model := range []string{"gpt-6-astral", "gpt-6-solar", "gpt-6-lunar", "gpt-6", "unpriced-model"} {
		if _, _, err := LookupCodex(model); err == nil {
			t.Fatalf("accepted unknown model %s", model)
		}
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		_, price, _ := LookupCodex(model)
		normal, _ := price.Effective(272_000, "default")
		long, _ := price.Effective(272_001, " FAST ")
		if long.Input != normal.Input*5 || long.Cached != normal.Cached*5 || long.Output != normal.Output*15/4 {
			t.Fatal("context/tier uplift not preserved")
		}
	}
	external := Price{Standard: Rates{7_000_000, 300_000, 11_000_000}, Long: &Rates{14_000_000, 600_000, 16_500_000}, Threshold: 272_000}
	cost, err := external.Cost(domain.UsageAmount{InputTokens: 300_000, OutputTokens: 100_000}, "default")
	if err != nil || cost != 5_850_000 {
		t.Fatal("external price replaced by Codex rate card")
	}
	if _, err := external.Cost(domain.UsageAmount{InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64}, "default"); err == nil {
		t.Fatal("ledger overflow accepted")
	}
	if _, err := external.Cost(domain.UsageAmount{InputTokens: 1, CachedInputTokens: 2}, "default"); err == nil {
		t.Fatal("invalid usage accepted")
	}
	price := Price{Standard: Rates{100_000, 100_000, 100_000}}
	if got, err := price.Cost(domain.UsageAmount{InputTokens: 9, OutputTokens: 1}, "default"); err != nil || got != 1 {
		t.Fatal("categories rounded independently")
	}
}
