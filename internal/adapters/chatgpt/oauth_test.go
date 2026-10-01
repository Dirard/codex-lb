package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-lb/internal/application"
)

func TestAuthorizationURLContainsProtocolParameters(t *testing.T) {
	client := NewOAuthClient(OAuthConfig{HTTPClient: http.DefaultClient})
	url := client.AuthorizationURL("state-1", "challenge-1")
	for _, want := range []string{
		"https://auth.openai.com/oauth/authorize?",
		"state=state-1", "code_challenge=challenge-1", "code_challenge_method=S256",
		"client_id=app_EMoamEEZ73f0CkXaXp7hrann", "scope=openid+profile+email+offline_access",
		"originator=codex_chatgpt_desktop",
	} {
		if !strings.Contains(url, want) {
			t.Fatalf("authorization URL missing %q: %s", want, url)
		}
	}
}

func TestExchangeCodePostsFormAndParsesTokens(t *testing.T) {
	var observedPath, observedForm string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedPath = r.URL.Path
		_ = r.ParseForm()
		observedForm = r.PostForm.Encode()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "access-1", "refresh_token": "refresh-1", "id_token": "id-1",
		})
	}))
	defer server.Close()
	client := NewOAuthClient(OAuthConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	tokens, err := client.ExchangeCode(context.Background(), "code-1", "verifier-1")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" || tokens.IDToken != "id-1" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if observedPath != "/oauth/token" {
		t.Fatalf("path = %s", observedPath)
	}
	for _, want := range []string{"grant_type=authorization_code", "code=code-1", "code_verifier=verifier-1"} {
		if !strings.Contains(observedForm, want) {
			t.Fatalf("form missing %q: %s", want, observedForm)
		}
	}
}

func TestExchangeCodeSurfacesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "invalid_grant", "message": "code consumed"}})
	}))
	defer server.Close()
	client := NewOAuthClient(OAuthConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.ExchangeCode(context.Background(), "code-1", "verifier-1")
	var oauthErr *application.OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "invalid_grant" || !strings.Contains(oauthErr.Message, "sign in again") || strings.Contains(oauthErr.Message, "code consumed") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceCodeAndPendingToken(t *testing.T) {
	var userCodeRequest int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			userCodeRequest++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["client_id"] == "" {
				t.Error("device code request missing client_id")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_auth_id": "device-1", "usercode": "ABCD-EFGH", "interval": 1, "expires_in": 900,
			})
		case "/api/accounts/deviceauth/token":
			if userCodeRequest == 0 {
				t.Error("token polled before device code was requested")
			}
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewOAuthClient(OAuthConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	device, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceAuthID != "device-1" || device.UserCode != "ABCD-EFGH" || device.ExpiresInSeconds != 900 || !strings.Contains(device.VerificationURL, "/codex/device") {
		t.Fatalf("device = %+v", device)
	}
	_, ready, err := client.ExchangeDeviceToken(context.Background(), "device-1", "ABCD-EFGH")
	if err != nil || ready {
		t.Fatalf("pending exchange = ready %v err %v", ready, err)
	}
}

func TestDeviceTokenRejectsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "access-only"})
	}))
	defer server.Close()
	client := NewOAuthClient(OAuthConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	if _, _, err := client.ExchangeDeviceToken(context.Background(), "device-1", "ABCD"); err == nil {
		t.Fatal("missing tokens accepted")
	}
}
