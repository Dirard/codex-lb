package domain

import (
	"errors"
	"testing"
)

func TestGrokSubscriptionSourceRules(t *testing.T) {
	valid := func() ModelSource {
		return ModelSource{ID: "src_grok", Name: "Grok", Kind: ModelSourceGrokSubscription, BaseURL: GrokTextBaseURL,
			Responses: true, ProviderConfig: ModelSourceProviderConfig{GrokAccountID: "vacc_one"},
			Models: []ModelSourceModel{{Model: "grok-4.7", Enabled: true}}}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid source: %v", err)
	}
	for name, change := range map[string]func(*ModelSource){
		"no account":     func(s *ModelSource) { s.ProviderConfig.GrokAccountID = "" },
		"padded account": func(s *ModelSource) { s.ProviderConfig.GrokAccountID = " vacc_one" },
		"other endpoint": func(s *ModelSource) { s.BaseURL = "https://proxy.example.invalid/v1" },
		"audio":          func(s *ModelSource) { s.Audio = true },
		"embeddings":     func(s *ModelSource) { s.Embeddings = true },
		"account elsewhere": func(s *ModelSource) {
			s.Kind, s.BaseURL = ModelSourceOpenAICompatible, "https://api.example.invalid/v1"
		},
	} {
		source := valid()
		change(&source)
		if err := source.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}
