package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestAccountDeletionHTTPPolicyAndHistory(t *testing.T) {
	for _, query := range []string{"", "?delete_history=false", "?delete_history=true"} {
		t.Run(query, func(t *testing.T) {
			ctx := context.Background()
			stub := &httpStubOAuth{tokens: stubTokenSet("deletion")}
			server, store, _ := newAccountsTestServer(t, stub)
			t.Cleanup(func() { _ = server.accounts.Close() })
			handler := server.Handler(nil, nil)
			token := ""
			request := func(method, path, body, origin string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Content-Type", "application/json")
				if token != "" {
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
				}
				if origin != "" {
					r.Header.Set("Origin", origin)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			authJSON, _ := json.Marshal(map[string]any{"tokens": map[string]any{
				"idToken": stub.tokens.IDToken, "accessToken": stub.tokens.AccessToken, "refreshToken": stub.tokens.RefreshToken,
			}})
			imported, err := server.accounts.ImportAccount(ctx, authJSON)
			if err != nil {
				t.Fatal(err)
			}
			accountID := imported.AccountID
			path := "/api/accounts/" + accountID
			if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "deletion-history", AccountID: accountID,
				Model: "synthetic", Status: "success", RequestedAt: time.Now(), Usage: domain.UsageAmount{InputTokens: 12}}); err != nil {
				t.Fatal(err)
			}
			if got := request("DELETE", path+query, "", ""); got.Code != 401 {
				t.Fatalf("unauthenticated deletion status: %d", got.Code)
			}
			token, err = server.auth.SetupPassword(ctx, "synthetic-test-password", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if got := request("DELETE", path+query, "", "https://attacker.invalid"); got.Code != 403 {
				t.Fatalf("cross-origin deletion status: %d", got.Code)
			}
			for _, malformed := range []string{"?delete_history=invalid", "?delete_history=", "?delete_history=true&delete_history=false", "?delete_history=%xx"} {
				if got := request("DELETE", path+malformed, "", "http://localhost"); got.Code != 400 {
					t.Fatalf("invalid flag %q status: %d", malformed, got.Code)
				}
			}
			if _, err := store.GetAccountCredential(ctx, accountID); err != nil {
				t.Fatalf("refused request deleted credentials: %v", err)
			}
			oldLogin := request("POST", "/api/oauth/start", `{"forceMethod":"browser"}`, "http://localhost")
			if oldLogin.Code != 200 {
				t.Fatalf("start old login: %d", oldLogin.Code)
			}
			oldCallback := "http://localhost:1455/auth/callback?code=synthetic&state=" + stub.state
			if got := request("DELETE", path+query, "", "http://localhost"); got.Code != 200 {
				t.Fatalf("delete status: %d %s", got.Code, got.Body.String())
			}
			if got := request("DELETE", path+"?delete_history=true", "", "http://localhost"); got.Code != 200 {
				t.Fatalf("repeat delete status: %d", got.Code)
			}
			if _, err := store.GetAccountCredential(ctx, accountID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("deleted credentials available: %v", err)
			}
			if got := request("POST", path+"/export/auth", "", "http://localhost"); got.Code != 404 {
				t.Fatalf("deleted export status: %d", got.Code)
			}
			if got := request("GET", "/api/accounts", "", ""); got.Code != 200 || strings.Contains(got.Body.String(), accountID) {
				t.Fatalf("deleted account still displayed: %d %s", got.Code, got.Body.String())
			}
			wantCount := int64(1)
			if query == "?delete_history=true" {
				wantCount = 0
			}
			totals, err := store.UsageTotals(ctx, "", "")
			if err != nil || totals.RequestCount != wantCount {
				t.Fatalf("delete_history policy not applied: %+v %v", totals, err)
			}
			got := request("POST", "/api/oauth/start", "{}", "http://localhost")
			var flow struct {
				FlowID *string `json:"flowId"`
			}
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &flow) != nil || flow.FlowID == nil {
				t.Fatalf("deleted tombstone blocked new OAuth: %d %s", got.Code, got.Body.String())
			}
			freshJSON, _ := json.Marshal(map[string]any{"tokens": map[string]any{
				"idToken": stub.tokens.IDToken, "accessToken": "fresh-synthetic-access", "refreshToken": "fresh-synthetic-refresh",
			}})
			if _, err := server.accounts.ImportAccount(ctx, freshJSON); err != nil {
				t.Fatal(err)
			}
			callbackJSON, _ := json.Marshal(map[string]string{"callbackUrl": oldCallback})
			oldReply := request("POST", "/api/oauth/manual-callback", string(callbackJSON), "http://localhost")
			if oldReply.Code != 200 || !strings.Contains(oldReply.Body.String(), `"status":"error"`) {
				t.Fatalf("old login revived deleted incarnation: %d %s", oldReply.Code, oldReply.Body.String())
			}
			credential, err := store.GetAccountCredential(ctx, accountID)
			if err != nil {
				t.Fatal(err)
			}
			access, err := server.cipher.Decrypt(credential.AccessTokenEncrypted)
			if err != nil || string(access) != "fresh-synthetic-access" {
				t.Fatal("old login replaced new credentials")
			}
			totals, err = store.UsageTotals(ctx, "", accountID)
			if err != nil || totals.RequestCount != 0 {
				t.Fatalf("reimport inherited deleted history: %+v %v", totals, err)
			}
		})
	}
}
