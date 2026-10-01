package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func TestDashboardSecurityAndGroupKeyFlow(t *testing.T) {
	ctx := context.Background()
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
	handler := httpapi.New(store, vault, httpapi.Config{}, nil).Handler(nil, nil)
	var cookie *http.Cookie
	request := func(method, path string, body any, mutate func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://localhost:2455"+path, bytes.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:5678"
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if mutate != nil {
			mutate(r)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/accounts", nil, nil).Code; got != 401 {
		t.Fatalf("unauthenticated accounts status %d", got)
	}
	setup := map[string]string{"password": "synthetic-test-password"}
	if got := request("POST", "/api/dashboard-auth/password/setup", setup, func(r *http.Request) {
		r.RemoteAddr = "192.0.2.1:8888"
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
	}).Code; got != 403 {
		t.Fatalf("remote bootstrap accepted (%d)", got)
	}
	if got := request("POST", "/api/dashboard-auth/password/setup", setup, func(r *http.Request) {
		r.Header.Set("Origin", "https://attacker.invalid")
	}).Code; got != 403 {
		t.Fatalf("cross-origin setup accepted (%d)", got)
	}
	result := request("POST", "/api/dashboard-auth/password/setup", setup, nil)
	if result.Code != 200 {
		t.Fatalf("setup status %d", result.Code)
	}
	cookies := result.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite == http.SameSiteNoneMode {
		t.Fatal("insecure session cookie")
	}
	cookie = cookies[0]
	if got := request("GET", "/api/accounts", nil, nil).Code; got != 200 {
		t.Fatalf("authenticated accounts status %d", got)
	}
	account := domain.Account{ID: "acct_test", Email: "synthetic@example.invalid", Kind: domain.AccountChatGPT, Provider: "openai", Status: domain.AccountActive, PlanType: "plus", RoutingPolicy: "normal", CreatedAt: time.Now()}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt([]byte("synthetic-not-a-real-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: encrypted, RefreshTokenEncrypted: encrypted, IDTokenEncrypted: encrypted}); err != nil {
		t.Fatal(err)
	}
	groupPayload := map[string]any{"name": "test", "accountIds": []string{account.ID}, "limits": []map[string]any{{"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 1000}}}
	groupResponse := request("POST", "/api/account-groups/", groupPayload, nil)
	var group domain.AccountGroup
	if groupResponse.Code != 200 || json.Unmarshal(groupResponse.Body.Bytes(), &group) != nil || group.ID == "" {
		t.Fatalf("create group status %d", groupResponse.Code)
	}
	keyResponse := request("POST", "/api/api-keys", map[string]any{"name": "test-key", "groupId": group.ID}, nil)
	var key struct {
		domain.APIKey
		Secret string `json:"key"`
	}
	if keyResponse.Code != 200 || json.Unmarshal(keyResponse.Body.Bytes(), &key) != nil || key.Secret == "" {
		t.Fatalf("create key status %d", keyResponse.Code)
	}
	if len(key.Limits) != 1 || key.Limits[0].MaxValue != 1000 {
		t.Fatal("key did not inherit group limits")
	}
	if eligible, err := store.EligibleAccounts(ctx, key.ID); err != nil || len(eligible) != 1 {
		t.Fatal("group key cannot access its configured account")
	}
	list := request("GET", "/api/api-keys/", nil, nil)
	if list.Code != 200 || bytes.Contains(list.Body.Bytes(), []byte(key.Secret)) || bytes.Contains(list.Body.Bytes(), []byte("keyHash")) {
		t.Fatal("key listing failed or exposed a secret")
	}
	groupPayload["accountIds"] = []string{}
	if got := request("PUT", "/api/account-groups/"+group.ID, groupPayload, nil).Code; got != 200 {
		t.Fatalf("empty group status %d", got)
	}
	if got := request("PATCH", "/api/api-keys/"+key.ID, map[string]string{"name": "renamed"}, nil).Code; got != 200 {
		t.Fatalf("rename key status %d", got)
	}
	eligible, err := store.EligibleAccounts(ctx, key.ID)
	if !errors.Is(err, domain.ErrNoAccounts) || len(eligible) != 0 {
		t.Fatal("renaming grouped key expanded its empty scope")
	}
	settingsResponse := request("GET", "/api/settings", nil, nil)
	if settingsResponse.Code != 200 || bytes.Contains(settingsResponse.Body.Bytes(), []byte("passwordHash")) {
		t.Fatal("settings failed or exposed secrets")
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := request("PUT", "/api/settings", map[string]any{"expectedVersion": settings.Version, "dashboardSessionTtlSeconds": 3600}, nil).Code; got != 200 {
		t.Fatalf("settings update status %d", got)
	}
	if got := request("PUT", "/api/settings", map[string]any{"expectedVersion": settings.Version, "dashboardSessionTtlSeconds": 7200}, nil).Code; got != 409 {
		t.Fatalf("stale settings overwrite status %d", got)
	}
}

func TestRuntimeSettingsAPIValidationAndRestart(t *testing.T) {
	t.Setenv("CODEX_LB_OPENAI_CACHE_AFFINITY_MAX_AGE_SECONDS", "1800")
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.sqlite")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(store, vault, httpapi.Config{}, nil).Handler(nil, nil)
	var cookie *http.Cookie
	request := func(method, endpoint string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://localhost:2455"+endpoint, bytes.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	setup := request(http.MethodPost, "/api/dashboard-auth/password/setup", map[string]string{"password": "synthetic-test-password"})
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) != 1 {
		t.Fatalf("admin setup = %d", setup.Code)
	}
	cookie = setup.Result().Cookies()[0]
	initial := request(http.MethodGet, "/api/settings", nil)
	var loaded domain.RuntimeSettings
	if initial.Code != http.StatusOK || json.Unmarshal(initial.Body.Bytes(), &loaded) != nil ||
		loaded.RelativeAvailabilityPower != 2 || loaded.RelativeAvailabilityTopK != 5 ||
		loaded.WarmupModel != "gpt-5.4-mini" || loaded.CodexClientVersion != "0.156.0" ||
		loaded.OpenAICacheAffinityMaxAgeSeconds != 1800 || loaded.StickyReallocationPrimaryBudgetThresholdPct != 95 || loaded.StickyReallocationSecondaryBudgetThresholdPct != 100 ||
		loaded.WeeklyPaceSmoothingMinutes != 30 || loaded.WeeklyPaceWorkingDays != "0,1,2,3,4,5,6" ||
		!loaded.ShowResetCreditBadges || !loaded.ShowResetCreditExpiryBadge {
		t.Fatalf("settings defaults = %d %s", initial.Code, initial.Body.String())
	}
	saved := request(http.MethodPut, "/api/settings", map[string]any{
		"expectedVersion": loaded.Version, "relativeAvailabilityPower": 1.5, "relativeAvailabilityTopK": 4,
		"showResetCreditBadges": false, "showResetCreditExpiryBadge": false,
		"warmupModel":                      " gpt-6-sol ",
		"codexClientVersion":               " 0.157.0-beta.1 ",
		"openaiCacheAffinityMaxAgeSeconds": 600, "stickyReallocationBudgetThresholdPct": 70,
		"stickyReallocationPrimaryBudgetThresholdPct": 80, "stickyReallocationSecondaryBudgetThresholdPct": 90,
		"weeklyPaceWorkingDays": "4, 0,4,2", "weeklyPaceSmoothingMinutes": 120,
	})
	var updated domain.RuntimeSettings
	if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &updated) != nil ||
		updated.RelativeAvailabilityPower != 1.5 || updated.RelativeAvailabilityTopK != 4 ||
		updated.WarmupModel != "gpt-6-sol" || updated.CodexClientVersion != "0.157.0-beta.1" ||
		updated.OpenAICacheAffinityMaxAgeSeconds != 600 || updated.StickyReallocationPrimaryBudgetThresholdPct != 80 || updated.StickyReallocationSecondaryBudgetThresholdPct != 90 ||
		updated.WeeklyPaceSmoothingMinutes != 120 || updated.WeeklyPaceWorkingDays != "0,2,4" ||
		updated.ShowResetCreditBadges || updated.ShowResetCreditExpiryBadge || updated.Version != loaded.Version+1 {
		t.Fatalf("settings update = %d %s", saved.Code, saved.Body.String())
	}
	for _, invalid := range []map[string]any{
		{"expectedVersion": updated.Version, "relativeAvailabilityPower": 0},
		{"expectedVersion": updated.Version, "relativeAvailabilityPower": -1},
		{"expectedVersion": updated.Version, "relativeAvailabilityTopK": 0},
		{"expectedVersion": updated.Version, "relativeAvailabilityTopK": 21},
		{"expectedVersion": updated.Version, "warmupModel": "  "},
		{"expectedVersion": updated.Version, "codexClientVersion": ""},
		{"expectedVersion": updated.Version, "codexClientVersion": nil},
		{"expectedVersion": updated.Version, "codexClientVersion": "latest"},
		{"expectedVersion": updated.Version, "codexClientVersion": "0.156.0\r\nX-Injected: yes"},
		{"expectedVersion": updated.Version, "openaiCacheAffinityMaxAgeSeconds": 0},
		{"expectedVersion": updated.Version, "stickyReallocationPrimaryBudgetThresholdPct": -1},
		{"expectedVersion": updated.Version, "stickyReallocationSecondaryBudgetThresholdPct": 101},
		{"expectedVersion": updated.Version, "weeklyPaceWorkingDays": ""},
		{"expectedVersion": updated.Version, "weeklyPaceWorkingDays": "7"},
		{"expectedVersion": updated.Version, "weeklyPaceWorkingDays": nil},
		{"expectedVersion": updated.Version, "weeklyPaceSmoothingMinutes": 45},
		{"expectedVersion": updated.Version, "weeklyPaceSmoothingMinutes": nil},
	} {
		if response := request(http.MethodPut, "/api/settings", invalid); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid settings accepted: %d %s", response.Code, response.Body.String())
		}
	}
	if response := request(http.MethodPut, "/api/settings", map[string]any{
		"expectedVersion": loaded.Version, "relativeAvailabilityPower": 9.0, "relativeAvailabilityTopK": 19, "codexClientVersion": "0.158.0",
		"showResetCreditBadges": true,
	}); response.Code != http.StatusConflict {
		t.Fatalf("stale version accepted: %d %s", response.Code, response.Body.String())
	}
	for _, invalid := range []any{156, true, []string{"0.156.0"}} {
		if response := request(http.MethodPut, "/api/settings", map[string]any{"expectedVersion": updated.Version, "codexClientVersion": invalid}); response.Code != http.StatusBadRequest {
			t.Fatalf("non-string version accepted: %d", response.Code)
		}
	}
	if response := request(http.MethodPut, "/api/settings", map[string]any{
		"expectedVersion": updated.Version, "autoRedeemResetCreditsBeforeExpiry": true,
	}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported auto-redeem accepted: %d %s", response.Code, response.Body.String())
	}
	reopened, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.LoadSettings(ctx)
	if err != nil || stored.RelativeAvailabilityPower != 1.5 || stored.RelativeAvailabilityTopK != 4 ||
		stored.WarmupModel != "gpt-6-sol" || stored.CodexClientVersion != "0.157.0-beta.1" ||
		stored.OpenAICacheAffinityMaxAgeSeconds != 600 || stored.StickyReallocationPrimaryBudgetThresholdPct != 80 || stored.StickyReallocationSecondaryBudgetThresholdPct != 90 ||
		stored.WeeklyPaceSmoothingMinutes != 120 || stored.WeeklyPaceWorkingDays != "0,2,4" ||
		stored.ShowResetCreditBadges || stored.ShowResetCreditExpiryBadge || stored.Version != updated.Version {
		t.Fatalf("settings changed after restart/rejections: %+v %v", stored, err)
	}
}

func TestAccountAdmissionSettingsHTTPPatchSemantics(t *testing.T) {
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "6")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "12")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "2")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "40")
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(store, vault, httpapi.Config{}, nil).Handler(nil, nil)
	var cookie *http.Cookie
	request := func(method string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost:2455/api/settings", bytes.NewReader(encoded))
		if method == http.MethodPost {
			r.URL.Path = "/api/dashboard-auth/password/setup"
		}
		r.RemoteAddr = "127.0.0.1:5678"
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	setup := request(http.MethodPost, map[string]string{"password": "synthetic-test-password"})
	if setup.Code != 200 {
		t.Fatalf("admin setup: %d %s", setup.Code, setup.Body.String())
	}
	cookie = setup.Result().Cookies()[0]
	var initial domain.RuntimeSettings
	got := request(http.MethodGet, nil)
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &initial) != nil ||
		initial.ProxyAccountStreamLimit != 12 || initial.ProxyAccountStreamLimitEnvironmentValue != 12 ||
		initial.ProxyAccountStreamLimitOverride == nil || *initial.ProxyAccountStreamLimitOverride != 12 {
		t.Fatalf("effective environment settings missing: %d %s", got.Code, got.Body.String())
	}
	got = request(http.MethodPut, map[string]any{"expectedVersion": initial.Version,
		"proxyAccountResponseCreateLimit": 2, "proxyAccountStreamLimit": 0,
		"proxyAccountStreamRecoveryReserve": 3, "proxyApiKeyFairShareCongestionThresholdPct": 80})
	var updated domain.RuntimeSettings
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &updated) != nil ||
		updated.ProxyAccountResponseCreateLimit != 2 || updated.ProxyAccountStreamLimit != 0 ||
		updated.ProxyAccountStreamLimitOverride == nil || *updated.ProxyAccountStreamLimitOverride != 0 ||
		updated.ProxyAccountStreamRecoveryReserve != 3 || updated.ProxyApiKeyFairShareCongestionThresholdPct != 80 {
		t.Fatalf("explicit zero/overrides not persisted: %d %s", got.Code, got.Body.String())
	}
	got = request(http.MethodPut, map[string]any{"expectedVersion": updated.Version, "proxyApiKeyFairShareCongestionThresholdPct": 70})
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &updated) != nil || updated.ProxyAccountStreamLimit != 0 ||
		updated.ProxyApiKeyFairShareCongestionThresholdPct != 70 {
		t.Fatalf("omitted override was cleared: %d %s", got.Code, got.Body.String())
	}
	got = request(http.MethodPut, map[string]any{"expectedVersion": updated.Version,
		"proxyAccountStreamLimit": nil, "proxyAccountStreamRecoveryReserve": nil})
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &updated) != nil || updated.ProxyAccountStreamLimit != 12 ||
		updated.ProxyAccountStreamRecoveryReserve != 2 || updated.ProxyAccountStreamLimitOverride != nil ||
		updated.ProxyAccountStreamRecoveryReserveOverride != nil {
		t.Fatalf("null did not restore inheritance: %d %s", got.Code, got.Body.String())
	}
	for _, invalid := range []map[string]any{
		{"expectedVersion": updated.Version, "proxyAccountStreamLimit": -1},
		{"expectedVersion": updated.Version, "proxyApiKeyFairShareCongestionThresholdPct": 101},
		{"expectedVersion": updated.Version, "proxyAccountStreamRecoveryReserve": 13},
	} {
		if got := request(http.MethodPut, invalid); got.Code != 400 {
			t.Fatalf("invalid account setting accepted: %d %s", got.Code, got.Body.String())
		}
	}
	if got := request(http.MethodPut, map[string]any{"expectedVersion": initial.Version, "proxyAccountStreamLimit": 1}); got.Code != 409 {
		t.Fatalf("stale account-setting update accepted: %d %s", got.Code, got.Body.String())
	}
}
