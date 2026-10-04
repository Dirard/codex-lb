package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codex-lb/internal/adapters/chatgpt"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type providerRecoveryTokenSource struct{}

func (providerRecoveryTokenSource) AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error) {
	return "usage-token", nil
}

func (providerRecoveryTokenSource) ForceRefresh(_ context.Context, _ domain.Account, rejected string) (string, error) {
	return rejected, nil
}

func TestProviderPermissionRefreshRecoversSQLiteStatusAndSelection(t *testing.T) {
	proxy, store, upstream := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(time.Hour)

	account, err := store.GetAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID, account.Status = "chatgpt-account-a", domain.AccountRateLimited
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	other, err := store.GetAccount(ctx, "account-b")
	if err != nil {
		t.Fatal(err)
	}
	other.Status = domain.AccountPaused
	if err := store.SaveAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, window := range []string{"primary", "secondary"} {
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{
			AccountID: account.ID, Window: window, UsedPercent: 0,
			ResetAt: &reset, ObservedAt: now.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.ListAccountQuota(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status := application.EffectiveAccountQuotaStatus(account, before, nil, nil, now); status != domain.AccountRateLimited {
		t.Fatalf("before status = %s", status)
	}

	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"allowed":          true,
				"limit_reached":    false,
				"primary_window":   map[string]any{"used_percent": 0, "reset_at": reset.Unix(), "limit_window_seconds": 18000},
				"secondary_window": map[string]any{"used_percent": 0, "reset_at": reset.Unix(), "limit_window_seconds": 604800},
			},
		})
	}))
	defer usageServer.Close()
	usage := application.NewAccountUsageService(store, providerRecoveryTokenSource{},
		chatgpt.NewUsageClient(chatgpt.UsageConfig{BaseURL: usageServer.URL, HTTPClient: usageServer.Client()}),
		store, func() time.Time { return now })
	usage.ConfigureQuotaRecovery(store)
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}

	saved, err := store.GetAccount(ctx, account.ID)
	if err != nil || saved.Status != domain.AccountActive {
		t.Fatalf("saved status = %s error = %v", saved.Status, err)
	}
	quotas, err := store.ListAccountQuota(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status := application.EffectiveAccountQuotaStatus(saved, quotas, nil, nil, now); status != domain.AccountActive {
		t.Fatalf("effective status = %s quotas = %+v", status, quotas)
	}
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"},
		json.RawMessage(`{"model":"gpt-6-sol","input":"provider recovery"}`), nil); err != nil {
		t.Fatal(err)
	}
	if len(upstream.accounts) != 1 || upstream.accounts[0] != account.ID {
		t.Fatalf("recovered account was not selected: %+v", upstream.accounts)
	}
}

func TestProviderPermissionMissingSiblingStaysBlockedOnLaterPoll(t *testing.T) {
	_, store, _ := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	account, err := store.GetAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID, account.Status = "chatgpt-account-a", domain.AccountRateLimited
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	for _, window := range []string{"primary", "secondary"} {
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{
			AccountID: account.ID, Window: window, UsedPercent: 100,
			ResetAt: &reset, ObservedAt: now.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var calls int
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rateLimit := map[string]any{
			"allowed":        true,
			"limit_reached":  false,
			"primary_window": map[string]any{"used_percent": 0, "reset_at": reset.Unix(), "limit_window_seconds": 18000},
		}
		calls++
		if calls == 3 {
			rateLimit["secondary_window"] = map[string]any{"used_percent": 0, "reset_at": reset.Unix(), "limit_window_seconds": 604800}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rate_limit": rateLimit})
	}))
	defer usageServer.Close()
	current := now
	usage := application.NewAccountUsageService(store, providerRecoveryTokenSource{},
		chatgpt.NewUsageClient(chatgpt.UsageConfig{BaseURL: usageServer.URL, HTTPClient: usageServer.Client()}),
		store, func() time.Time { return current })
	usage.ConfigureQuotaRecovery(store)
	for poll := 1; poll <= 3; poll++ {
		if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
			t.Fatal(err)
		}
		current = current.Add(time.Second)
		saved, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.AccountRateLimited
		if poll == 3 {
			want = domain.AccountActive
		}
		if saved.Status != want {
			t.Fatalf("poll %d status = %s want = %s", poll, saved.Status, want)
		}
		quotas, err := store.ListAccountQuota(ctx, account.ID)
		if err != nil || len(quotas) != 2 {
			t.Fatalf("poll %d governing windows = %+v error = %v", poll, quotas, err)
		}
	}
}
