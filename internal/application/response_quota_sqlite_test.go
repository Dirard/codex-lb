package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func usageQuota(accountID, window string, used float64, reset, at time.Time) domain.AccountQuota {
	return domain.AccountQuota{AccountID: accountID, Window: window, UsedPercent: used, ResetAt: &reset, ObservedAt: at}
}

func TestProxyPurchasedCreditsCoverIncludedWindows(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	has, no := true, false
	balance, zero := 12.5, 0.0
	for _, id := range []string{"account-a", "account-b"} {
		snapshot := domain.AccountUsageSnapshot{AccountID: id, ObservedAt: now,
			Quotas: []domain.AccountQuota{usageQuota(id, "primary", 20, reset, now), usageQuota(id, "secondary", 100, reset, now)}}
		if id == "account-a" {
			snapshot.Credits = &domain.AccountCreditStatus{AccountID: id, Has: &has, Balance: &balance, ObservedAt: now}
		}
		if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := store.GetAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	blocked.Status = domain.AccountQuotaExceeded
	if err := store.SaveAccount(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if candidates, err := store.QuotaCandidateAccounts(ctx, "key-test"); err != nil || len(candidates) != 2 {
		t.Fatalf("credit selection scope = %+v %v", candidates, err)
	}
	if credit, err := store.LoadAccountCreditStatus(ctx, "account-a"); err != nil || credit == nil || !credit.Usable() {
		t.Fatalf("persisted credit status = %+v %v", credit, err)
	}
	if saved, err := store.GetAccount(ctx, "account-a"); err == nil {
		quotas, _ := store.ListAccountQuota(ctx, saved.ID)
		credits, _ := store.LoadAccountCreditStatus(ctx, saved.ID)
		if status := application.EffectiveAccountQuotaStatus(saved, quotas, credits, nil, time.Now()); status != domain.AccountActive {
			t.Fatalf("credit-backed derived status = %s account=%+v quotas=%+v credits=%+v", status, saved, quotas, credits)
		}
	}
	first, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"first"}`), nil)
	if err != nil || len(upstream.accounts) != 1 || upstream.accounts[0] != "account-a" {
		t.Fatalf("credit-backed secondary selection = %+v %v %v", first, err, upstream.accounts)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "account-a", ObservedAt: now.Add(time.Second),
		Credits: &domain.AccountCreditStatus{AccountID: "account-a", Has: &no, Balance: &zero, ObservedAt: now.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountQuota(ctx, usageQuota("account-b", "secondary", 40, reset, now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"new"}`), nil); err != nil || upstream.accounts[1] != "account-b" {
		t.Fatalf("new zero-credit sample did not remove override: %v %v", err, upstream.accounts)
	}
	// An established owner stays pinned when its telemetry is exhausted.
	continuation := json.RawMessage(`{"model":"gpt-6-sol","previous_response_id":"` + first.ResponseID + `","input":"continue"}`)
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, continuation, nil); err != nil || upstream.accounts[2] != "account-a" {
		t.Fatalf("continuation moved on zero credits: %v %v", err, upstream.accounts)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "account-a", ObservedAt: now.Add(2 * time.Second),
		Quotas:  []domain.AccountQuota{usageQuota("account-a", "primary", 100, reset, now.Add(2*time.Second))},
		Credits: &domain.AccountCreditStatus{AccountID: "account-a", Has: &has, Balance: &balance, ObservedAt: now.Add(2 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	blocked, err = store.GetAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	blocked.Status = domain.AccountQuotaExceeded
	if err := store.SaveAccount(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "primary-allowed", APIKeyID: "key-test", AccountID: "account-a",
		Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 1}, Now: now.Add(2 * time.Second)}); err != nil {
		t.Errorf("ledger rejected credit-backed exhausted primary: %v", err)
	}
	if err := store.SaveAccountQuota(ctx, usageQuota("account-b", "primary", 100, reset, now.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"fresh"}`), nil); err != nil || len(upstream.accounts) != 4 || upstream.accounts[3] != "account-a" {
		t.Fatalf("purchased credits did not cover primary exhaustion: %v %v", err, upstream.accounts)
	}
}

func TestProxyPurchasedCreditsCannotOverrideNewerUpstreamRefusal(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	has, balance := true, 12.5
	saveCredit := func(at time.Time) {
		t.Helper()
		if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "account-a", ObservedAt: at,
			Credits: &domain.AccountCreditStatus{AccountID: "account-a", Has: &has, Balance: &balance, ObservedAt: at}}); err != nil {
			t.Fatal(err)
		}
	}
	saveCredit(now.Add(-time.Minute))
	refuse := true
	upstream.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if target.Account.ID == "account-a" && refuse {
			refuse = false
			return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		return complete("resp_"+target.Account.ID, emit)
	}
	request := json.RawMessage(`{"model":"gpt-6-sol","input":"credit refusal"}`)
	options := application.ResponseOptions{KeyID: "key-test"}
	if _, err := proxy.Respond(ctx, options, request, nil); err != nil || len(upstream.accounts) != 2 || upstream.accounts[0] != "account-a" || upstream.accounts[1] != "account-b" {
		t.Fatalf("quota refusal did not fail over normally: %v %v", err, upstream.accounts)
	}
	refusalAt, err := store.LoadAccountQuotaRefusalAt(ctx, "account-a")
	if err != nil || refusalAt == nil {
		t.Fatalf("quota refusal was not recorded: %v", err)
	}
	for attempt := range 2 {
		if attempt == 1 {
			saveCredit(*refusalAt)
		}
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "stale-credit", APIKeyID: "key-test", AccountID: "account-a",
			Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 1}, Now: time.Now()}); !errors.Is(err, domain.ErrNoAccounts) {
			t.Fatalf("reservation reopened refused account using old credits: %v", err)
		}
		before := len(upstream.accounts)
		if _, err := proxy.Respond(ctx, options, request, nil); err != nil || len(upstream.accounts) != before+1 || upstream.accounts[before] != "account-b" {
			t.Fatalf("selection reopened refused account using old credits: %v %v", err, upstream.accounts)
		}
	}
	// Fresh positive evidence can restore admission even with both windows at 100%.
	fresh := refusalAt.Add(time.Second)
	saveCredit(fresh)
	reset := fresh.Add(time.Hour)
	for _, id := range []string{"account-a", "account-b"} {
		for _, window := range []string{"primary", "secondary"} {
			if err := store.SaveAccountQuota(ctx, usageQuota(id, window, 100, reset, fresh)); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := len(upstream.accounts)
	if _, err := proxy.Respond(ctx, options, request, nil); err != nil || len(upstream.accounts) != before+1 || upstream.accounts[before] != "account-a" {
		t.Fatalf("new credit evidence did not restore admission: %v %v", err, upstream.accounts)
	}
	key, err := store.GetAPIKey(ctx, "key-test")
	if err != nil || key.Limits[0].CurrentValue != 80 {
		t.Fatalf("refused request or retry was billed incorrectly: %+v %v", key.Limits, err)
	}
}

func TestProxySparkUsesFreshSeparateQuotaAndFailsClosedWhenMissing(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	for _, id := range []string{"account-a", "account-b"} {
		account, err := store.GetAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		account.PlanType = "pro"
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		standard := []domain.AccountQuota{usageQuota(id, "primary", 100, reset, now), usageQuota(id, "secondary", 100, reset, now)}
		snapshot := domain.AccountUsageSnapshot{AccountID: id, ObservedAt: now, Quotas: standard, AdditionalReported: true}
		if id == "account-a" {
			snapshot.AdditionalQuotas = []domain.AccountAdditionalQuota{{AccountID: id, QuotaKey: "codex_spark", LimitName: "codex_other",
				MeteredFeature: "codex_bengalfox", Window: "primary", UsedPercent: 0, ResetAt: &reset, ObservedAt: now}}
		}
		if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	proxy.ResolvePrice = func(_ context.Context, _ domain.Account, model string) (pricing.Price, error) {
		if model == "gpt-5.3-codex-spark" {
			return pricing.Price{Standard: pricing.Rates{Input: 1_000_000, Output: 1_000_000}}, nil
		}
		return pricing.Price{}, pricing.ErrUnpriced
	}
	model := "gpt-5.3-codex-spark"
	catalogSnapshot := &domain.CatalogSnapshot{
		Models: map[string]domain.CatalogModel{model: {Slug: model, SupportedInAPI: true,
			SourceKind: domain.ModelCatalogSourceSubscription, AvailableInPlans: []string{"pro"}}},
		ModelPlans:              map[string][]string{model: {"pro"}},
		ModelAccounts:           map[string][]string{model: {"account-b"}},
		AccountPlans:            map[string]string{"account-a": "pro", "account-b": "pro"},
		AccountCatalogsComplete: true,
	}
	if err := store.SaveModelCatalogSnapshot(ctx, domain.ModelCatalogRecord{SchemaVersion: application.ModelCatalogSchemaVersion,
		RefreshedAt: now, ContentHash: "synthetic", Snapshot: catalogSnapshot}); err != nil {
		t.Fatal(err)
	}
	catalog := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	if err := catalog.Load(ctx); err != nil {
		t.Fatal(err)
	}
	proxy.Catalog = catalog
	request := json.RawMessage(`{"model":"gpt-5.3-codex-spark","input":"spark"}`)
	first, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, request, nil)
	if err != nil || len(upstream.accounts) != 1 || upstream.accounts[0] != "account-a" {
		t.Fatalf("fresh separate quota did not bypass standard exhaustion: %v %v", err, upstream.accounts)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "account-a", ObservedAt: now.Add(time.Second), AdditionalReported: true}); err != nil {
		t.Fatal(err)
	}
	continuation := json.RawMessage(`{"model":"gpt-5.3-codex-spark","previous_response_id":"` + first.ResponseID + `","input":"continue"}`)
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, continuation, nil); err != nil || upstream.accounts[1] != "account-a" {
		t.Fatalf("established Spark owner lost after telemetry expired: %v %v", err, upstream.accounts)
	}
	_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, request, nil)
	var proxyErr *application.ProxyError
	if !errors.As(err, &proxyErr) || proxyErr.Code != "additional_quota_data_unavailable" || len(upstream.accounts) != 2 {
		t.Fatalf("missing additional evidence dispatched: %v %v", err, upstream.accounts)
	}
}

func TestProxyFreeMonthlyWindowGovernsNewSelection(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(30 * 24 * time.Hour)
	free, err := store.GetAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	free.PlanType = "free"
	if err := store.SaveAccount(ctx, free); err != nil {
		t.Fatal(err)
	}
	for _, q := range []domain.AccountQuota{
		usageQuota("account-a", "primary", 0, reset, now),
		usageQuota("account-a", "secondary", 0, reset, now),
		usageQuota("account-a", "monthly", 100, reset, now),
		usageQuota("account-b", "secondary", 50, reset, now),
	} {
		if err := store.SaveAccountQuota(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	request := json.RawMessage(`{"model":"gpt-6-sol","input":"monthly"}`)
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, request, nil); err != nil || upstream.accounts[0] != "account-b" {
		t.Fatalf("exhausted monthly window admitted free account: %v %v", err, upstream.accounts)
	}
	if err := store.SaveAccountQuota(ctx, usageQuota("account-a", "monthly", 0, reset, now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	for _, window := range []string{"primary", "secondary"} {
		if err := store.SaveAccountQuota(ctx, usageQuota("account-a", window, 100, reset, now.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	request = json.RawMessage(`{"model":"gpt-6-sol","input":"new monthly conversation"}`)
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, request, nil); err != nil || upstream.accounts[1] != "account-a" {
		t.Fatalf("stale 5h/7d row overrode free monthly availability: %v %v", err, upstream.accounts)
	}
}

func TestProxySparkRejectsStaleOrExhaustedSeparateQuota(t *testing.T) {
	for _, test := range []struct {
		name, code string
		used       float64
		age        time.Duration
	}{
		{"stale", "additional_quota_data_unavailable", 0, 10 * time.Minute},
		{"exhausted", "quota_exhausted", 100, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy, store, upstream := proxyFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			reset := now.Add(time.Hour)
			for _, id := range []string{"account-a", "account-b"} {
				account, err := store.GetAccount(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				account.PlanType = "pro"
				if err := store.SaveAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
				observed := now.Add(-test.age)
				if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: id, ObservedAt: observed,
					AdditionalReported: true, AdditionalQuotas: []domain.AccountAdditionalQuota{{AccountID: id,
						QuotaKey: "codex_spark", Window: "primary", UsedPercent: test.used, ResetAt: &reset, ObservedAt: observed}}}); err != nil {
					t.Fatal(err)
				}
			}
			proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) {
				return pricing.Price{Standard: pricing.Rates{Input: 1_000_000, Output: 1_000_000}}, nil
			}
			_, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-5.3-codex-spark","input":"spark"}`), nil)
			var proxyErr *application.ProxyError
			if !errors.As(err, &proxyErr) || proxyErr.Code != test.code || len(upstream.accounts) != 0 {
				t.Fatalf("%s additional quota dispatched: %v %v", test.name, err, upstream.accounts)
			}
		})
	}
}

func TestProxySparkPlusPlanUsesStandardQuota(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "account-a", ObservedAt: now,
		Quotas: []domain.AccountQuota{usageQuota("account-a", "primary", 100, reset, now)}, AdditionalReported: true,
		AdditionalQuotas: []domain.AccountAdditionalQuota{{AccountID: "account-a", QuotaKey: "codex_spark", Window: "primary",
			UsedPercent: 0, ResetAt: &reset, ObservedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) {
		return pricing.Price{Standard: pricing.Rates{Input: 1_000_000, Output: 1_000_000}}, nil
	}
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-5.3-codex-spark","input":"spark"}`), nil); err != nil ||
		len(upstream.accounts) != 1 || upstream.accounts[0] != "account-b" {
		t.Fatalf("plus plan incorrectly borrowed separately metered quota: %v %v", err, upstream.accounts)
	}
}
