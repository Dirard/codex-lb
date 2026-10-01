package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestAccountGroupsEndpointRequiresAdminAndUpdatesAtomically(t *testing.T) {
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
	request := func(method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://localhost:2455"+path, bytes.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("Content-Type", "application/json")
		if authenticated && cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := request(http.MethodPut, "/api/accounts/acct-a/groups", map[string]any{"groupIds": []string{}}, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", rec.Code)
	}
	setup := request(http.MethodPost, "/api/dashboard-auth/password/setup", map[string]string{"password": "synthetic-test-password"}, false)
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) != 1 {
		t.Fatalf("setup status = %d", setup.Code)
	}
	cookie = setup.Result().Cookies()[0]

	now := time.Now().UTC()
	account := domain.Account{ID: "acct-a", Email: "primary@example.invalid", Kind: domain.AccountChatGPT,
		Provider: "openai", Status: domain.AccountActive, PlanType: "plus", RoutingPolicy: "normal", CreatedAt: now}
	other := account
	other.ID, other.Email = "acct-b", "other@example.invalid"
	for _, a := range []domain.Account{account, other} {
		if err := store.SaveAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	groupA := domain.AccountGroup{ID: "group-a", Name: "A", AccountIDs: []string{"acct-a", "acct-b"}, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 100}}}
	groupB := domain.AccountGroup{ID: "group-b", Name: "B", AccountIDs: []string{"acct-b"}}
	for _, group := range []domain.AccountGroup{groupA, groupB} {
		if err := store.SaveGroup(ctx, group, now); err != nil {
			t.Fatal(err)
		}
	}

	rec := request(http.MethodPut, "/api/accounts/acct-a/groups", map[string]any{"groupIds": []string{"group-b"}}, true)
	var response struct {
		AccountID string   `json:"accountId"`
		GroupIDs  []string `json:"groupIds"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.AccountID != account.ID || len(response.GroupIDs) != 1 || response.GroupIDs[0] != groupB.ID {
		t.Fatalf("update status = %d body %s", rec.Code, rec.Body.String())
	}
	assertHTTPGroupAccounts(t, store, groupA.ID, []string{"acct-b"})
	assertHTTPGroupAccounts(t, store, groupB.ID, []string{"acct-a", "acct-b"})
	stored, err := store.GetGroup(ctx, groupA.ID)
	if err != nil || len(stored.Limits) != 1 || stored.Limits[0].MaxValue != 100 {
		t.Fatalf("limits changed: %+v, %v", stored, err)
	}

	for name, body := range map[string]any{
		"missing array": struct{}{},
		"null array":    map[string]any{"groupIds": nil},
		"empty id":      map[string]any{"groupIds": []string{""}},
		"duplicate":     map[string]any{"groupIds": []string{"group-b", "group-b"}},
	} {
		if rec := request(http.MethodPut, "/api/accounts/acct-a/groups", body, true); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status = %d", name, rec.Code)
		}
	}
	if rec := request(http.MethodPut, "/api/accounts/acct-a/groups", map[string]any{"groupIds": []string{"group-a", "missing"}}, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown group status = %d", rec.Code)
	}
	if rec := request(http.MethodPut, "/api/accounts/missing/groups", map[string]any{"groupIds": []string{}}, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account status = %d", rec.Code)
	}
	assertHTTPGroupAccounts(t, store, groupA.ID, []string{"acct-b"})
	assertHTTPGroupAccounts(t, store, groupB.ID, []string{"acct-a", "acct-b"})

	if rec := request(http.MethodPut, "/api/accounts/acct-a/groups", map[string]any{"groupIds": []string{}}, true); rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d", rec.Code)
	}
	assertHTTPGroupAccounts(t, store, groupA.ID, []string{"acct-b"})
	assertHTTPGroupAccounts(t, store, groupB.ID, []string{"acct-b"})
}

func assertHTTPGroupAccounts(t *testing.T, store *sqlite.Store, groupID string, want []string) {
	t.Helper()
	group, err := store.GetGroup(context.Background(), groupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(group.AccountIDs) != len(want) {
		t.Fatalf("group %s accounts = %v, want %v", groupID, group.AccountIDs, want)
	}
	for i, accountID := range want {
		if group.AccountIDs[i] != accountID {
			t.Fatalf("group %s accounts = %v, want %v", groupID, group.AccountIDs, want)
		}
	}
}
