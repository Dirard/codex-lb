package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestKeyUsageSelfServiceScopesAndPrivacy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt([]byte("synthetic-only-token"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	resetPrimary, resetSecondary := now.Add(4*time.Hour), now.Add(6*24*time.Hour)
	for _, account := range []domain.Account{
		{ID: "acct-a", Kind: domain.AccountChatGPT, Provider: "openai", Email: "a@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now},
		{ID: "acct-b", Kind: domain.AccountChatGPT, Provider: "openai", Email: "b@example.invalid", PlanType: "pro", Status: domain.AccountActive, CreatedAt: now},
	} {
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID,
			AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
			t.Fatal(err)
		}
		used := 25.0
		if account.ID == "acct-b" {
			used = 90
		}
		for _, quota := range []domain.AccountQuota{
			{AccountID: account.ID, Window: "primary", UsedPercent: used, ResetAt: &resetPrimary, ObservedAt: now},
			{AccountID: account.ID, Window: "secondary", UsedPercent: 50, ResetAt: &resetSecondary, ObservedAt: now},
		} {
			if err := store.SaveAccountQuota(ctx, quota); err != nil {
				t.Fatal(err)
			}
		}
	}
	groupID := "group-a"
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: "A", AccountIDs: []string{"acct-a"},
		Limits: []domain.LimitRule{{Type: domain.LimitCredits, Window: domain.WindowFiveHours, MaxValue: 100}}}, now); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, token string }{{"key-a", "synthetic-key-a"}, {"key-b", "synthetic-key-b"}} {
		key := domain.APIKey{ID: item.id, Name: item.id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(item.token))),
			KeyPrefix: item.token, IsActive: true, CreatedAt: now, UsageSections: "upstream_limits,account_pool_usage"}
		if item.id == "key-a" {
			key.GroupID = &groupID
		} else {
			key.AccountAssignmentScopeEnabled = true
			key.AssignedAccountIDs = []string{"acct-b"}
		}
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ key, account string }{{"key-a", "acct-a"}, {"key-b", "acct-b"}} {
		reservation := domain.ReservationRequest{ID: "reservation-" + item.key, APIKeyID: item.key,
			AccountID: item.account, Model: "gpt-test", Now: now}
		if _, err := store.ReserveUsage(ctx, reservation); err != nil {
			t.Fatal(err)
		}
		event := domain.UsageEvent{RequestID: "request-" + item.key, APIKeyID: item.key,
			AccountID: item.account, Model: "gpt-test", RequestKind: "normal", Status: "success", RequestedAt: now,
			Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2, CostMicrodollars: 1000000}}
		if _, err := store.SettleUsage(ctx, reservation.ID, domain.UsageSettlement{Status: "finalized", Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewKeyUsageHandler(store)
	mux := http.NewServeMux()
	handler.RegisterPublicRoutes(mux)
	server := &Server{config: Config{}}
	public := server.proxyIngress(mux)
	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		public.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/v1/usage", "/v1/usage/"} {
		if response := get(path, ""); response.Code != 401 {
			t.Fatalf("keyless local principal read usage: %d", response.Code)
		}
	}
	if response := get("/v1/usage", "unknown"); response.Code != 401 {
		t.Fatalf("unknown key read usage: %d", response.Code)
	}
	response := get("/v1/usage/", "synthetic-key-a")
	var own application.KeySelfUsage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &own) != nil {
		t.Fatalf("self usage route: %d %s", response.Code, response.Body.String())
	}
	if own.RequestCount != 1 || own.TotalTokens != 15 || own.CachedInputTokens != 2 || own.TotalCostUSD != 1 ||
		len(own.Limits) != 1 || own.Limits[0].Source != "api_key_limit" || len(own.UpstreamLimits) != 2 ||
		own.UpstreamLimits[0].MaxValue != 225 || own.AccountPoolUsage == nil || own.AccountPoolUsage.Primary == nil ||
		*own.AccountPoolUsage.Primary != 75 {
		t.Fatalf("unscoped or fabricated usage: %+v", own)
	}
	other := get("/v1/usage", "synthetic-key-b")
	var otherUsage application.KeySelfUsage
	if other.Code != 200 || json.Unmarshal(other.Body.Bytes(), &otherUsage) != nil ||
		len(otherUsage.Limits) != 2 || otherUsage.Limits[0].MaxValue != 1500 ||
		otherUsage.AccountPoolUsage == nil || otherUsage.AccountPoolUsage.Primary == nil ||
		*otherUsage.AccountPoolUsage.Primary != 10 {
		t.Fatalf("assigned-account scope not applied: %d %s", other.Code, other.Body.String())
	}
	headers, err := handler.QuotaHeaders(ctx, "key-a")
	if err != nil || headers["x-codex-primary-used-percent"] != "25" || headers["x-codex-primary-window-minutes"] != "300" ||
		headers["x-codex-credits-balance"] != "" {
		t.Fatalf("scoped quota headers: %+v, %v", headers, err)
	}
	codexRequest := httptest.NewRequest(http.MethodGet, "http://localhost/api/codex/usage", nil)
	codexRequest.Header.Set("Authorization", "Bearer synthetic-key-a")
	codexResponse := httptest.NewRecorder()
	handler.ServeCodexKeyUsage(codexResponse, codexRequest)
	var codex application.CodexKeyUsage
	if codexResponse.Code != 200 || json.Unmarshal(codexResponse.Body.Bytes(), &codex) != nil ||
		codex.RateLimit == nil || codex.RateLimit.PrimaryWindow == nil || codex.Credits != nil {
		t.Fatalf("codex key usage view: %d %s", codexResponse.Code, codexResponse.Body.String())
	}
	keyA, err := store.GetAPIKey(ctx, "key-a")
	if err != nil {
		t.Fatal(err)
	}
	keyA.UsageSections = "account_pool_usage"
	if err := store.SaveAPIKey(ctx, keyA, now); err != nil {
		t.Fatal(err)
	}
	poolOnly := get("/v1/usage", "synthetic-key-a")
	var poolUsage application.KeySelfUsage
	if poolOnly.Code != 200 || json.Unmarshal(poolOnly.Body.Bytes(), &poolUsage) != nil ||
		len(poolUsage.UpstreamLimits) != 0 || poolUsage.AccountPoolUsage == nil || poolUsage.AccountPoolUsage.Primary == nil {
		t.Fatalf("usageSections account_pool_usage not honored: %d %s", poolOnly.Code, poolOnly.Body.String())
	}
	headers, err = handler.QuotaHeaders(ctx, "key-a")
	if err != nil || len(headers) != 0 {
		t.Fatalf("usageSections leaked quota headers: %+v, %v", headers, err)
	}
	keyA.UsageSections = "upstream_limits,account_pool_usage"
	if err := store.SaveAPIKey(ctx, keyA, now); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HideUpstreamQuotaFromKeys = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	private := get("/v1/usage", "synthetic-key-a")
	var hidden application.KeySelfUsage
	if private.Code != 200 || json.Unmarshal(private.Body.Bytes(), &hidden) != nil ||
		len(hidden.Limits) != 1 || len(hidden.UpstreamLimits) != 0 || hidden.AccountPoolUsage != nil {
		t.Fatalf("quota privacy lost own usage or leaked upstream: %d %s", private.Code, private.Body.String())
	}
	headers, err = handler.QuotaHeaders(ctx, "key-a")
	if err != nil || len(headers) != 0 {
		t.Fatalf("quota privacy leaked headers: %+v, %v", headers, err)
	}
	settings.HideUpstreamQuotaFromKeys = false
	settings.Version++
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: "A", AccountIDs: []string{},
		Limits: []domain.LimitRule{{Type: domain.LimitCredits, Window: domain.WindowFiveHours, MaxValue: 100}}}, now); err != nil {
		t.Fatal(err)
	}
	removed := get("/v1/usage", "synthetic-key-a")
	var afterRemoval application.KeySelfUsage
	if removed.Code != 200 || json.Unmarshal(removed.Body.Bytes(), &afterRemoval) != nil ||
		len(afterRemoval.UpstreamLimits) != 0 || afterRemoval.AccountPoolUsage == nil ||
		afterRemoval.AccountPoolUsage.Primary != nil || afterRemoval.AccountPoolUsage.Secondary != nil {
		t.Fatalf("removed group member quota still visible: %d %s", removed.Code, removed.Body.String())
	}
	keyB, err := store.GetAPIKey(ctx, "key-b")
	if err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Second)
	keyB.ExpiresAt = &expired
	if err := store.SaveAPIKey(ctx, keyB, now); err != nil {
		t.Fatal(err)
	}
	if response := get("/v1/usage", "synthetic-key-b"); response.Code != 401 {
		t.Fatalf("expired key read usage: %d", response.Code)
	}
}
