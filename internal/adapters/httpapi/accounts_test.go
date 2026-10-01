package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
)

type httpStubOAuth struct {
	state       string
	challenge   string
	tokens      application.OAuthTokens
	exchanges   int
	device      application.DeviceCode
	deviceReady bool
}

func (s *httpStubOAuth) AuthorizationURL(state, challenge string) string {
	s.state, s.challenge = state, challenge
	return "https://auth.example.test/authorize?state=" + state
}

func (s *httpStubOAuth) ExchangeCode(context.Context, string, string) (application.OAuthTokens, error) {
	s.exchanges++
	return s.tokens, nil
}

func (s *httpStubOAuth) RequestDeviceCode(context.Context) (application.DeviceCode, error) {
	return s.device, nil
}

func (s *httpStubOAuth) ExchangeDeviceToken(context.Context, string, string) (application.OAuthTokens, bool, error) {
	return s.tokens, s.deviceReady, nil
}

func testJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func stubTokenSet(sub string) application.OAuthTokens {
	return application.OAuthTokens{
		AccessToken:  testJWT(map[string]any{"sub": sub, "exp": 1900000000}),
		RefreshToken: "refresh-" + sub,
		IDToken: testJWT(map[string]any{
			"email": sub + "@example.test", "sub": sub,
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "chatgpt-http", "user_id": sub,
				"workspace_id": "org-http", "chatgpt_plan_type": "Pro",
			},
		}),
	}
}

func newAccountsTestServer(t *testing.T, stub *httpStubOAuth) (*Server, *sqlite.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	server.setAccountsService(application.NewAccountsService(store, store, vault, stub, time.Now))
	auth := http.NewServeMux()
	server.registerAuth(auth)
	admin := http.NewServeMux()
	server.registerAccountRoutes(admin)
	auth.Handle("/api/", server.requireAdmin(admin))
	return server, store, auth
}

func TestAccountRoutesRequireAdminSession(t *testing.T) {
	_, _, handler := newAccountsTestServer(t, &httpStubOAuth{})
	for _, target := range []string{
		"/api/accounts/a/pause", "/api/accounts/a/export/auth", "/api/accounts/import", "/api/oauth/start",
	} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:2455"+target, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s unauthenticated status = %d", target, rec.Code)
		}
	}
}

