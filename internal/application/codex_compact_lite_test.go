package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// liteRejectingCatalog declares every model `use_responses_lite: false`.
type liteRejectingCatalog struct{}

func (liteRejectingCatalog) FilterAccounts(selection ModelCatalogSelection) ModelCatalogSelectionResult {
	if selection.RequireResponsesLite {
		return ModelCatalogSelectionResult{ResponsesLiteMismatch: true}
	}
	return ModelCatalogSelectionResult{Candidates: selection.Candidates, EffectiveTier: selection.ServiceTier}
}

type quotaMarkContinuations struct{ Continuations }

func (quotaMarkContinuations) MarkContinuationQuotaRefused(context.Context, string, string, string, string) error {
	return nil
}

// A compact quota replay rebuilt from Lite history must keep the Lite flag, so
// a key-enforced non-Lite model is still rejected by the catalog policy.
func TestCompactQuotaReplayKeepsResponsesLiteForCatalogPolicy(t *testing.T) {
	service, provider, _, store := operationFixture()
	store.Continuations = quotaMarkContinuations{}
	enforced := "gpt-6-sol"
	key := store.keys["key"]
	key.EnforcedModel = &enforced
	store.keys["key"] = key
	history, _ := json.Marshal(responseContext{Items: []json.RawMessage{
		json.RawMessage(`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"lookup","parameters":{}}]}`),
		json.RawMessage(`{"role":"user","content":"original"}`),
		json.RawMessage(`{"type":"custom_tool_call","call_id":"tool-1","name":"apply_patch","input":"patch"}`),
	}})
	store.owner = &domain.Continuation{ResponseID: "resp_owner", KeyID: "key", AccountID: "acct-a", ProviderID: "openai",
		Model: "gpt-6-sol", ContextEncrypted: history, ExpiresAt: time.Now().Add(time.Hour)}
	service.cipher = compactTestCipher{}
	service.Catalog = liteRejectingCatalog{}
	service.selectCompact = func(_ context.Context, _ string, request *responseRequest, _ domain.RuntimeSettings, _ domain.Account, excluded map[string]bool, _ ...string) (domain.Account, *accountLease, error) {
		candidates := []domain.Account{}
		for _, account := range store.eligible {
			if !excluded[account.ID] {
				candidates = append(candidates, account)
			}
		}
		accounts, _, err := filterCatalogAccounts(service.Catalog, request, candidates, "", true)
		if err == nil && len(accounts) == 0 {
			err = domain.ErrNoAccounts
		}
		if err != nil {
			return domain.Account{}, nil, err
		}
		return accounts[0], nil, nil
	}
	provider.results["compact"] = CodexOperationResult{Status: 429, Failed: true, ErrorCode: "usage_limit_reached", ContentType: "application/json", Body: []byte(`{"error":{"code":"usage_limit_reached"}}`)}
	body := []byte(`{"model":"gpt-6-sol","previous_response_id":"resp_owner","input":[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}]}`)
	_, err := service.CompactWithOptions(context.Background(), CodexOperationOptions{KeyID: "key"}, body)
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "responses_lite_model_mismatch" {
		t.Fatalf("Lite replay bypassed the enforced-model catalog policy: %v", err)
	}
	if len(provider.targets) != 1 || provider.targets[0].Account.ID != "acct-a" {
		t.Fatalf("replay reached another account: %+v", provider.targets)
	}
}
