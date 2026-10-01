package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestAffinityAdminRoutesRequireAuthAndPreserveDurableHints(t *testing.T) {
	store, server := localKeyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	account := domain.Account{ID: "affinity-account", Kind: domain.AccountChatGPT, Provider: "openai",
		Email: "synthetic@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	cipher, err := server.cipher.Encrypt([]byte("synthetic-provider-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID,
		AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "affinity-reservation", APIKeyID: domain.LocalProxyKeyID,
		AccountID: account.ID, Model: "gpt-6-sol", Now: now}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []domain.AffinityKind{domain.AffinityPromptCache, domain.AffinityStickyThread, domain.AffinityCodexSession} {
		if changed, err := store.SaveAffinity(ctx, domain.AffinityBinding{Key: string(kind), Kind: kind,
			APIKeyID: domain.LocalProxyKeyID, AccountID: account.ID, UpdatedAt: now.Add(-time.Hour)}, 0, "affinity-reservation"); err != nil || !changed {
			t.Fatalf("seed affinity: %v %v", changed, err)
		}
	}
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
	if got := request("GET", "/api/sticky-sessions", "", ""); got.Code != 401 {
		t.Fatalf("unprotected affinities: %d", got.Code)
	}
	token, err = server.auth.SetupPassword(ctx, "synthetic-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var page domain.AffinityList
	got := request("GET", "/api/sticky-sessions/?staleOnly=true&limit=1", "", "")
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil || page.Total != 1 || page.StalePromptCacheCount != 1 || len(page.Entries) != 1 || page.Entries[0].Kind != domain.AffinityPromptCache {
		t.Fatalf("stale page contract: %d %s", got.Code, got.Body.String())
	}
	if got := request("POST", "/api/sticky-sessions/purge", "{}", "https://attacker.invalid"); got.Code != 403 {
		t.Fatalf("cross-origin affinity mutation allowed: %d", got.Code)
	}
	got = request("POST", "/api/sticky-sessions/purge/", "{}", "http://localhost")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"deletedCount":1`) {
		t.Fatalf("stale purge contract: %d %s", got.Code, got.Body.String())
	}
	got = request("GET", "/api/sticky-sessions", "", "")
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil || page.Total != 2 || page.StalePromptCacheCount != 0 {
		t.Fatalf("purge deleted durable hints: %d %s", got.Code, got.Body.String())
	}
	got = request("POST", "/api/sticky-sessions/delete", `{"sessions":[{"key":"sticky_thread","kind":"sticky_thread"},{"key":"missing","kind":"prompt_cache"}]}`, "http://localhost")
	var deleted domain.AffinityDeleteResult
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &deleted) != nil || deleted.DeletedCount != 1 || len(deleted.Failed) != 1 || deleted.Failed[0].Reason != "not_found" {
		t.Fatalf("partial batch delete contract: %d %s", got.Code, got.Body.String())
	}
	got = request("POST", "/api/sticky-sessions/delete-filtered", `{"keyQuery":"codex_"}`, "http://localhost")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"deletedCount":1`) {
		t.Fatalf("filtered delete contract: %d %s", got.Code, got.Body.String())
	}
	if got := request("DELETE", "/api/sticky-sessions/codex_session/missing", "", "http://localhost"); got.Code != 404 {
		t.Fatalf("missing single delete: %d %s", got.Code, got.Body.String())
	}
}
