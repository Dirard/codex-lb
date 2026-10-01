package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type usageRouteStub struct {
	mu            sync.Mutex
	credits       application.ResetCredits
	consumed      []string
	consumeOK     bool
	consumeErr    error
	creditFetches int
	usage         application.UsageSnapshot
}

func (s *usageRouteStub) FetchUsage(context.Context, string, string) (application.UsageSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage, nil
}

func (s *usageRouteStub) FetchResetCredits(context.Context, string, string) (application.ResetCredits, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creditFetches++
	return s.credits, nil
}

func (s *usageRouteStub) ConsumeResetCredit(_ context.Context, _, _, creditID, _ string) (application.ResetCreditConsume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumed = append(s.consumed, creditID)
	if s.consumeErr != nil {
		return application.ResetCreditConsume{}, s.consumeErr
	}
	if !s.consumeOK {
		return application.ResetCreditConsume{}, &application.UsageError{Code: "upstream_unavailable", Message: "reset credit consume failed", Status: 503}
	}
	redeemed := time.Unix(1893456000, 0).UTC()
	code := "reset"
	if len(s.consumed) > 1 {
		code = "already_redeemed"
	}
	return application.ResetCreditConsume{Code: code, WindowsReset: 2, RedeemedAt: &redeemed}, nil
}

type cipherTokenSource struct {
	cipher application.SecretCipher
}

