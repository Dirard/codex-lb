package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestKeyAllAccountsThroughAdminAndResponses(t *testing.T) {
	ctx := context.Background()
	var called []string
	public, store, _ := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		called = append(called, target.Account.ID)
		return wireComplete(fmt.Sprintf("all-account-response-%d", len(called)), emit)
	}))
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []domain.Account{
		{ID: "second-chatgpt", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus"},
		{ID: "external-source", Kind: domain.AccountExternal, Provider: "zai", PlanType: "external"},
	} {
		account.Status, account.CreatedAt = domain.AccountActive, time.Now()
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		copy := credential
		copy.AccountID = account.ID
		if err := store.SaveAccountCredential(ctx, copy); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "admin.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(store, vault, httpapi.Config{}, nil).Handler(public.Config.Handler, nil)
	var cookie *http.Cookie
	request := func(method, path string, body any, key string) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(encoded))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		} else if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	setup := request("POST", "/api/dashboard-auth/password/setup", map[string]string{"password": "synthetic-admin-password"}, "")
	if setup.Code != 200 || len(setup.Result().Cookies()) != 1 {
		t.Fatal("admin setup failed")
	}
	cookie = setup.Result().Cookies()[0]
	for _, explicitEmpty := range []bool{false, true} {
		t.Run(map[bool]string{false: "omitted", true: "explicit empty"}[explicitEmpty], func(t *testing.T) {
			payload := map[string]any{"name": "All key"}
			if explicitEmpty {
				payload["assignedAccountIds"], payload["assignedSourceIds"] = []string{}, []string{}
			}
			created := request("POST", "/api/api-keys/", payload, "")
			var key struct {
				domain.APIKey
				Secret string `json:"key"`
			}
			if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &key) != nil || key.Secret == "" {
				t.Fatal("key creation failed")
			}
			check := func(accounts int, accountScope, sourceScope bool) {
				t.Helper()
				current, err := store.GetAPIKey(ctx, key.ID)
				if err != nil || current.AccountAssignmentScopeEnabled != accountScope || current.SourceAssignmentScopeEnabled != sourceScope {
					t.Fatalf("wrong saved scope: account=%v source=%v err=%v", current.AccountAssignmentScopeEnabled, current.SourceAssignmentScopeEnabled, err)
				}
				eligible, err := store.EligibleAccounts(ctx, key.ID)
				if len(eligible) != accounts || accounts > 0 && err != nil || accounts == 0 && !errors.Is(err, domain.ErrNoAccounts) {
					t.Fatalf("eligible=%d expected=%d err=%v", len(eligible), accounts, err)
				}
			}
			patch := func(body any) {
				t.Helper()
				if w := request("PATCH", "/api/api-keys/"+key.ID, body, ""); w.Code != 200 {
					t.Fatalf("key edit failed: %d", w.Code)
				}
			}
			generate := func(path, wantAccount string, wantStatus int) {
				t.Helper()
				before := len(called)
				w := request("POST", path, map[string]any{"model": "gpt-6-luna", "input": "synthetic", "stream": false}, key.Secret)
				if w.Code != wantStatus {
					t.Fatalf("Responses status=%d want=%d body=%s", w.Code, wantStatus, w.Body.String())
				}
				if wantStatus == 200 {
					if len(called) != before+1 || wantAccount != "" && called[before] != wantAccount {
						t.Fatal("wrong account dispatched")
					}
				} else if len(called) != before {
					t.Fatal("closed scope dispatched upstream")
				}
			}
			check(3, false, false)
			for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
				generate(path, "", 200)
			}
			patch(map[string]any{"assignedAccountIds": []string{"second-chatgpt"}})
			check(1, true, false)
			generate("/v1/responses", "second-chatgpt", 200)
			patch(map[string]any{"assignedAccountIds": []string{}})
			patch(map[string]string{"name": "All renamed"})
			check(3, false, false)
			generate("/backend-api/codex/responses", "", 200)
			patch(map[string]any{"assignedSourceIds": []string{"external-source"}})
			check(3, false, true)
			patch(map[string]any{"assignedSourceIds": []string{}})
			check(3, false, false)
			current, err := store.GetAPIKey(ctx, key.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.AccountAssignmentScopeEnabled = true
			if err := store.SaveAPIKey(ctx, current, time.Now()); err != nil {
				t.Fatal(err)
			}
			patch(map[string]string{"name": "Restricted-empty renamed"})
			check(0, true, false)
			generate("/v1/responses", "", 503)
			group := domain.AccountGroup{ID: "empty-" + key.ID, Name: "Empty group " + key.ID}
			if err := store.SaveGroup(ctx, group, time.Now()); err != nil {
				t.Fatal(err)
			}
			patch(map[string]any{"groupId": group.ID, "assignedAccountIds": []string{}})
			check(0, true, false)
			generate("/backend-api/codex/responses", "", 503)
			patch(map[string]any{"groupId": nil, "assignedAccountIds": []string{}})
			check(3, false, false)
			generate("/backend-api/codex/responses", "", 200)
		})
	}
}
