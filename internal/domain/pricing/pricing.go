// Package pricing calculates cost in integer microdollars. These built-in rates
// preserve the fork's Codex rate card; external providers supply their own Price.
package pricing

import (
	"errors"
	"math/big"
	"path"
	"sort"
	"strings"

	"codex-lb/internal/domain"
)

var ErrUnpriced = errors.New("model has no configured price")

// Rates are microdollars per million tokens, not floats or credits. Cached input
// is part of InputTokens, so it is subtracted before pricing uncached input.
type Rates struct {
	Input  int64 `json:"inputMicrodollarsPerMillion"`
	Cached int64 `json:"cachedMicrodollarsPerMillion"`
	Output int64 `json:"outputMicrodollarsPerMillion"`
}

type Price struct {
	Standard                Rates  `json:"standard"`
	Priority                *Rates `json:"priority,omitempty"`
	Flex                    *Rates `json:"flex,omitempty"`
	Long                    *Rates `json:"longContext,omitempty"`
	Threshold               int64  `json:"longContextThreshold,omitempty"`
	PriorityMultiplierMilli int64  `json:"priorityMultiplierMilli,omitempty"`
}

func (p Price) Cost(usage domain.UsageAmount, tier string) (int64, error) {
	if err := usage.Validate(); err != nil {
		return 0, err
	}
	rates, err := p.Effective(usage.InputTokens, tier)
	if err != nil {
		return 0, err
	}
	total := new(big.Int)
	for _, item := range []struct{ tokens, rate int64 }{
		{usage.InputTokens - usage.CachedInputTokens, rates.Input},
		{usage.CachedInputTokens, rates.Cached}, {usage.OutputTokens, rates.Output},
	} {
		product := new(big.Int).Mul(big.NewInt(item.tokens), big.NewInt(item.rate))
		total.Add(total, product)
	}
	// Legacy ledger truncates the combined nonnegative cost once to microdollars.
	// Integer arithmetic removes the accidental extra truncation of binary floats.
	total.Quo(total, big.NewInt(1_000_000))
	if !total.IsInt64() {
		return 0, errors.New("cost exceeds supported ledger range")
	}
	return total.Int64(), nil
}

func (p Price) Effective(input int64, tier string) (Rates, error) {
	if err := p.Validate(); err != nil {
		return Rates{}, err
	}
	rates := p.Standard
	long := p.Long != nil && input > p.Threshold
	if long {
		rates = *p.Long
	}
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "priority", "fast":
		if p.Priority != nil {
			return *p.Priority, nil
		}
		if p.PriorityMultiplierMilli > 0 {
			rates = scaled(rates, p.PriorityMultiplierMilli, p.PriorityMultiplierMilli, 1000)
		}
	case "flex":
		if p.Flex != nil {
			rates = *p.Flex
			if long {
				rates = scaled(rates, 4, 3, 2)
			}
		}
	}
	return rates, nil
}

func (p Price) Validate() error {
	for _, rates := range []*Rates{&p.Standard, p.Priority, p.Flex, p.Long} {
		if rates == nil {
			continue
		}
		for _, value := range []int64{rates.Input, rates.Cached, rates.Output} {
			if value < 0 || value > 1_000_000_000_000 {
				return errors.New("invalid token price")
			}
		}
	}
	if p.PriorityMultiplierMilli < 0 || p.PriorityMultiplierMilli > 100_000 || (p.Long != nil && p.Threshold <= 0) {
		return errors.New("invalid tier or context pricing")
	}
	return nil
}

func scaled(r Rates, inputMultiplier, outputMultiplier, denominator int64) Rates {
	return Rates{r.Input * inputMultiplier / denominator, r.Cached * inputMultiplier / denominator, r.Output * outputMultiplier / denominator}
}

func LookupCodex(model string) (string, Price, error) {
	model = strings.ToLower(model)
	if price, ok := codexPrices[model]; ok {
		return model, price, nil
	}
	best, canonical := 0, ""
	for pattern, target := range aliases {
		matched, _ := path.Match(pattern, model)
		if matched && len(pattern) > best {
			best, canonical = len(pattern), target
		}
	}
	if canonical == "" {
		return "", Price{}, ErrUnpriced
	}
	return canonical, codexPrices[canonical], nil
}

func Models() []string {
	models := make([]string, 0, len(codexPrices))
	for name := range codexPrices {
		models = append(models, name)
	}
	sort.Strings(models)
	return models
}

