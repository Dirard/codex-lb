package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type viewUsageStub struct {
	snapshot application.UsageSnapshot
}

type stagedPlanUsage struct {
	viewUsageStub
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	first   application.UsageSnapshot
	second  application.UsageSnapshot
}

func (s *stagedPlanUsage) FetchUsage(ctx context.Context, _, _ string) (application.UsageSnapshot, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.entered)
		select {
		case <-s.release:
			return s.first, nil
		case <-ctx.Done():
			return application.UsageSnapshot{}, ctx.Err()
		}
	}
	return s.second, nil
}

func (s viewUsageStub) FetchUsage(context.Context, string, string) (application.UsageSnapshot, error) {
	return s.snapshot, nil
}

func (s viewUsageStub) FetchResetCredits(context.Context, string, string) (application.ResetCredits, error) {
	return application.ResetCredits{}, nil
}

func (s viewUsageStub) ConsumeResetCredit(context.Context, string, string, string, string) (application.ResetCreditConsume, error) {
	return application.ResetCreditConsume{}, nil
}

func TestListAccountsIncludesQuotaCapacityAndTelemetryFields(t *testing.T) {
	server, store, handler := newAccountsTestServer(t, &httpStubOAuth{})
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.AdditionalQuotaRoutingPolicies = map[string]string{"codex_spark": "preserve"}
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, viewUsageStub{}, &noPins{}, time.Now)
	server.ConfigureUsage(usage)
	// listAccounts lives in registerAdmin; exercise the full production mux.
	handler = server.Handler(nil, nil)

	known := domain.Account{ID: "acct_plus", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg1", Email: "plus@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountQuotaExceeded, CreatedAt: time.Now().UTC()}
	unknown := domain.Account{ID: "acct_custom", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg2", Email: "custom@example.test", PlanType: "custom-plan", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now().UTC()}
	free := domain.Account{ID: "acct_free", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg3", Email: "free@example.test", PlanType: "free", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now().UTC()}
	for _, account := range []domain.Account{known, unknown, free} {
		if err := store.SaveAccount(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(context.Background(), encryptHTTPTestToken(t, server, account.ID)); err != nil {
			t.Fatal(err)
		}
	}
	minutes := 300
	if err := store.SaveAccountQuota(context.Background(), domain.AccountQuota{AccountID: known.ID, Window: "primary", UsedPercent: 40, WindowMinutes: &minutes, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	monthlyReset := time.Now().Add(30 * 24 * time.Hour).UTC()
	for _, quota := range []domain.AccountQuota{
		{AccountID: free.ID, Window: "primary", UsedPercent: 100, ResetAt: &monthlyReset, ObservedAt: time.Now().UTC()},
		{AccountID: free.ID, Window: "secondary", UsedPercent: 100, ResetAt: &monthlyReset, ObservedAt: time.Now().UTC()},
		{AccountID: free.ID, Window: "monthly", UsedPercent: 40, ResetAt: &monthlyReset, ObservedAt: time.Now().UTC()},
	} {
		if err := store.SaveAccountQuota(context.Background(), quota); err != nil {
			t.Fatal(err)
		}
	}
	// Populate the in-memory telemetry snapshot through the real service path.
	snapshotStub := viewUsageStub{snapshot: application.UsageSnapshot{
		PlanType: "plus", Primary: &application.UsageWindow{UsedPercent: floatPtr(40), WindowMinutes: &minutes},
		Credits:          application.UsageCredits{Has: boolPtr(true), Unlimited: boolPtr(false), Balance: stringPtr("12.5")},
		AdditionalQuotas: []application.AdditionalQuota{{LimitName: "codex_other", MeteredFeature: "codex_bengalfox", Primary: &application.UsageWindow{UsedPercent: floatPtr(25)}}},
	}}
	usageStubService := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, snapshotStub, &noPins{}, time.Now)
	if err := usageStubService.RefreshAccountUsage(context.Background(), known.ID); err != nil {
		t.Fatal(err)
	}
	// Point the server at the stub-backed service for the snapshot view.
	server.ConfigureUsage(usageStubService)
	handler = server.Handler(nil, nil)

	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	cookie := setupRec.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodGet, "http://localhost:2455/api/accounts", nil)
	req.RemoteAddr = "127.0.0.1:5678"
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"windowMinutesPrimary":300`) || !strings.Contains(body, `"capacityCreditsPrimary":225`) ||
		!strings.Contains(body, `"remainingCreditsPrimary":135`) || !strings.Contains(body, `"creditsHas":true`) ||
		!strings.Contains(body, `"creditsBalance":12.5`) || !strings.Contains(body, `"limitName":"codex_other"`) {
		t.Fatalf("known-plan view fields missing: %s", body)
	}
	// A new process has no usageSeen cache; the account DTO must still read
	// purchased credits and additional windows from SQLite.
	server.ConfigureUsage(nil)
	handler = server.Handler(nil, nil)
	restarted := httptest.NewRequest(http.MethodGet, "http://localhost:2455/api/accounts", nil)
	restarted.RemoteAddr = "127.0.0.1:5678"
	restarted.AddCookie(cookie)
	restartedRec := httptest.NewRecorder()
	handler.ServeHTTP(restartedRec, restarted)
	if restartedRec.Code != http.StatusOK || !strings.Contains(restartedRec.Body.String(), `"creditsBalance":12.5`) ||
		!strings.Contains(restartedRec.Body.String(), `"quotaKey":"codex_spark"`) ||
		!strings.Contains(restartedRec.Body.String(), `"routingPolicy":"preserve"`) {
		t.Fatalf("persisted account usage missing after service restart: %d %s", restartedRec.Code, restartedRec.Body.String())
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, account := range payload.Accounts {
		if account["accountId"] == "acct_plus" && account["status"] != "active" {
			t.Fatalf("credit-backed status mismatched routing: %+v", account)
		}
		if account["accountId"] == "acct_free" {
			usage := account["usage"].(map[string]any)
			if usage["monthlyRemainingPercent"] != float64(60) || usage["primaryRemainingPercent"] != nil || usage["secondaryRemainingPercent"] != nil ||
				account["capacityCreditsSecondary"] != nil || account["status"] != "active" {
				t.Fatalf("free monthly-only view = %+v", account)
			}
		}
		if account["accountId"] != "acct_custom" {
			continue
		}
		if account["capacityCreditsPrimary"] != nil {
			t.Fatalf("custom plan invented capacity: %v", account["capacityCreditsPrimary"])
		}
		if account["availableResetCredits"] != nil {
			t.Fatalf("custom plan invented reset credits: %v", account["availableResetCredits"])
		}
	}
}

func TestConfirmedUsagePlanDowngradeChangesAccountView(t *testing.T) {
	ctx := context.Background()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	account := domain.Account{ID: "acct_downgrade", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg-down",
		Email: "down@example.test", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now().UTC()}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, encryptHTTPTestToken(t, server, account.ID)); err != nil {
		t.Fatal(err)
	}
	used, minutes, reset := 0.0, 30*24*60, time.Now().Add(30*24*time.Hour).UTC()
	stub := viewUsageStub{snapshot: application.UsageSnapshot{PlanType: "free", Monthly: &application.UsageWindow{
		UsedPercent: &used, ResetAt: &reset, WindowMinutes: &minutes}}}
	clock := time.Now().UTC()
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, stub, &noPins{}, func() time.Time {
		clock = clock.Add(10 * time.Millisecond)
		return clock
	})
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if saved, err := store.GetAccount(ctx, account.ID); err != nil || saved.PlanType != "plus" {
		t.Fatalf("single free report changed paid plan: %+v %v", saved, err)
	}
	if quotas, err := store.ListAccountQuota(ctx, account.ID); err != nil || len(quotas) != 0 {
		t.Fatalf("unconfirmed monthly quota published: %+v %v", quotas, err)
	}
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(nil, nil)
	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	cookie := setupRec.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodGet, "http://localhost:2455/api/accounts", nil)
	req.RemoteAddr = "127.0.0.1:5678"
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("account view = %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || len(payload.Accounts) != 1 {
		t.Fatalf("account view payload = %+v %v", payload, err)
	}
	view := payload.Accounts[0]
	usageView := view["usage"].(map[string]any)
	if view["planType"] != "free" || usageView["monthlyRemainingPercent"] != float64(100) ||
		usageView["primaryRemainingPercent"] != nil || usageView["secondaryRemainingPercent"] != nil {
		t.Fatalf("confirmed free account still presented as paid: %+v", view)
	}
}

func TestOlderInFlightUsageCannotRevertNewerPlan(t *testing.T) {
	ctx := context.Background()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	account := domain.Account{ID: "acct_plan_race", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg-race",
		Email: "race@example.test", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now().UTC()}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, encryptHTTPTestToken(t, server, account.ID)); err != nil {
		t.Fatal(err)
	}
	oldUsed, newUsed := 90.0, 20.0
	stub := &stagedPlanUsage{entered: make(chan struct{}), release: make(chan struct{}),
		first:  application.UsageSnapshot{PlanType: "plus", Primary: &application.UsageWindow{UsedPercent: &oldUsed}},
		second: application.UsageSnapshot{PlanType: "pro", Primary: &application.UsageWindow{UsedPercent: &newUsed}}}
	clock := time.Now().UTC()
	var clockMu sync.Mutex
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, stub, &noPins{}, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(10 * time.Millisecond)
		return clock
	})
	done := make(chan error, 1)
	go func() { done <- usage.RefreshAccountUsage(ctx, account.ID) }()
	select {
	case <-stub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first usage fetch did not start")
	}
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	close(stub.release)
	if err := <-done; !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("old fetch was not fenced: %v", err)
	}
	saved, err := store.GetAccount(ctx, account.ID)
	quotas, quotaErr := store.ListAccountQuota(ctx, account.ID)
	if err != nil || quotaErr != nil || saved.PlanType != "pro" || len(quotas) != 1 || quotas[0].UsedPercent != 20 {
		t.Fatalf("stale usage reverted newer plan/quota: %+v %+v errors=%v/%v", saved, quotas, err, quotaErr)
	}
	if err := store.TransitionAccountStatus(ctx, account.ID, domain.AccountActive, "", domain.AccountPaused); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: clock.Add(time.Minute), FetchStartedAt: clock.Add(time.Minute),
		ExpectedAccount: &saved, ExpectedCredential: &credential, ReportedPlanType: "plus"}
	if err := store.SaveAccountUsageSnapshot(ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("usage write crossed operator pause: %v", err)
	}
	if paused, err := store.GetAccount(ctx, account.ID); err != nil || paused.Status != domain.AccountPaused || paused.PlanType != "pro" {
		t.Fatalf("operator state overwritten: %+v %v", paused, err)
	}
}

type noPins struct{}

func (noPins) GetPinnedResetCredit(context.Context, string, int64, string) (application.ResetRedemption, bool, error) {
	return application.ResetRedemption{}, false, nil
}

func (noPins) PinResetCredit(context.Context, string, int64, string, string) (application.ResetRedemption, error) {
	return application.ResetRedemption{}, nil
}

func floatPtr(value float64) *float64 { return &value }

func boolPtr(value bool) *bool { return &value }

func stringPtr(value string) *string { return &value }
