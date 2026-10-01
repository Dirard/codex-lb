package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func TestKeyReportsIsolationAuthorizationAndReadOnly(t *testing.T) {
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
	now := time.Now().UTC()
	if err := store.SaveAccount(ctx, domain.Account{ID: "private-account", Kind: domain.AccountChatGPT, Provider: "openai",
		Email: "private@example.test", Alias: "private-alias", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credential, err := vault.Encrypt([]byte("synthetic-provider-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "private-account",
		AccessTokenEncrypted: credential, RefreshTokenEncrypted: credential, IDTokenEncrypted: credential}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"key-a", "key-b", "key-exhausted"} {
		key := domain.APIKey{ID: id, Name: id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-"+id))),
			KeyPrefix: "synthetic", IsActive: true, CreatedAt: now}
		if id == "key-exhausted" {
			key.Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 15, ResetAt: now.Add(time.Hour)}}
		}
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct {
		key, suffix string
		when        time.Time
		cost        int64
	}{
		{"key-a", "current", now, 250000}, {"key-b", "current", now, 9000000},
		{"key-a", "previous", now.AddDate(0, 0, -8), 400000}, {"key-b", "previous", now.AddDate(0, 0, -8), 8000000},
		{"key-b", "ancient", now.AddDate(0, 0, -30), 1000000},
		{"key-exhausted", "current", now, 500000},
	} {
		id := event.key + "-" + event.suffix
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: event.key, AccountID: "private-account", Model: "model-" + event.key,
			Budget: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, CostMicrodollars: event.cost}, Now: event.when}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
			RequestID: id, APIKeyID: event.key, AccountID: "private-account", Model: "model-" + event.key,
			UserAgentGroup: "client-" + event.key, RequestKind: "normal", Status: "success", RequestedAt: event.when,
			Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, CostMicrodollars: event.cost},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.UsageTotals(ctx, "key-a", "")
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	cookie, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	public := http.NewServeMux()
	NewKeyUsageHandler(store).RegisterPublicRoutes(public)
	handler := server.Handler(public, nil)
	request := func(method, path, token, session string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/v1/usage/reports", "/v1/usage/reports/"} {
		for _, token := range []string{"", "unknown", "synthetic-" + domain.LocalProxyKeyID} {
			if response := request("GET", path, token, cookie); response.Code != 401 {
				t.Fatalf("invalid/internal/admin-cookie principal accepted: %d", response.Code)
			}
		}
		response := request("GET", path, "synthetic-key-a", "")
		var report domain.KeyReportsResponse
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &report) != nil {
			t.Fatalf("own report: %d %s", response.Code, response.Body.String())
		}
		if report.Summary.TotalCostUSD != .25 || report.Summary.TotalRequests != 1 ||
			report.Comparison.Previous.TotalCostUSD != .4 || len(report.ByModel) != 1 || report.ByModel[0].Model != "model-key-a" {
			t.Fatalf("cross-key aggregate or comparison leaked: %+v", report)
		}
		var dailyCost float64
		for _, day := range report.Daily {
			dailyCost += day.CostUSD
		}
		if dailyCost != .25 || report.Comparison.CanCompare || len(report.ByUserAgent) != 1 || report.ByUserAgent[0].UserAgent != "client-key-a" {
			t.Fatal("daily/client/previous-activity report escaped key scope")
		}
		for _, forbidden := range []string{"byAccount", "private-account", "private@example.test", "private-alias", "key-b", "synthetic-key-a"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("private field %q exposed", forbidden)
			}
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Set-Cookie") != "" {
			t.Fatal("reports must not be cached or create a dashboard session")
		}
		if response := request("POST", path, "synthetic-key-a", ""); response.Code < 400 {
			t.Fatal("report route allowed a mutation")
		}
	}
	for _, query := range []string{"api_key_id=key-b", "apiKeyId=key-b", "account_id=private-account", "key=synthetic-key-b", "model=a&model=b", "start_date=invalid", "start_date=2000-01-01"} {
		if response := request("GET", "/v1/usage/reports?"+query, "synthetic-key-a", ""); response.Code != 400 {
			t.Fatalf("invalid scope/filter accepted: %s status=%d", query, response.Code)
		}
	}
	for _, query := range []string{"model=model-key-a", "useragent_group=client-key-a", "model=model-key-b"} {
		response := request("GET", "/v1/usage/reports?"+query, "synthetic-key-a", "")
		var report domain.KeyReportsResponse
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &report) != nil {
			t.Fatalf("filtered report failed: %s status=%d", query, response.Code)
		}
		if query == "model=model-key-b" && report.Summary.TotalRequests != 0 {
			t.Fatal("model filter escaped authenticated key scope")
		}
	}
	for _, path := range []string{"/api/accounts", "/api/api-keys", "/api/account-groups"} {
		if response := request("GET", path, "synthetic-key-a", ""); response.Code != 401 {
			t.Fatalf("key accessed admin route %s: %d", path, response.Code)
		}
	}
	if response := request("POST", "/api/account-groups", "synthetic-key-a", ""); response.Code != 401 {
		t.Fatal("key granted admin mutation access")
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", "/v1/usage/reports", "", ""); response.Code != 401 {
		t.Fatal("keyless local bypass applied to reports")
	}
	if response := request("GET", "/v1/usage/reports", "synthetic-key-b", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"totalCostUsd":9`) {
		t.Fatal("valid key must work with proxy key auth disabled")
	}
	exhausted, err := store.GetAPIKey(ctx, "key-exhausted")
	if err != nil || len(exhausted.Limits) != 1 || exhausted.Limits[0].CurrentValue != 15 {
		t.Fatal("test key did not exhaust its generation limit")
	}
	if response := request("GET", "/v1/usage/reports", "synthetic-key-exhausted", ""); response.Code != 200 {
		t.Fatal("exhausted generation limit blocked report read")
	}
	for _, id := range []string{"key-a", "key-b", "key-exhausted"} {
		login := request("POST", "/api/key-reports/session", "synthetic-"+id, "")
		if login.Code != 200 || len(login.Result().Cookies()) != 1 {
			t.Fatal("valid/exhausted key could not create report session", id, login.Code)
		}
		browser := reportSessionRequest(handler, "GET", "/api/key-reports/reports", "", login.Result().Cookies()[0])
		bearer := request("GET", "/v1/usage/reports", "synthetic-"+id, "")
		if browser.Code != 200 || browser.Body.String() != bearer.Body.String() {
			t.Fatal("browser session did not preserve bearer report isolation/limits", id, browser.Code)
		}
	}
	internal := NewKeyUsageHandler(internalReportKeyStore{store})
	r := httptest.NewRequest("GET", "/v1/usage/reports", nil)
	r.Header.Set("Authorization", "Bearer synthetic-internal-token")
	w := httptest.NewRecorder()
	internal.serveReports(w, r)
	if w.Code != 401 {
		t.Fatal("internal principal read a self-service report")
	}
	after, err := store.UsageTotals(ctx, "key-a", "")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reading reports changed usage")
	}
	key, err := store.GetAPIKey(ctx, "key-a")
	if err != nil {
		t.Fatal(err)
	}
	key.IsActive = false
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", "/v1/usage/reports", "synthetic-key-a", ""); response.Code != 401 {
		t.Fatal("revoked key read reports")
	}
	key.IsActive = true
	expired := now.Add(-time.Hour)
	key.ExpiresAt = &expired
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", "/v1/usage/reports", "synthetic-key-a", ""); response.Code != 401 {
		t.Fatal("expired key read reports")
	}
}

type internalReportKeyStore struct{ KeyUsageStore }

func (internalReportKeyStore) FindAPIKeyByHash(context.Context, string) (domain.APIKey, error) {
	return domain.APIKey{ID: domain.LocalProxyKeyID, IsActive: true}, nil
}
