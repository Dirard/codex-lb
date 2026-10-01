package application

import (
	"encoding/json"
	"strings"

	"codex-lb/internal/domain"
)

type ModelCatalogSelection struct {
	Model                        string
	ServiceTier                  string
	TierEnforced                 bool
	Candidates                   []domain.Account
	EstablishedOwnerID           string
	AllowAdditionalQuotaOmission bool
	RequireResponsesLite         bool
}

type ModelCatalogSelectionResult struct {
	Candidates             []domain.Account
	EffectiveTier          string
	Authoritative          bool
	QuotaOmittedAccountIDs map[string]bool
	ResponsesLiteMismatch  bool
	PreferWebsockets       bool
}

type ModelCatalogRoutingPolicy interface {
	FilterAccounts(ModelCatalogSelection) ModelCatalogSelectionResult
}

var _ ModelCatalogRoutingPolicy = (*ModelCatalogService)(nil)

// SubscriptionReasoningFallback preserves subscription catalog order without
// a network refresh. External source metadata must not supply this fallback.
func (s *ModelCatalogService) SubscriptionReasoningFallback(model string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot != nil {
		entry := s.snapshot.Models[strings.ToLower(strings.TrimSpace(model))]
		for _, level := range entry.SupportedReasoningLevels {
			effort := strings.ToLower(strings.TrimSpace(level.Effort))
			switch effort {
			case "none", "low", "medium", "high", "xhigh", "max", "ultra":
				return effort
			}
		}
	}
	return "low"
}

// filterCatalogAccounts applies the common model/tier gate before both normal
// Responses and compact admissions, including any authorized tier rewrite.
func filterCatalogAccounts(policy ModelCatalogRoutingPolicy, request *responseRequest, candidates []domain.Account, ownerID string, allowQuotaOmission bool) ([]domain.Account, map[string]bool, error) {
	if policy == nil || len(candidates) == 0 {
		request.PreferWebsockets = domain.BootstrapCatalog()[canonicalModel(request.Model)].PreferWebsockets
		return candidates, nil, nil
	}
	selection := policy.FilterAccounts(ModelCatalogSelection{Model: canonicalModel(request.Model),
		ServiceTier: request.Tier, TierEnforced: request.TierEnforced, RequireResponsesLite: request.ModelEnforced && request.ResponsesLite,
		Candidates: candidates, EstablishedOwnerID: ownerID, AllowAdditionalQuotaOmission: allowQuotaOmission})
	request.PreferWebsockets = selection.PreferWebsockets
	if selection.ResponsesLiteMismatch {
		return nil, nil, &ProxyError{Code: "responses_lite_model_mismatch", Status: 403, Message: "API key enforced model does not support Responses Lite"}
	}
	if len(selection.Candidates) == 0 {
		if ownerID != "" {
			return nil, nil, ownerUnavailable()
		}
		return nil, nil, &ProxyError{Code: "model_not_available", Status: 404, Message: "No permitted account supports the requested model and service tier"}
	}
	if request.Tier != selection.EffectiveTier {
		request.Tier = selection.EffectiveTier
		request.WireClean = false
		if request.Tier == "" {
			delete(request.Object, "service_tier")
		} else {
			request.Object["service_tier"] = jsonString(request.Tier)
		}
	}
	return selection.Candidates, selection.QuotaOmittedAccountIDs, nil
}

