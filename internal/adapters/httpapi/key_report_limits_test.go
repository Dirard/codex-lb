package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestKeyReportGroupLimitsAreScopedAndLive(t *testing.T) {
	ctx := context.Background()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	now := time.Now().UTC()
	if err := store.SaveAccount(ctx, domain.Account{ID: "private-account", Kind: domain.AccountChatGPT,
		Provider: "openai", Email: "private@example.test", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, encryptHTTPTestToken(t, server, "private-account")); err != nil {
		t.Fatal(err)
	}
	week, reset := 10080, now.Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "private-account", Window: "secondary",
		UsedPercent: 40, WindowMinutes: &week, ResetAt: &reset, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	balance := 15.5
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "private-account", ObservedAt: now,
		Credits: &domain.AccountCreditStatus{AccountID: "private-account", ObservedAt: now, Balance: &balance}}); err != nil {
		t.Fatal(err)
	}
	groupA, groupB := "group-a", "group-b"
	for _, id := range []string{groupA, groupB} {
		if err := store.SaveGroup(ctx, domain.AccountGroup{ID: id, Name: id, AccountIDs: []string{"private-account"},
			Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 1000}}}, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"caller", "peer", "disabled", "expired", "deleted", "other-group", "ungrouped"} {
		key := domain.APIKey{ID: id, Name: "name-" + id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-"+id))),
			KeyPrefix: "private-prefix-" + id, IsActive: true, GroupID: &groupA, UsageSections: "account_pool_usage"}
		if id == "other-group" {
			key.GroupID = &groupB
		} else if id == "ungrouped" {
			key.GroupID = nil
		}
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
		if id == "caller" || id == "peer" {
			amount := int64(250)
			if id == "peer" {
				amount = 720
			}
			if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reserved-" + id, APIKeyID: id, AccountID: "private-account",
				Model: "gpt-6-luna", Budget: domain.UsageAmount{InputTokens: amount}, Now: now}); err != nil {
				t.Fatal(err)
			}
		}
		if id == "disabled" || id == "expired" {
			key.IsActive = id != "disabled"
			if id == "expired" {
				past := now.Add(-time.Hour)
				key.ExpiresAt = &past
			}
			if err := store.SaveAPIKey(ctx, key, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.DeleteAPIKey(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	// Change membership/auth after bearer lookup to exercise the actual snapshot boundary.
	wrapped := &keyReportChangingStore{KeyUsageStore: store}
	public := http.NewServeMux()
	NewKeyUsageHandler(wrapped).RegisterPublicRoutes(public)
	handler := server.Handler(public, nil)
	read := func(path string) (domain.KeyReportsResponse, *httptest.ResponseRecorder) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.Header.Set("Authorization", "Bearer synthetic-caller")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var report domain.KeyReportsResponse
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &report) != nil {
			t.Fatal("invalid report response")
		}
		return report, rec
	}
	for _, path := range []string{"/v1/usage/reports", "/v1/usage/reports/"} {
		report, rec := read(path)
		if rec.Code != 200 || report.Group == nil || report.Group.Name != groupA || len(report.Group.Keys) != 4 || len(report.Limits) != 1 || report.Limits[0].CurrentValue != 250 {
			t.Fatalf("own/group limits: status=%d report=%+v", rec.Code, report.KeyReportLimitSummary)
		}
		if quota := report.Group.AccountQuota; quota == nil || quota.AccountCount != 1 || len(quota.Windows) != 1 || quota.Windows[0].UsedPercent != 40 ||
			quota.PurchasedCredits == nil || *quota.PurchasedCredits != 15.5 || quota.CreditsKnownAccountCount != 1 {
			t.Fatalf("group account aggregate missing in HTTP report: %+v", quota)
		}
		for _, key := range report.Group.Keys {
			if key.ID == "peer" && (key.Limits[0].CurrentValue != 720 || key.IsCurrent) {
				t.Fatal("peer did not retain its separate counter")
			}
			if key.ID == "caller" && !key.IsCurrent || key.ID == "disabled" && key.IsActive || key.ID == "expired" && key.ExpiresAt == nil {
				t.Fatalf("invalid member status: %+v", key)
			}
		}
		for _, forbidden := range []string{"name-other-group", "name-ungrouped", "name-deleted", "private-prefix", "private-account", "private@example.test", "synthetic-", "keyHash", "keyPrefix", "assignedAccount", "pooled"} {
			if strings.Contains(rec.Body.String(), forbidden) {
				t.Fatalf("private field exposed: %s", forbidden)
			}
		}
	}
	for _, filter := range []string{"group_id=group-b", "groupId=group-b", "api_key_id=peer"} {
		if _, rec := read("/v1/usage/reports?" + filter); rec.Code != 400 {
			t.Fatal("accepted caller-selected report scope")
		}
	}
	// Changing group rules updates each member's maximum, not its consumption.
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: groupA, Name: groupA, AccountIDs: []string{"private-account"},
		Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 2000}}}, now); err != nil {
		t.Fatal(err)
	}
	if report, _ := read("/v1/usage/reports?model=absent-model"); report.Limits[0].MaxValue != 2000 || report.Limits[0].CurrentValue != 250 {
		t.Fatal("report filter or group edit changed personal consumption")
	}
	wrapped.change = func(key domain.APIKey) {
		key.GroupID = &groupB
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	if report, rec := read("/v1/usage/reports"); rec.Code != 200 || report.Group == nil || report.Group.Name != groupB || len(report.Group.Keys) != 2 || strings.Contains(rec.Body.String(), "name-peer") {
		t.Fatal("stale authenticated membership revealed former group")
	}
	wrapped.change = func(key domain.APIKey) {
		key.GroupID = nil
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	if report, rec := read("/v1/usage/reports"); rec.Code != 200 || report.Group != nil {
		t.Fatal("detached key retained group report access")
	}
	wrapped.change = func(key domain.APIKey) {
		key.IsActive = false
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, rec := read("/v1/usage/reports"); rec.Code != 401 {
		t.Fatal("key revoked after authentication still read limits")
	}
}

type keyReportChangingStore struct {
	KeyUsageStore
	change func(domain.APIKey)
}

func (s *keyReportChangingStore) FindAPIKeyByHash(ctx context.Context, hash string) (domain.APIKey, error) {
	key, err := s.KeyUsageStore.FindAPIKeyByHash(ctx, hash)
	if err == nil && s.change != nil {
		change := s.change
		s.change = nil
		change(key)
	}
	return key, err
}
