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
	"codex-lb/internal/domain"
)

func TestAPIKeyReportRoutesMatchDashboardContracts(t *testing.T) {
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
	server := New(store, vault, Config{}, nil)
	mux := http.NewServeMux()
	server.registerReportsRoutes(mux, store)
	handler := server.requireAdmin(mux)
	now := time.Now().UTC()
	if err := store.SaveAccount(ctx, domain.Account{ID: "acct", Kind: domain.AccountChatGPT,
		Provider: "openai", Email: "synthetic@example.invalid", PlanType: "plus",
		Status: domain.AccountActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "acct",
		AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{ID: "key", Name: "synthetic", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-key"))),
		KeyPrefix: "synthetic-key", IsActive: true, CreatedAt: now}
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reservation", APIKeyID: "key",
		AccountID: "acct", Model: "gpt-test", Now: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "reservation", domain.UsageSettlement{Status: "finalized",
		Event: domain.UsageEvent{RequestID: "request", APIKeyID: "key", AccountID: "acct", Model: "gpt-test",
			RequestKind: "normal", Status: "success", RequestedAt: now.Add(-time.Hour),
			Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, CostMicrodollars: 250000}}}); err != nil {
		t.Fatal(err)
	}
	request := func(path, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("/api/api-keys/key/trends", "").Code; got != 401 {
		t.Fatalf("unauthenticated trend: %d", got)
	}
	token, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	trendsResponse := request("/api/api-keys/key/trends", token)
	var trends domain.APIKeyTrendsResponse
	if trendsResponse.Code != 200 || json.Unmarshal(trendsResponse.Body.Bytes(), &trends) != nil ||
		trends.KeyID != "key" || len(trends.Cost) < 168 || len(trends.Tokens) != len(trends.Cost) {
		t.Fatalf("trend route/contract: %d %s", trendsResponse.Code, trendsResponse.Body.String())
	}
	usageResponse := request("/api/api-keys/key/usage-7d", token)
	var usage domain.APIKeyUsage7DayResponse
	if usageResponse.Code != 200 || json.Unmarshal(usageResponse.Body.Bytes(), &usage) != nil ||
		usage.KeyID != "key" || usage.TotalRequests != 1 || usage.TotalTokens != 15 ||
		usage.TotalCostUSD != 0.25 || len(usage.AccountCosts) != 1 {
		t.Fatalf("usage route/contract: %d %s", usageResponse.Code, usageResponse.Body.String())
	}
	if got := request("/api/api-keys/missing/usage-7d", token).Code; got != 404 {
		t.Fatalf("missing key report: %d", got)
	}
}
