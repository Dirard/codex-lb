package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestStartWithExistingAccountsReturnsBrowserWithoutFlow(t *testing.T) {
	ctx := context.Background()
	service, store, _, _ := newTestService(t, true)
	if err := store.SaveAccount(ctx, domain.Account{ID: "acct_1", Kind: domain.AccountChatGPT, Provider: "openai", Email: "a@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	result, err := service.StartOAuth(ctx, "", "")
	if err != nil || result.Method != "browser" || result.FlowID != nil {
		t.Fatalf("start with accounts = %+v err %v", result, err)
	}
	if status := service.OAuthStatus(ctx, ""); status.Status != "success" {
		t.Fatalf("flowless status = %+v", status)
	}
}

func TestBrowserFlowManualCallbackLifecycle(t *testing.T) {
	ctx := context.Background()
	service, store, stub, _ := newTestService(t, true)
	stub.codeTokens = tokenSet("user-9")
	start, err := service.StartOAuth(ctx, "browser", "")
	if err != nil {
		t.Fatal(err)
	}
	if start.FlowID == nil || start.AuthorizationURL == nil || !strings.Contains(*start.AuthorizationURL, stub.lastState) {
		t.Fatalf("start = %+v stub state %q", start, stub.lastState)
	}
	if service.OAuthStatus(ctx, *start.FlowID).Status != "pending" {
		t.Fatal("new browser flow is not pending")
	}
	wrongState := service.ManualCallback(ctx, "http://localhost:1455/auth/callback?code=x&state=wrong", "")
	if wrongState.Status != "error" {
		t.Fatalf("wrong state = %+v", wrongState)
	}
	if service.OAuthStatus(ctx, *start.FlowID).Status != "pending" {
		t.Fatal("unknown state failed an unrelated flow")
	}

	stub.codeErr = nil
	start, err = service.StartOAuth(ctx, "browser", "")
	if err != nil {
		t.Fatal(err)
	}
	callbackURL := "http://localhost:1455/auth/callback?code=abc&state=" + stub.lastState
	result := service.ManualCallback(ctx, callbackURL, "")
	if result.Status != "success" {
		t.Fatalf("manual callback = %+v", result)
	}
	if stub.exchanges != 1 {
		t.Fatalf("exchanges = %d", stub.exchanges)
	}
	account, err := store.GetAccount(ctx, "chatgpt-1_"+hashPrefix("org-1", 8))
	if err != nil || account.Email != "user-9@example.test" {
		t.Fatalf("oauth account = %+v err %v", account, err)
	}
	if _, err := store.GetAccountCredential(ctx, account.ID); err != nil {
		t.Fatalf("credential missing: %v", err)
	}
	replay := service.ManualCallback(ctx, callbackURL, "")
	if replay.Status != "success" || stub.exchanges != 1 {
		t.Fatalf("replay = %+v exchanges %d", replay, stub.exchanges)
	}
}

func TestBrowserFlowExpiresByTTL(t *testing.T) {
	ctx := context.Background()
	service, _, stub, now := newTestService(t, true)
	stub.codeTokens = tokenSet("user-9")
	start, err := service.StartOAuth(ctx, "browser", "")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(browserFlowTTL + time.Second)
	if status := service.OAuthStatus(ctx, *start.FlowID); status.Status != "error" {
		t.Fatalf("expired status = %+v", status)
	}
	expired := service.ManualCallback(ctx, "http://localhost:1455/auth/callback?code=abc&state="+stub.lastState, "")
	if expired.Status != "error" || stub.exchanges != 0 {
		t.Fatalf("expired callback = %+v exchanges %d", expired, stub.exchanges)
	}
}

func TestReauthSeatVerification(t *testing.T) {
	ctx := context.Background()
	service, store, stub, _ := newTestService(t, true)
	original := tokenSet("user-1")
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]any{"idToken": original.IDToken, "accessToken": original.AccessToken, "refreshToken": original.RefreshToken}})
	imported, err := service.ImportAccount(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetAccountCredential(ctx, imported.AccountID)

	stub.codeTokens = tokenSet("user-2")
	start, err := service.StartOAuth(ctx, "browser", imported.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := service.ManualCallback(ctx, "http://localhost:1455/auth/callback?code=abc&state="+stub.lastState, *start.FlowID)
	if mismatch.Status != "error" || mismatch.ErrorMessage == nil || *mismatch.ErrorMessage != seatMismatchMessage {
		t.Fatalf("mismatch = %+v", mismatch)
	}
	after, _ := store.GetAccountCredential(ctx, imported.AccountID)
	if string(after.AccessTokenEncrypted) != string(before.AccessTokenEncrypted) {
		t.Fatal("seat mismatch overwrote tokens")
	}

	stub.codeTokens = tokenSet("user-1")
	start, err = service.StartOAuth(ctx, "browser", imported.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	match := service.ManualCallback(ctx, "http://localhost:1455/auth/callback?code=abc&state="+stub.lastState, *start.FlowID)
	if match.Status != "success" {
		t.Fatalf("seat match = %+v", match)
	}
	saved, _ := store.GetAccount(ctx, imported.AccountID)
	if saved.ChatGPTUserID != "user-1" || saved.CreatedAt.IsZero() {
		t.Fatalf("reauth saved account = %+v", saved)
	}
	if complete := service.CompleteOAuth(ctx, *start.FlowID, "", ""); complete.Status != "success" {
		t.Fatalf("complete = %+v", complete)
	}
}

func TestDeviceFlowPollsUntilTokensArrive(t *testing.T) {
	ctx := context.Background()
	service, store, stub, _ := newTestService(t, true)
	stub.device = DeviceCode{VerificationURL: "https://auth.example.test/codex/device", UserCode: "ABCD", DeviceAuthID: "device-1", IntervalSeconds: 0, ExpiresInSeconds: 300}
	stub.deviceTokens = tokenSet("user-7")
	stub.deviceReady = true
	start, err := service.StartOAuth(ctx, "device", "")
	if err != nil {
		t.Fatal(err)
	}
	if start.Method != "device" || start.DeviceAuthID == nil || *start.DeviceAuthID != "device-1" || start.UserCode == nil || *start.UserCode != "ABCD" {
		t.Fatalf("device start = %+v", start)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.OAuthStatus(ctx, *start.FlowID).Status != "success" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if status := service.OAuthStatus(ctx, *start.FlowID); status.Status != "success" {
		t.Fatalf("device status = %+v calls %d", status, stub.deviceCalls)
	}
	if _, err := store.GetAccountCredential(ctx, "chatgpt-1_"+hashPrefix("org-1", 8)); err != nil {
		t.Fatalf("device credential missing: %v", err)
	}
}

func TestDeviceFlowReportsPendingWhilePolling(t *testing.T) {
	ctx := context.Background()
	service, _, stub, _ := newTestService(t, true)
	stub.device = DeviceCode{VerificationURL: "https://auth.example.test/codex/device", UserCode: "ABCD", DeviceAuthID: "device-1", IntervalSeconds: 60, ExpiresInSeconds: 300}
	start, err := service.StartOAuth(ctx, "device", "")
	if err != nil {
		t.Fatal(err)
	}
	if complete := service.CompleteOAuth(ctx, *start.FlowID, "", ""); complete.Status != "pending" {
		t.Fatalf("pending complete = %+v", complete)
	}
	_ = stub
}

func TestOAuthStartSurfacesUpstreamError(t *testing.T) {
	ctx := context.Background()
	service, _, stub, _ := newTestService(t, true)
	stub.deviceErr = &OAuthError{Code: "device_auth_unavailable", Message: "Device code login is not enabled"}
	_, err := service.StartOAuth(ctx, "device", "")
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "device_auth_unavailable" {
		t.Fatalf("device start err = %v", err)
	}
}
