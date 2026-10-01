package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestDashboardAccountCreditFieldsMatchAccountList(t *testing.T) {
	ctx := context.Background()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	now := time.Now().UTC()
	reset := now.Add(24 * time.Hour)
	for _, test := range []struct {
		id, plan string
		windows  []string
		used     float64
		credits  *domain.AccountCreditStatus
	}{
		{"dual", "plus", []string{"primary", "secondary"}, 40, &domain.AccountCreditStatus{Has: boolPtr(true), Balance: floatPtr(12.5)}},
		{"weekly", "pro", []string{"secondary"}, 50, &domain.AccountCreditStatus{Has: boolPtr(false), Balance: floatPtr(0)}},
		{"monthly", "free", []string{"monthly"}, 40, nil},
		{"unlimited", "plus", []string{"primary", "secondary"}, 100, &domain.AccountCreditStatus{Unlimited: boolPtr(true)}},
		{"exhausted", "plus", []string{"secondary"}, 100, &domain.AccountCreditStatus{Has: boolPtr(false), Balance: floatPtr(0)}},
		{"custom", "custom-plan", []string{"secondary"}, 20, nil},
		{"unknown", "plus", nil, 0, nil},
	} {
		account := domain.Account{ID: test.id, Kind: domain.AccountChatGPT, Provider: "openai",
			Email: test.id + "@example.test", PlanType: test.plan, Status: domain.AccountActive, CreatedAt: now}
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		snapshot := domain.AccountUsageSnapshot{AccountID: test.id, ObservedAt: now, Credits: test.credits}
		if snapshot.Credits != nil {
			snapshot.Credits.AccountID, snapshot.Credits.ObservedAt = test.id, now
		}
		for _, window := range test.windows {
			minutes := map[string]int{"primary": 300, "secondary": 10080, "monthly": 43200}[window]
			snapshot.Quotas = append(snapshot.Quotas, domain.AccountQuota{AccountID: test.id, Window: window,
				UsedPercent: test.used, WindowMinutes: &minutes, ResetAt: &reset, ObservedAt: now})
		}
		if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	token, err := server.auth.SetupPassword(ctx, "synthetic-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(nil, nil)
	read := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := read("/api/dashboard/overview", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dashboard returned %d", rec.Code)
	}
	views := make([]map[string]map[string]any, 0, 2)
	for _, path := range []string{"/api/accounts", "/api/dashboard/overview"} {
		rec := read(path, true)
		var response struct {
			Accounts []map[string]any `json:"accounts"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil || len(response.Accounts) != 7 {
			t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
		}
		accounts := make(map[string]map[string]any)
		for _, account := range response.Accounts {
			accounts[account["accountId"].(string)] = account
		}
		views = append(views, accounts)
	}
	for id, account := range views[0] {
		for _, field := range []string{"status", "windowMinutesPrimary", "windowMinutesSecondary", "windowMinutesMonthly",
			"resetAtPrimary", "resetAtSecondary", "resetAtMonthly", "capacityCreditsPrimary", "capacityCreditsSecondary", "capacityCreditsMonthly",
			"remainingCreditsPrimary", "remainingCreditsSecondary", "remainingCreditsMonthly", "creditsHas", "creditsUnlimited", "creditsBalance"} {
			got, present := views[1][id][field]
			if !present || got != account[field] {
				t.Errorf("dashboard %s.%s = %v (present=%v), account list = %v", id, field, got, present, account[field])
			}
		}
	}
	for id, expected := range map[string]map[string]any{
		"dual":      {"remainingCreditsSecondary": float64(4536), "creditsBalance": 12.5},
		"weekly":    {"remainingCreditsSecondary": float64(25200), "windowMinutesPrimary": nil, "creditsBalance": float64(0)},
		"monthly":   {"remainingCreditsMonthly": 680.4, "windowMinutesSecondary": nil, "creditsBalance": nil},
		"unlimited": {"remainingCreditsSecondary": float64(0), "creditsUnlimited": true, "status": "active"},
		"exhausted": {"remainingCreditsSecondary": float64(0), "creditsBalance": float64(0), "status": "quota_exceeded"},
		"custom":    {"remainingCreditsSecondary": nil, "creditsBalance": nil},
		"unknown":   {"remainingCreditsSecondary": nil, "creditsBalance": nil},
	} {
		for field, want := range expected {
			if got := views[1][id][field]; got != want {
				t.Errorf("dashboard %s.%s = %v, want %v", id, field, got, want)
			}
		}
	}
}
