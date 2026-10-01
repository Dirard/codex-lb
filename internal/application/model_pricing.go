package application

import (
	"context"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type ModelPricingStore interface {
	ListModelPriceOverrides(context.Context) (map[string]pricing.Price, error)
	GetModelPriceOverride(context.Context, string) (pricing.Price, bool, error)
	ApplyModelPriceChange(context.Context, string, *pricing.Price, time.Time) (ModelRepriceSummary, error)
}

type ModelRepriceSummary struct {
	Applied               bool  `json:"applied"`
	RecomputedRequests    int64 `json:"recomputedRequests"`
	CostDeltaMicrodollars int64 `json:"costDeltaMicrodollars"`
	PartialRawRequests    int64 `json:"partialRawRequests"`
	PartialFoldedRequests int64 `json:"partialFoldedRequests"`
	PartialFoldedBuckets  int64 `json:"partialFoldedBuckets"`
	UndimensionedHistory  bool  `json:"undimensionedHistory"`
}

type ModelPriceEntry struct {
	Model      string               `json:"model"`
	Price      pricing.Price        `json:"price"`
	Source     string               `json:"source"`
	HasBuiltin bool                 `json:"hasBuiltin"`
	Reprice    *ModelRepriceSummary `json:"reprice,omitempty"`
}

type ModelPriceList struct {
	Prices []ModelPriceEntry `json:"prices"`
}

type ModelPricingService struct {
	store ModelPricingStore
	now   func() time.Time
}

func NewModelPricingService(store ModelPricingStore, now func() time.Time) *ModelPricingService {
	if now == nil {
		now = time.Now
	}
	return &ModelPricingService{store: store, now: now}
}

func (s *ModelPricingService) List(ctx context.Context) (ModelPriceList, error) {
	result := ModelPriceList{Prices: []ModelPriceEntry{}}
	overrides, err := s.store.ListModelPriceOverrides(ctx)
	if err != nil {
		return result, err
	}
	for _, model := range pricing.Models() {
		_, builtin, _ := pricing.LookupCodex(model)
		entry := ModelPriceEntry{Model: model, Price: cloneModelPrice(builtin), Source: "builtin", HasBuiltin: true}
		if custom, ok := overrides[model]; ok {
			entry.Price, entry.Source = cloneModelPrice(custom), "custom"
			delete(overrides, model)
		}
		result.Prices = append(result.Prices, entry)
	}
	for model, custom := range overrides {
		_, _, builtinErr := pricing.LookupCodex(model)
		result.Prices = append(result.Prices, ModelPriceEntry{Model: model, Price: cloneModelPrice(custom),
			Source: "custom", HasBuiltin: builtinErr == nil})
	}
	sort.Slice(result.Prices, func(i, j int) bool { return result.Prices[i].Model < result.Prices[j].Model })
	return result, nil
}

func (s *ModelPricingService) Save(ctx context.Context, model string, price pricing.Price) (ModelPriceEntry, error) {
	model, err := normalizePriceModel(model)
	if err != nil || price.Validate() != nil {
		return ModelPriceEntry{}, domain.ErrInvalid
	}
	summary, err := s.store.ApplyModelPriceChange(ctx, model, &price, s.now().UTC())
	if err != nil {
		return ModelPriceEntry{}, err
	}
	_, _, builtinErr := pricing.LookupCodex(model)
	return ModelPriceEntry{Model: model, Price: cloneModelPrice(price), Source: "custom", HasBuiltin: builtinErr == nil, Reprice: &summary}, nil
}

func (s *ModelPricingService) Delete(ctx context.Context, model string) (ModelRepriceSummary, error) {
	model, err := normalizePriceModel(model)
	if err != nil {
		return ModelRepriceSummary{}, err
	}
	return s.store.ApplyModelPriceChange(ctx, model, nil, s.now().UTC())
}

// ResolveCodex checks an exact custom model first, then existing built-in
// aliases. The returned value is a request-owned price snapshot for settlement.
func (s *ModelPricingService) ResolveCodex(ctx context.Context, model string) (pricing.Price, error) {
	model, err := normalizePriceModel(model)
	if err != nil {
		return pricing.Price{}, pricing.ErrUnpriced
	}
	if custom, ok, err := s.store.GetModelPriceOverride(ctx, model); err != nil {
		return pricing.Price{}, err
	} else if ok {
		return cloneModelPrice(custom), nil
	}
	canonical, builtin, err := pricing.LookupCodex(model)
	if err != nil {
		return pricing.Price{}, err
	}
	if canonical != model {
		if custom, ok, err := s.store.GetModelPriceOverride(ctx, canonical); err != nil {
			return pricing.Price{}, err
		} else if ok {
			return cloneModelPrice(custom), nil
		}
	}
	return cloneModelPrice(builtin), nil
}

func normalizePriceModel(raw string) (string, error) {
	model := strings.ToLower(strings.TrimSpace(raw))
	if len(model) == 0 || len(model) > 128 {
		return "", domain.ErrInvalid
	}
	for i := 0; i < len(model); i++ {
		c := model[i]
		allowed := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			i > 0 && (c == '.' || c == '_' || c == '-' || c == '/' || c == ':')
		if !allowed {
			return "", domain.ErrInvalid
		}
	}
	return model, nil
}

func cloneModelPrice(price pricing.Price) pricing.Price {
	clone := price
	copyRates := func(rates *pricing.Rates) *pricing.Rates {
		if rates == nil {
			return nil
		}
		value := *rates
		return &value
	}
	clone.Priority = copyRates(price.Priority)
	clone.Flex = copyRates(price.Flex)
	clone.Long = copyRates(price.Long)
	return clone
}
