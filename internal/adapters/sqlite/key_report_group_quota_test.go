package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestKeyReportGroupQuotaWeightsScopeWindowsAndVisibility(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	groupID := "group-a"
	ids := []string{"plus", "pro", "free", "custom", "missing"}
	for _, id := range append(append([]string{}, ids...), "outside") {
		saveTestAccount(t, store, id)
		account, err := store.GetAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == "pro" || id == "free" || id == "custom" {
			account.PlanType = id
		}
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []domain.AccountGroup{
		{ID: groupID, Name: "A", AccountIDs: ids},
		{ID: "group-b", Name: "B", AccountIDs: []string{"plus", "outside"}},
	} {
		if err := store.SaveGroup(ctx, group, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	key := testKey("caller", &groupID)
	key.UsageSections = "account_pool_usage"
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	reset := fixedTime.Add(time.Hour)
	for _, entry := range []struct {
		id, window string
		used       float64
		minutes    int
	}{
		{"plus", "primary", 100, 300}, {"plus", "secondary", 100, 10080},
		{"pro", "primary", 0, 10080}, // Legacy weekly-only label must not become a 5h window.
		{"free", "monthly", 50, 43200}, {"free", "secondary", 100, 10080},
		{"custom", "secondary", 100, 10080}, {"outside", "secondary", 100, 10080},
	} {
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: entry.id, Window: entry.window, UsedPercent: entry.used,
			WindowMinutes: &entry.minutes, ResetAt: &reset, ObservedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(at time.Time) domain.KeyReportLimitSummary {
		t.Helper()
		snapshot, err := store.KeyReportLimits(ctx, key.ID, at)
		if err != nil || snapshot.Group == nil {
			t.Fatalf("group snapshot: %+v %v", snapshot, err)
		}
		return snapshot
	}
	if empty := read(fixedTime).Group.AccountQuota; empty == nil || empty.PurchasedCredits != nil || empty.CreditsKnownAccountCount != 0 {
		t.Fatal("unknown purchased balances became zero")
	}
	for id, balance := range map[string]float64{"plus": 10.5, "pro": 25.25, "free": 0, "outside": 9999} {
		if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: id, ObservedAt: fixedTime,
			Credits: &domain.AccountCreditStatus{AccountID: id, ObservedAt: fixedTime, Balance: &balance}}); err != nil {
			t.Fatal(err)
		}
	}
	quota := read(fixedTime).Group.AccountQuota
	if quota == nil || quota.AccountCount != 5 || len(quota.Windows) != 3 {
		t.Fatalf("group quota coverage: %+v", quota)
	}
	if quota.PurchasedCredits == nil || *quota.PurchasedCredits != 35.75 || quota.CreditsUnlimited || quota.CreditsKnownAccountCount != 3 {
		t.Fatalf("purchased credits escaped group or counted window duplicates: %+v", quota)
	}
	for _, window := range quota.Windows {
		want, count := 100.0, 1
		switch window.Window {
		case "secondary":
			want, count = 7560.0/(7560+50400)*100, 2
		case "monthly":
			want = 50
		}
		if math.Abs(window.UsedPercent-want) > 1e-9 || window.AccountCount != count {
			t.Fatalf("wrong capacity-weighted quota or duplicate account: %+v", window)
		}
	}
	if quota := read(reset).Group.AccountQuota; quota == nil || len(quota.Windows) != 0 || quota.AccountCount != 5 {
		t.Fatal("expired observations presented as available/zero instead of unknown")
	}
	unlimited, observed := true, fixedTime.Add(time.Minute)
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "pro", ObservedAt: observed,
		Credits: &domain.AccountCreditStatus{AccountID: "pro", ObservedAt: observed, Unlimited: &unlimited}}); err != nil {
		t.Fatal(err)
	}
	if quota := read(observed).Group.AccountQuota; !quota.CreditsUnlimited || quota.CreditsKnownAccountCount != 3 {
		t.Fatal("unlimited purchased credits not retained")
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HideUpstreamQuotaFromKeys = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if read(fixedTime).Group.AccountQuota != nil {
		t.Fatal("global privacy policy exposed upstream quota")
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HideUpstreamQuotaFromKeys = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	key.UsageSections = "upstream_limits"
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if read(fixedTime).Group.AccountQuota != nil {
		t.Fatal("key visibility policy exposed hidden pool usage")
	}
}
