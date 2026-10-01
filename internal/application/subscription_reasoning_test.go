package application

import (
	"testing"

	"codex-lb/internal/domain"
)

func TestSubscriptionReasoningFallbackUsesCurrentCatalogOrder(t *testing.T) {
	service := NewModelCatalogService(nil, nil, nil, ModelCatalogConfig{})
	if got := service.SubscriptionReasoningFallback("missing"); got != "low" {
		t.Fatalf("bootstrap fallback: %s", got)
	}
	for _, test := range []struct {
		efforts []string
		want    string
	}{
		{[]string{"minimal", " medium ", "high"}, "medium"},
		{[]string{"minimal", "none", "low"}, "none"},
		{[]string{"", "unknown", "minimal"}, "low"},
	} {
		var levels []domain.CatalogReasoningLevel
		for _, effort := range test.efforts {
			levels = append(levels, domain.CatalogReasoningLevel{Effort: effort})
		}
		service.snapshot = &domain.CatalogSnapshot{Models: map[string]domain.CatalogModel{
			"model": {SupportedReasoningLevels: levels},
		}}
		if got := service.SubscriptionReasoningFallback(" MODEL "); got != test.want {
			t.Fatalf("catalog fallback: %s, want %s", got, test.want)
		}
	}
}