// All values below are microdollars per 1M tokens (input, cache, output).
var codexPrices = map[string]Price{
	"gpt-6-astra":         {Standard: Rates{10_000_000, 1_000_000, 50_000_000}, PriorityMultiplierMilli: 2500, Flex: &Rates{5_000_000, 500_000, 25_000_000}},
	"gpt-6-sol":           {Standard: Rates{2_000_000, 200_000, 10_000_000}, PriorityMultiplierMilli: 2500, Flex: &Rates{1_000_000, 100_000, 5_000_000}, Threshold: 272_000, Long: &Rates{4_000_000, 400_000, 15_000_000}},
	"gpt-6-luna":          {Standard: Rates{100_000, 10_000, 500_000}, PriorityMultiplierMilli: 2500, Flex: &Rates{50_000, 5_000, 250_000}, Threshold: 272_000, Long: &Rates{200_000, 20_000, 750_000}},
	"gpt-5.6-sol":         {Standard: Rates{5_000_000, 500_000, 30_000_000}, Priority: &Rates{10_000_000, 1_000_000, 60_000_000}, Flex: &Rates{2_500_000, 250_000, 15_000_000}, Threshold: 272_000, Long: &Rates{10_000_000, 1_000_000, 45_000_000}},
	"gpt-5.6-terra":       {Standard: Rates{2_000_000, 200_000, 12_000_000}, Priority: &Rates{4_000_000, 400_000, 24_000_000}, Flex: &Rates{1_000_000, 100_000, 6_000_000}, Threshold: 272_000, Long: &Rates{4_000_000, 400_000, 18_000_000}},
	"gpt-5.6-luna":        {Standard: Rates{200_000, 20_000, 1_200_000}, Priority: &Rates{400_000, 40_000, 2_400_000}, Flex: &Rates{100_000, 10_000, 600_000}, Threshold: 272_000, Long: &Rates{400_000, 40_000, 1_800_000}},
	"gpt-5.5":             {Standard: Rates{5_000_000, 500_000, 30_000_000}, Priority: &Rates{12_500_000, 1_250_000, 75_000_000}, Flex: &Rates{2_500_000, 250_000, 15_000_000}},
	"gpt-5.5-pro":         {Standard: Rates{30_000_000, 30_000_000, 180_000_000}, Flex: &Rates{15_000_000, 15_000_000, 90_000_000}},
	"gpt-5.4":             {Standard: Rates{2_500_000, 250_000, 15_000_000}, Priority: &Rates{5_000_000, 500_000, 30_000_000}, Flex: &Rates{1_250_000, 125_000, 7_500_000}, Threshold: 272_000, Long: &Rates{5_000_000, 500_000, 22_500_000}},
	"gpt-5.4-mini":        {Standard: Rates{750_000, 75_000, 4_500_000}, Flex: &Rates{375_000, 37_500, 2_250_000}},
	"gpt-5.4-nano":        {Standard: Rates{200_000, 20_000, 1_250_000}, Flex: &Rates{100_000, 10_000, 625_000}},
	"gpt-5.4-pro":         {Standard: Rates{30_000_000, 30_000_000, 180_000_000}, Flex: &Rates{15_000_000, 15_000_000, 90_000_000}, Threshold: 272_000, Long: &Rates{60_000_000, 60_000_000, 270_000_000}},
	"gpt-5.3-codex":       {Standard: Rates{1_750_000, 175_000, 14_000_000}, Priority: &Rates{3_500_000, 350_000, 28_000_000}},
	"gpt-5.3":             {Standard: Rates{1_750_000, 175_000, 14_000_000}},
	"gpt-5.3-chat-latest": {Standard: Rates{1_750_000, 175_000, 14_000_000}},
	"gpt-5.2":             {Standard: Rates{1_750_000, 175_000, 14_000_000}, PriorityMultiplierMilli: 2000, Flex: &Rates{875_000, 87_500, 7_000_000}},
	"gpt-5.2-chat-latest": {Standard: Rates{1_750_000, 175_000, 14_000_000}},
	"gpt-5.2-codex":       {Standard: Rates{1_750_000, 175_000, 14_000_000}, Priority: &Rates{3_500_000, 350_000, 28_000_000}},
	"gpt-5.1":             {Standard: Rates{1_250_000, 125_000, 10_000_000}, PriorityMultiplierMilli: 2000, Flex: &Rates{625_000, 62_500, 5_000_000}},
	"gpt-5.1-chat-latest": {Standard: Rates{1_250_000, 125_000, 10_000_000}},
	"gpt-5.1-codex":       {Standard: Rates{1_250_000, 125_000, 10_000_000}, Priority: &Rates{2_500_000, 250_000, 20_000_000}},
	"gpt-5.1-codex-max":   {Standard: Rates{1_250_000, 125_000, 10_000_000}, Priority: &Rates{2_500_000, 250_000, 20_000_000}},
	"gpt-5.1-codex-mini":  {Standard: Rates{250_000, 25_000, 2_000_000}},
	"gpt-5":               {Standard: Rates{1_250_000, 125_000, 10_000_000}, PriorityMultiplierMilli: 2000, Flex: &Rates{625_000, 62_500, 5_000_000}},
	"gpt-5-chat-latest":   {Standard: Rates{1_250_000, 125_000, 10_000_000}},
	"gpt-5-codex":         {Standard: Rates{1_250_000, 125_000, 10_000_000}, Priority: &Rates{2_500_000, 250_000, 20_000_000}},
	// Retained for historical reports, not a public Images API.
	"gpt-image-2":      {Standard: Rates{5_000_000, 2_000_000, 30_000_000}},
	"gpt-image-1.5":    {Standard: Rates{5_000_000, 2_000_000, 30_000_000}},
	"gpt-image-1":      {Standard: Rates{5_000_000, 2_000_000, 30_000_000}},
	"gpt-image-1-mini": {Standard: Rates{5_000_000, 2_000_000, 30_000_000}},
}

var aliases = func() map[string]string {
	values := make(map[string]string, len(codexPrices)+1)
	for model := range codexPrices {
		pattern := model + "*"
		if strings.HasPrefix(model, "gpt-6-") {
			pattern = model + "-*"
		}
		values[pattern] = model
	}
	values["gpt-5.6"] = "gpt-5.6-sol"
	return values
}()
