package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func TestDashboardStatusMatchesOverviewLastSync(t *testing.T) {
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
	server := New(store, vault, Config{}, nil)
	mux := http.NewServeMux()
	server.registerReportsRoutes(mux, store)
	handler := server.requireAdmin(mux)
	token, err := server.auth.SetupPassword(ctx, "synthetic-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, target any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}

	var empty struct {
		LastSyncAt *time.Time `json:"lastSyncAt"`
	}
	get("/api/dashboard/status", &empty)
	if empty.LastSyncAt != nil {
		t.Fatalf("no quota observations: lastSyncAt = %v, want null", empty.LastSyncAt)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	accounts := []domain.Account{
		{ID: "active", Kind: domain.AccountChatGPT, Status: domain.AccountActive},
		{ID: "external", Kind: domain.AccountExternal, Status: domain.AccountActive, BaseURL: "https://upstream.example.invalid"},
		{ID: "deleted", Kind: domain.AccountChatGPT, Status: domain.AccountDeactivated, DeactivationReason: "deleted"},
		{ID: "paused", Kind: domain.AccountChatGPT, Status: domain.AccountPaused},
	}
	observed := map[string]time.Time{
		"active":   now.Add(-3 * time.Minute),
		"external": now.Add(-10 * time.Second),
		"deleted":  now.Add(-20 * time.Second),
		"paused":   now.Add(-2 * time.Minute),
	}
	for _, account := range accounts {
		account.Provider, account.Email, account.PlanType, account.CreatedAt = "openai", account.ID+"@example.invalid", "plus", now
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary",
			UsedPercent: 10, ObservedAt: observed[account.ID]}); err != nil {
			t.Fatal(err)
		}
	}

	var status struct {
		LastSyncAt *time.Time `json:"lastSyncAt"`
	}
	get("/api/dashboard/status", &status)
	var overview domain.DashboardOverview
	get("/api/dashboard/overview", &overview)
	want := observed["paused"]
	if status.LastSyncAt == nil || !status.LastSyncAt.Equal(want) {
		t.Fatalf("status lastSyncAt = %v, want %v (external and deleted accounts ignored)", status.LastSyncAt, want)
	}
	if overview.LastSyncAt == nil || !overview.LastSyncAt.Equal(*status.LastSyncAt) {
		t.Fatalf("overview lastSyncAt = %v, status = %v; they must agree", overview.LastSyncAt, status.LastSyncAt)
	}
}
