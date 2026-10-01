package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type catalogPolicyFunc func(application.ModelCatalogSelection) application.ModelCatalogSelectionResult

func (f catalogPolicyFunc) FilterAccounts(s application.ModelCatalogSelection) application.ModelCatalogSelectionResult {
	return f(s)
}

func TestCatalogSelectionDoesNotMigrateAnIneligibleExistingOwner(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	blockedOwner := false
	proxy.Catalog = catalogPolicyFunc(func(selection application.ModelCatalogSelection) application.ModelCatalogSelectionResult {
		if selection.EstablishedOwnerID != "" && blockedOwner {
			if len(selection.Candidates) != 1 || selection.Candidates[0].ID != selection.EstablishedOwnerID {
				t.Fatal("catalog saw alternative candidates for an active owner")
			}
			return application.ModelCatalogSelectionResult{}
		}
		return application.ModelCatalogSelectionResult{Candidates: selection.Candidates, EffectiveTier: selection.ServiceTier}
	})
	ctx := context.Background()
	options := application.ResponseOptions{KeyID: "key-test"}
	first, err := proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"hello"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "account-a", Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(fmt.Sprintf(`{"model":"gpt-6-sol","input":"continue","previous_response_id":%q}`, first.ResponseID))
	if _, err := proxy.Respond(ctx, options, body, nil); err != nil || upstream.accounts[1] != "account-a" {
		t.Fatalf("catalog wiring broke owner at zero quota: %v", err)
	}
	blockedOwner = true
	if _, err := proxy.Respond(ctx, options, body, nil); err == nil || len(upstream.accounts) != 2 {
		t.Fatal("catalog mismatch silently moved continuation to another account")
	}
}

func TestCatalogTierFallbackRewritesBothInitialAndQuotaReplayWire(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "key-test")
	if err != nil {
		t.Fatal(err)
	}
	tier := "priority"
	key.EnforcedServiceTier = &tier
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	proxy.Catalog = catalogPolicyFunc(func(selection application.ModelCatalogSelection) application.ModelCatalogSelectionResult {
		if !selection.TierEnforced {
			t.Fatal("enforced tier provenance lost")
		}
		return application.ModelCatalogSelectionResult{Candidates: selection.Candidates, EffectiveTier: ""}
	})
	upstream.respond = func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if strings.Contains(string(body), "service_tier") {
			t.Fatal("fallback was not applied to provider wire")
		}
		if target.Account.ID == "account-a" {
			return application.ResponseResult{}, &application.ProviderFailure{Code: "insufficient_quota", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		return complete("done", emit)
	}
	_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello"}`), nil)
	if err != nil || len(upstream.accounts) != 2 || upstream.accounts[1] != "account-b" {
		t.Fatalf("quota replay failed: %v", err)
	}
}