func TestAccountRoutesImportCRUDAndExportAuth(t *testing.T) {
	stub := &httpStubOAuth{tokens: stubTokenSet("user-http")}
	_, store, handler := newAccountsTestServer(t, stub)

	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup status = %d body %s", setupRec.Code, setupRec.Body.String())
	}
	cookie := setupRec.Result().Cookies()[0]
	request := func(method, target, contentType string, body []byte) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, "http://localhost:2455"+target, reader)
		req.RemoteAddr = "127.0.0.1:5678"
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// File selection and paste use this same multipart auth_json boundary.
	importJSON := func(raw []byte) *httptest.ResponseRecorder {
		var multipartBody bytes.Buffer
		writer := multipart.NewWriter(&multipartBody)
		part, err := writer.CreateFormFile("auth_json", "auth.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return request(http.MethodPost, "/api/accounts/import", writer.FormDataContentType(), multipartBody.Bytes())
	}
	authJSON, _ := json.Marshal(map[string]any{"tokens": map[string]any{
		"id_token": stub.tokens.IDToken, "access_token": stub.tokens.AccessToken, "refresh_token": stub.tokens.RefreshToken,
		"account_id": "selected-chatgpt-http",
	}})
	imported := importJSON(authJSON)
	var importResult application.AccountImportResult
	if imported.Code != http.StatusOK || json.Unmarshal(imported.Body.Bytes(), &importResult) != nil {
		t.Fatalf("import status = %d body %s", imported.Code, imported.Body.String())
	}
	if importResult.AccountID == "" || importResult.Status != "active" {
		t.Fatalf("import result = %+v", importResult)
	}
	credential, err := store.GetAccountCredential(context.Background(), importResult.AccountID)
	if err != nil || string(credential.AccessTokenEncrypted) == stub.tokens.AccessToken {
		t.Fatalf("credential stored insecurely: %+v err %v", credential, err)
	}
	accountID := importResult.AccountID
	account, err := store.GetAccount(context.Background(), accountID)
	if err != nil || account.ChatGPTAccountID != "selected-chatgpt-http" {
		t.Fatal("snake_case account_id was not preserved")
	}
	invalidAuth, _ := json.Marshal(map[string]any{"tokens": map[string]any{
		"id_token": stub.tokens.IDToken, "access_token": stub.tokens.AccessToken, "refresh_token": stub.tokens.RefreshToken,
		"accessToken": "synthetic-secret-conflict",
	}})
	if rejected := importJSON(invalidAuth); rejected.Code != http.StatusBadRequest ||
		!strings.Contains(rejected.Body.String(), "invalid_auth_json") || strings.Contains(rejected.Body.String(), "synthetic-secret-conflict") {
		t.Fatal("conflicting aliases did not return a safe invalid-auth response")
	}

	// CRUD.
	if rec := request(http.MethodPost, "/api/accounts/"+accountID+"/pause", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d", rec.Code)
	}
	if rec := request(http.MethodPost, "/api/accounts/"+accountID+"/reactivate", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("reactivate status = %d", rec.Code)
	}
	if rec := request(http.MethodPut, "/api/accounts/"+accountID+"/alias", "application/json", []byte(`{"alias":"  Primary  "}`)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"alias":"Primary"`) {
		t.Fatalf("alias = %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/api/accounts/"+accountID+"/limit-warmup", "application/json", []byte(`{"enabled":true}`)); rec.Code != http.StatusOK {
		t.Fatalf("limit warmup = %d", rec.Code)
	}
	if rec := request(http.MethodPut, "/api/accounts/"+accountID+"/routing-policy", "application/json", []byte(`{"routingPolicy":"preserve"}`)); rec.Code != http.StatusOK {
		t.Fatalf("routing policy = %d", rec.Code)
	}
	if rec := request(http.MethodPut, "/api/accounts/"+accountID+"/routing-policy", "application/json", []byte(`{"routingPolicy":"nope"}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid routing policy = %d", rec.Code)
	}
	if rec := request(http.MethodPatch, "/api/accounts/"+accountID, "application/json", []byte(`{"securityWorkAuthorized":true}`)); rec.Code != http.StatusOK {
		t.Fatalf("patch account = %d", rec.Code)
	}
	if rec := request(http.MethodPost, "/api/accounts/missing/pause", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing pause = %d", rec.Code)
	}

	// Authorized export with no-store.
	export := request(http.MethodPost, "/api/accounts/"+accountID+"/export/auth", "", nil)
	if export.Code != http.StatusOK {
		t.Fatalf("export status = %d body %s", export.Code, export.Body.String())
	}
	if cache := export.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("export cache-control = %q", cache)
	}
	var exportBody struct {
		Filename string `json:"filename"`
		Tokens   struct {
			AccessToken string `json:"accessToken"`
		} `json:"tokens"`
		CodexAuthJSON struct {
			Tokens struct {
				RefreshToken string `json:"refresh_token"`
			} `json:"tokens"`
		} `json:"codexAuthJson"`
	}
	if json.Unmarshal(export.Body.Bytes(), &exportBody) != nil || exportBody.Tokens.AccessToken != stub.tokens.AccessToken || exportBody.CodexAuthJSON.Tokens.RefreshToken != stub.tokens.RefreshToken {
		t.Fatalf("export body = %s", export.Body.String())
	}
	var exportDocument map[string]json.RawMessage
	if err := json.Unmarshal(export.Body.Bytes(), &exportDocument); err != nil {
		t.Fatal(err)
	}
	reimported := importJSON(exportDocument["codexAuthJson"])
	var reimportResult application.AccountImportResult
	if reimported.Code != http.StatusOK || json.Unmarshal(reimported.Body.Bytes(), &reimportResult) != nil || reimportResult.AccountID != accountID {
		t.Fatal("exported Codex auth.json did not reimport into the same account")
	}
	accounts, err := store.ListAccounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatal("failed import or export round trip created an extra account")
	}

	// Delete hides the account from admin operations.
	if rec := request(http.MethodDelete, "/api/accounts/"+accountID+"?delete_history=false", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := request(http.MethodPost, "/api/accounts/"+accountID+"/pause", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted pause = %d", rec.Code)
	}
}

func TestOAuthRoutesBrowserFlowWithManualCallback(t *testing.T) {
	stub := &httpStubOAuth{tokens: stubTokenSet("user-oauth")}
	_, _, handler := newAccountsTestServer(t, stub)
	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
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

	start := request(http.MethodPost, "/api/oauth/start", []byte(`{"forceMethod":"browser"}`))
	var startBody struct {
		FlowID           *string `json:"flowId"`
		Method           string  `json:"method"`
		AuthorizationURL *string `json:"authorizationUrl"`
		CallbackURL      *string `json:"callbackUrl"`
		VerificationURL  *string `json:"verificationUrl"`
		UserCode         *string `json:"userCode"`
		DeviceAuthID     *string `json:"deviceAuthId"`
		IntervalSeconds  *int    `json:"intervalSeconds"`
		ExpiresInSeconds *int    `json:"expiresInSeconds"`
	}
	if start.Code != http.StatusOK || json.Unmarshal(start.Body.Bytes(), &startBody) != nil || startBody.FlowID == nil || startBody.AuthorizationURL == nil || startBody.CallbackURL == nil || startBody.VerificationURL != nil || startBody.UserCode != nil || startBody.ExpiresInSeconds == nil {
		t.Fatalf("start = %d %s", start.Code, start.Body.String())
	}
	status := request(http.MethodGet, "/api/oauth/status?flowId="+*startBody.FlowID, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"pending"`) || !strings.Contains(status.Body.String(), `"errorMessage":null`) {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	callback := request(http.MethodPost, "/api/oauth/manual-callback", []byte(`{"callbackUrl":"http://localhost:1455/auth/callback?code=abc&state=`+stub.state+`"}`))
	if callback.Code != http.StatusOK || !strings.Contains(callback.Body.String(), `"status":"success"`) {
		t.Fatalf("manual callback = %d %s", callback.Code, callback.Body.String())
	}
	complete := request(http.MethodPost, "/api/oauth/complete", []byte(`{"flowId":"`+*startBody.FlowID+`"}`))
	if complete.Code != http.StatusOK || !strings.Contains(complete.Body.String(), `"status":"success"`) {
		t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
	}
}
