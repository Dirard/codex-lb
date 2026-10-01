package application

import (
	"encoding/json"
	"testing"

	"codex-lb/internal/domain"
)

func TestCatalogSparkOmissionRequiresRegistryPlanAndTier(t *testing.T) {
	model := "gpt-5.3-codex-spark"
	pro := domain.Account{ID: "omitted-pro", Kind: domain.AccountChatGPT, PlanType: "pro"}
	plus := domain.Account{ID: "omitted-plus", Kind: domain.AccountChatGPT, PlanType: "plus"}
	supported := domain.Account{ID: "supported", Kind: domain.AccountChatGPT, PlanType: "pro"}
	snapshot := &domain.CatalogSnapshot{
		Models: map[string]domain.CatalogModel{model: {
			Slug: model, SourceKind: domain.ModelCatalogSourceSubscription, SupportedInAPI: true,
			Raw: map[string]json.RawMessage{"service_tiers": json.RawMessage(`[{"id":"priority"}]`)},
		}},
		ModelPlans:              map[string][]string{model: {"pro"}},
		ModelAccounts:           map[string][]string{model: {supported.ID}},
		ModelTierPlans:          map[string]map[string][]string{model: {"priority": {"pro"}}},
		ModelTierAccounts:       map[string]map[string][]string{model: {"priority": {supported.ID}}},
		AccountPlans:            map[string]string{pro.ID: "pro", plus.ID: "plus", supported.ID: "pro"},
		AccountCatalogsComplete: true,
	}
	service := &ModelCatalogService{snapshot: snapshot}
	selection := ModelCatalogSelection{Model: model, ServiceTier: "priority", Candidates: []domain.Account{pro, plus, supported}, AllowAdditionalQuotaOmission: true}
	result := service.FilterAccounts(selection)
	if len(result.Candidates) != 2 || !result.QuotaOmittedAccountIDs[pro.ID] || result.QuotaOmittedAccountIDs[plus.ID] {
		t.Fatalf("quota omission plan/tier = %+v", result)
	}
	selection.AllowAdditionalQuotaOmission = false
	if denied := service.FilterAccounts(selection); len(denied.Candidates) != 1 || denied.Candidates[0].ID != supported.ID {
		t.Fatalf("non-quota path reused omission: %+v", denied)
	}
	selection.AllowAdditionalQuotaOmission = true
	delete(snapshot.ModelTierPlans[model], "priority")
	result = service.FilterAccounts(selection)
	if len(result.Candidates) != 1 || result.Candidates[0].ID != supported.ID {
		t.Fatalf("omitted account bypassed tier plan gate: %+v", result)
	}
}