func (c cipherTokenSource) AccessToken(_ context.Context, _ domain.Account, credential domain.AccountCredential) (string, error) {
	plaintext, err := c.cipher.Decrypt(credential.AccessTokenEncrypted)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (c cipherTokenSource) ForceRefresh(_ context.Context, _ domain.Account, rejectedToken string) (string, error) {
	return rejectedToken, nil
}

func TestAccountUsageRoutesSnapshotAndConsume(t *testing.T) {
	expires := time.Unix(1_800_100_000, 0).UTC()
	stub := &usageRouteStub{consumeOK: true, credits: application.ResetCredits{AvailableCount: 1, Credits: []application.ResetCredit{{
		ID: "credit-1", Status: "available", ResetType: "primary", Title: "Bonus",
		ExpiresAt: &expires,
	}}}}
	server, store, handler := newAccountsTestServer(t, &httpStubOAuth{})
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, stub, store, time.Now)
	usage.ConfigureQuotaRecovery(store)
	admin := http.NewServeMux()
	server.registerAccountRoutes(admin)
	server.registerAccountUsageRoutes(admin, usage)
	protected := server.requireAdmin(admin)
	root := http.NewServeMux()
	server.registerAuth(root)
	root.Handle("/api/", protected)
	handler = root

	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup status = %d body %s", setupRec.Code, setupRec.Body.String())
	}
	cookie := setupRec.Result().Cookies()[0]
	request := func(method, target string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost:2455"+target, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	unauthenticated := httptest.NewRequest(http.MethodGet, "http://localhost:2455/api/accounts/x/rate-limit-reset-credits", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthenticated)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated snapshot status = %d", unauthRec.Code)
	}

	account := domain.Account{
		ID: "acct_usage", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-usage",
		Email: "usage@example.test", PlanType: "plus", RoutingPolicy: "normal",
		Status: domain.AccountQuotaExceeded, CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	encrypted := encryptHTTPTestToken(t, server, account.ID)
	if err := store.SaveAccountCredential(context.Background(), encrypted); err != nil {
		t.Fatal(err)
	}
	oldReset, newReset := time.Now().Add(time.Hour), time.Now().Add(5*time.Hour)
	if err := store.SaveAccountQuota(context.Background(), domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 100, ResetAt: &oldReset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	remaining, creditCount := 0.0, 1
	stub.usage = application.UsageSnapshot{Primary: &application.UsageWindow{UsedPercent: &remaining, ResetAt: &newReset}, ResetCreditCount: &creditCount}

	empty := request(http.MethodGet, "/api/accounts/acct_usage/rate-limit-reset-credits", nil)
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "null" {
		t.Fatalf("empty snapshot = %d %s", empty.Code, empty.Body.String())
	}
	missingSnapshot := request(http.MethodGet, "/api/accounts/missing/rate-limit-reset-credits", nil)
	if missingSnapshot.Code != http.StatusOK || strings.TrimSpace(missingSnapshot.Body.String()) != "null" {
		t.Fatalf("missing snapshot = %d %s", missingSnapshot.Code, missingSnapshot.Body.String())
	}
	if err := usage.RefreshResetCredits(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := request(http.MethodGet, "/api/accounts/acct_usage/rate-limit-reset-credits", nil)
	if snapshot.Code != http.StatusOK || !strings.Contains(snapshot.Body.String(), `"availableCount":1`) || !strings.Contains(snapshot.Body.String(), `"resetType":"primary"`) || !strings.Contains(snapshot.Body.String(), `"nearestExpiresAt"`) {
		t.Fatalf("snapshot = %d %s", snapshot.Code, snapshot.Body.String())
	}

	consume := request(http.MethodPost, "/api/accounts/acct_usage/rate-limit-reset-credits/consume", []byte(`{"redeemRequestId":"redeem-http-1"}`))
	var consumeBody application.ResetCreditConsume
	if consume.Code != http.StatusOK || json.Unmarshal(consume.Body.Bytes(), &consumeBody) != nil || consumeBody.Code != "reset" || consumeBody.WindowsReset != 2 {
		t.Fatalf("consume = %d %s", consume.Code, consume.Body.String())
	}
	var enriched application.ResetCreditResult
	if json.Unmarshal(consume.Body.Bytes(), &enriched) != nil || !enriched.UsageWritten || enriched.AccountID != account.ID || enriched.Status != "reset" ||
		enriched.AccountStatusBefore != "quota_exceeded" || enriched.AccountStatusAfter != "active" || enriched.PrimaryBefore == nil || *enriched.PrimaryBefore != 100 || enriched.PrimaryAfter == nil || *enriched.PrimaryAfter != 0 {
		t.Fatalf("manual reset did not refresh/restore account: %s", consume.Body.String())
	}
	countView := request(http.MethodGet, "/api/accounts/acct_usage/usage-reset-credits", nil)
	if countView.Code != 200 || !strings.Contains(countView.Body.String(), `"rateLimitResetCredits":{"availableCount":1}`) {
		t.Fatalf("legacy count contract: %d %s", countView.Code, countView.Body.String())
	}
	// A newer real provider refusal must survive retry of this old receipt.
	ctx := context.Background()
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "reset-key", Name: "reset key", KeyHash: strings.Repeat("a", 64), KeyPrefix: "sk-synthetic", IsActive: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "post-reset-refusal", APIKeyID: "reset-key", AccountID: account.ID, Model: "gpt-5.5", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "post-reset-refusal", domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{RequestID: "post-reset-refusal", AccountID: account.ID, Model: "gpt-5.5", Status: "error", ErrorCode: "quota_exceeded", RequestedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAccountOutcome(ctx, account.ID, "post-reset-refusal", true, false); err != nil {
		t.Fatal(err)
	}
	retry := request(http.MethodPost, "/api/accounts/acct_usage/usage-reset-credits/consume", []byte(`{"redeemRequestId":"redeem-http-1"}`))
	if retry.Code != http.StatusOK || json.Unmarshal(retry.Body.Bytes(), &enriched) != nil || enriched.Code != "already_redeemed" || enriched.AccountStatusAfter != "quota_exceeded" {
		t.Fatalf("retry = %d %s", retry.Code, retry.Body.String())
	}
	stub.mu.Lock()
	consumed := append([]string(nil), stub.consumed...)
	stub.mu.Unlock()
	if len(consumed) != 2 || consumed[0] != "credit-1" || consumed[1] != "credit-1" {
		t.Fatalf("consumed = %+v", consumed)
	}

	missing := request(http.MethodPost, "/api/accounts/missing/rate-limit-reset-credits/consume", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing consume = %d", missing.Code)
	}
	// The older route lets upstream select a credit, even with no local list.
	stub.credits, stub.consumeOK = application.ResetCredits{}, false
	fetches := stub.creditFetches
	lost := request(http.MethodPost, "/api/accounts/acct_usage/usage-reset-credits/consume", []byte(`{"redeemRequestId":"lost-direct"}`))
	if lost.Code != http.StatusServiceUnavailable {
		t.Fatalf("lost response = %d %s", lost.Code, lost.Body.String())
	}
	stub.consumeOK = true
	reconciled := request(http.MethodPost, "/api/accounts/acct_usage/usage-reset-credits/consume", []byte(`{"redeemRequestId":"lost-direct"}`))
	if reconciled.Code != http.StatusOK || json.Unmarshal(reconciled.Body.Bytes(), &enriched) != nil || enriched.Code != "already_redeemed" || enriched.AccountStatusAfter != "active" {
		t.Fatalf("direct retry = %d %s", reconciled.Code, reconciled.Body.String())
	}
	if stub.creditFetches != fetches || len(stub.consumed) != 4 || stub.consumed[2] != "" || stub.consumed[3] != "" {
		t.Fatalf("direct reset selected a local credit: %+v fetches=%d", stub.consumed, stub.creditFetches)
	}
}

func TestAccountUsageConsumeConflictAndUpstreamErrors(t *testing.T) {
	stub := &usageRouteStub{consumeOK: true}
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	usage := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, stub, store, time.Now)
	admin := http.NewServeMux()
	server.registerAccountUsageRoutes(admin, usage)
	protected := server.requireAdmin(admin)

	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	serverAuth := http.NewServeMux()
	server.registerAuth(serverAuth)
	serverAuth.Handle("/api/", protected)
	serverAuth.ServeHTTP(setupRec, setup)
	cookie := setupRec.Result().Cookies()[0]
	request := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:2455"+target, nil)
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		serverAuth.ServeHTTP(rec, req)
		return rec
	}
	account := domain.Account{ID: "acct_none", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-none", Email: "none@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now().UTC()}
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(context.Background(), encryptHTTPTestToken(t, server, account.ID)); err != nil {
		t.Fatal(err)
	}
	if rec := request("/api/accounts/acct_none/rate-limit-reset-credits/consume"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_available_reset_credit") {
		t.Fatalf("no credit consume = %d %s", rec.Code, rec.Body.String())
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusBadGateway} {
		stub.consumeErr = &application.UsageError{Status: status, Code: "untrusted-provider-code", Message: "synthetic-secret-must-not-leak"}
		rec := request("/api/accounts/acct_none/usage-reset-credits/consume")
		want := status
		if status == http.StatusBadGateway {
			want = http.StatusServiceUnavailable
		}
		if rec.Code != want || strings.Contains(rec.Body.String(), "must-not-leak") || strings.Contains(rec.Body.String(), "untrusted-provider-code") {
			t.Fatalf("upstream error mapping = %d %s", rec.Code, rec.Body.String())
		}
	}
}

func encryptHTTPTestToken(t *testing.T, server *Server, accountID string) domain.AccountCredential {
	t.Helper()
	encrypted, err := server.cipher.Encrypt([]byte("access-token-" + accountID))
	if err != nil {
		t.Fatal(err)
	}
	return domain.AccountCredential{AccountID: accountID, AccessTokenEncrypted: encrypted, RefreshTokenEncrypted: encrypted, IDTokenEncrypted: encrypted}
}
