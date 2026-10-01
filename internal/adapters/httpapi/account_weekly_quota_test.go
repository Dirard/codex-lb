package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/chatgpt"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestWeeklyOnlyQuotaReachesAccountAndDashboardWithoutPhantomPrimary(t *testing.T) {
	ctx := context.Background()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	now := time.Now().UTC()
	account := domain.Account{ID: "weekly-pro", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "synthetic-weekly",
		Email: "weekly@example.test", PlanType: "pro", Status: domain.AccountActive, CreatedAt: now}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, encryptHTTPTestToken(t, server, account.ID)); err != nil {
		t.Fatal(err)
	}
	week := 10080
	reset := now.Add(6 * 24 * time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", WindowMinutes: &week,
		UsedPercent: 41, ResetAt: &reset, ObservedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var upstreamBody atomic.Value
	upstreamBody.Store(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":41,"limit_window_seconds":604800,"reset_at":1900000000},"secondary_window":null}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Error("unexpected upstream operation")
		}
		_, _ = w.Write([]byte(upstreamBody.Load().(string)))
	}))
	defer upstream.Close()
	client := chatgpt.NewUsageClient(chatgpt.UsageConfig{BaseURL: upstream.URL, HTTPClient: upstream.Client()})
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, client, &noPins{}, func() time.Time { return now })
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	server.ConfigureUsage(usage)
	handler := server.Handler(nil, nil)
	setup := httptest.NewRequest(http.MethodPost, "http://localhost/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.RemoteAddr = "127.0.0.1:5678"
	setup.Header.Set("Content-Type", "application/json")
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	if setupRec.Code != 200 {
		t.Fatalf("setup status %d", setupRec.Code)
	}
	read := func(path string, into any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.AddCookie(setupRec.Result().Cookies()[0])
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), into) != nil {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
	}
	var accounts struct {
		Accounts []struct {
			Primary *int `json:"windowMinutesPrimary"`
			Weekly  *int `json:"windowMinutesSecondary"`
			Usage   struct {
				Primary *float64 `json:"primaryRemainingPercent"`
				Weekly  *float64 `json:"secondaryRemainingPercent"`
			} `json:"usage"`
		} `json:"accounts"`
	}
	read("/api/accounts", &accounts)
	if len(accounts.Accounts) != 1 || accounts.Accounts[0].Primary != nil || accounts.Accounts[0].Usage.Primary != nil ||
		accounts.Accounts[0].Weekly == nil || *accounts.Accounts[0].Weekly != week || accounts.Accounts[0].Usage.Weekly == nil || *accounts.Accounts[0].Usage.Weekly != 59 {
		t.Fatalf("weekly-only account view is incorrect: %+v", accounts.Accounts)
	}
	var overview domain.DashboardOverview
	read("/api/dashboard/overview", &overview)
	if len(overview.Accounts) != 1 || overview.Accounts[0].Usage.PrimaryRemainingPercent != nil ||
		overview.Accounts[0].Usage.SecondaryRemainingPercent == nil || *overview.Accounts[0].Usage.SecondaryRemainingPercent != 59 {
		t.Fatal("dashboard did not use the corrected weekly-only quota")
	}
	// A present but incomplete window must not be interpreted as its absence.
	fiveHour := 300
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", WindowMinutes: &fiveHour,
		UsedPercent: 25, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	upstreamBody.Store(`{"rate_limit":{"primary_window":{},"secondary_window":{"used_percent":42,"limit_window_seconds":604800}}}`)
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	quotas, err := store.ListAccountQuota(ctx, account.ID)
	if err != nil || len(quotas) != 2 || quotas[0].Window != "primary" || quotas[0].UsedPercent != 25 {
		t.Fatal("partial upstream payload erased the known primary window")
	}
	now = now.Add(time.Second)
	upstreamBody.Store(`{"rate_limit":{}}`)
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if quotas, err = store.ListAccountQuota(ctx, account.ID); err != nil || len(quotas) != 2 {
		t.Fatal("empty upstream payload erased known windows")
	}
}