// FilterAccounts is the integration hook for response account selection. It
// never re-balances an established owner: when EstablishedOwnerID is set, that
// one account is either authorized by current catalog evidence or removed.
func (s *ModelCatalogService) FilterAccounts(selection ModelCatalogSelection) ModelCatalogSelectionResult {
	result := ModelCatalogSelectionResult{EffectiveTier: selection.ServiceTier}
	snapshot := s.Snapshot()
	result.PreferWebsockets = subscriptionCatalogModel(snapshot, selection.Model).PreferWebsockets
	if selection.RequireResponsesLite && catalogRejectsResponsesLite(snapshot, selection.Model) {
		result.ResponsesLiteMismatch = true
		return result
	}
	result.Authoritative = snapshot != nil && snapshot.AccountCatalogsComplete
	if result.Authoritative {
		for _, account := range selection.Candidates {
			if account.Kind == domain.AccountChatGPT && snapshot.AccountPlans[account.ID] == "" {
				result.Authoritative = false
				break
			}
		}
	}
	if selection.EstablishedOwnerID != "" {
		for _, account := range selection.Candidates {
			if account.ID == selection.EstablishedOwnerID {
				selection.Candidates = []domain.Account{account}
				break
			}
		}
		if len(selection.Candidates) != 1 || selection.Candidates[0].ID != selection.EstablishedOwnerID {
			selection.Candidates = nil
		}
	}
	external, chatgptCandidates := splitCatalogCandidates(selection.Candidates)
	if len(chatgptCandidates) == 0 {
		result.Candidates = external
		return result
	}
	selection.Candidates = chatgptCandidates
	model := strings.ToLower(strings.TrimSpace(selection.Model))
	quotaDefinition, quotaMapped := domain.AdditionalQuotaForModel(model)
	quotaMapped = quotaMapped && selection.AllowAdditionalQuotaOmission
	if snapshot != nil && snapshot.IsSuppressed(model) && !quotaMapped {
		result.Candidates = external
		return result
	}

	modelKnown := snapshot != nil
	if modelKnown {
		_, modelKnown = snapshot.ModelAccounts[model]
	}
	bootstrap := domain.BootstrapCatalog()
	bootstrapModel, bootstrapKnown := bootstrap[model]
	for _, account := range selection.Candidates {
		planAllowed := catalogPlanAllowed(snapshot, model, bootstrapKnown, bootstrapModel, account.PlanType)
		quotaPlanAllowed := quotaMapped && contains(quotaDefinition.Plans, strings.ToLower(strings.TrimSpace(account.PlanType)))
		if !planAllowed && !quotaPlanAllowed {
			continue
		}
		omitted := quotaMapped && snapshot != nil && (result.Authoritative || selection.EstablishedOwnerID != "") &&
			(!modelKnown || !accountSupports(snapshot, account.ID, model))
		if modelKnown && (result.Authoritative || selection.EstablishedOwnerID != "") &&
			!accountSupports(snapshot, account.ID, model) && !quotaMapped {
			continue
		}
		if omitted {
			if !quotaPlanAllowed || !quotaOmissionTierAllowed(snapshot, model, selection.ServiceTier, account.PlanType) {
				continue
			}
			if result.QuotaOmittedAccountIDs == nil {
				result.QuotaOmittedAccountIDs = make(map[string]bool)
			}
			result.QuotaOmittedAccountIDs[account.ID] = true
		}
		result.Candidates = append(result.Candidates, account)
	}

	result.Candidates = append(result.Candidates, external...)
	if domain.OmitEquivalentServiceTier(selection.ServiceTier) {
		result.EffectiveTier = ""
		return result
	}
	tier := domain.CanonicalServiceTier(selection.ServiceTier)
	if snapshot == nil || !result.Authoritative && selection.EstablishedOwnerID == "" {
		return result
	}
	var live domain.CatalogModel
	liveKnown := false
	if snapshot != nil {
		live, liveKnown = snapshot.Models[model]
	}
	advertised := liveKnown && live.ServiceTiers()[tier]
	if advertised {
		filtered := make([]domain.Account, 0, len(result.Candidates)+len(external))
		filtered = append(filtered, external...)
		for _, account := range result.Candidates {
			if account.Kind != domain.AccountChatGPT || accountSupportsTier(snapshot, account.ID, model, tier) ||
				result.QuotaOmittedAccountIDs[account.ID] && quotaOmissionTierAllowed(snapshot, model, tier, account.PlanType) {
				filtered = append(filtered, account)
			}
		}
		result.Candidates = filtered
		return result
	}
	if len(external) != 0 {
		result.Candidates = external
		return result
	}
	if !modelKnown {
		return result
	}
	if !advertised {
		if selection.TierEnforced {
			result.EffectiveTier = ""
			return result
		}
		result.Candidates = nil
		return result
	}
	return result
}

func subscriptionCatalogModel(snapshot *domain.CatalogSnapshot, model string) domain.CatalogModel {
	model = canonicalModel(model)
	entry, known := domain.CatalogModel{}, false
	if snapshot != nil {
		entry, known = snapshot.MetadataModels[model]
		if !known {
			entry, known = snapshot.Models[model]
		}
	}
	if !known {
		entry = domain.BootstrapCatalog()[model]
	}
	return entry
}

func catalogRejectsResponsesLite(snapshot *domain.CatalogSnapshot, model string) bool {
	entry := subscriptionCatalogModel(snapshot, model)
	var capability *bool
	return json.Unmarshal(entry.Raw["use_responses_lite"], &capability) == nil && capability != nil && !*capability
}

func quotaOmissionTierAllowed(snapshot *domain.CatalogSnapshot, model, tier, plan string) bool {
	if domain.OmitEquivalentServiceTier(tier) {
		return true
	}
	if snapshot == nil {
		return false
	}
	for _, allowed := range snapshot.ModelTierPlans[model][domain.CanonicalServiceTier(tier)] {
		if strings.EqualFold(allowed, strings.TrimSpace(plan)) {
			return true
		}
	}
	return false
}

func splitCatalogCandidates(accounts []domain.Account) (external, chatgpt []domain.Account) {
	for _, account := range accounts {
		if account.Kind == domain.AccountChatGPT {
			chatgpt = append(chatgpt, account)
		} else {
			external = append(external, account)
		}
	}
	return external, chatgpt
}

func catalogPlanAllowed(snapshot *domain.CatalogSnapshot, model string, bootstrapKnown bool, bootstrapModel domain.CatalogModel, plan string) bool {
	if snapshot != nil {
		if plans, ok := snapshot.ModelPlans[model]; ok {
			return contains(plans, plan)
		}
		if snapshot.AccountCatalogsComplete {
			return true
		}
	}
	if bootstrapKnown {
		return contains(bootstrapModel.AvailableInPlans, plan)
	}
	return true
}

func accountSupports(snapshot *domain.CatalogSnapshot, accountID, model string) bool {
	if ids, ok := snapshot.ModelAccounts[model]; ok {
		return contains(ids, accountID)
	}
	return false
}

func accountSupportsTier(snapshot *domain.CatalogSnapshot, accountID, model, tier string) bool {
	if tiers, ok := snapshot.ModelTierAccounts[model]; ok {
		if ids, ok := tiers[tier]; ok {
			return contains(ids, accountID)
		}
	}
	return false
}
