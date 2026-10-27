package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// The batched selection snapshot must equal the per-account loaders it replaces.
func TestAccountSelectionSnapshotEqualsPerAccountReads(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	for _, id := range []string{"quota", "credits", "refused", "additional", "empty"} {
		saveTestAccount(t, store, id)
	}
	key := testKey("key", nil)
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	reset := now.Add(time.Hour)
	week := 10080
	has, unlimited, balance := true, false, 12.5
	for _, snapshot := range []domain.AccountUsageSnapshot{
		{AccountID: "quota", ObservedAt: now, Quotas: []domain.AccountQuota{
			{AccountID: "quota", Window: "secondary", UsedPercent: 70, ResetAt: &reset, WindowMinutes: &week, ObservedAt: now},
			{AccountID: "quota", Window: "primary", UsedPercent: 20, ObservedAt: now}}},
		{AccountID: "credits", ObservedAt: now, Credits: &domain.AccountCreditStatus{AccountID: "credits", Has: &has, Unlimited: &unlimited, Balance: &balance, ObservedAt: now}},
		{AccountID: "additional", ObservedAt: now, AdditionalReported: true, AdditionalQuotas: []domain.AccountAdditionalQuota{
			{AccountID: "additional", QuotaKey: "codex_other", LimitName: "other", MeteredFeature: "f", Window: "primary", UsedPercent: 40, ResetAt: &reset, ObservedAt: now},
			{AccountID: "additional", QuotaKey: "codex_bengalfox", LimitName: "spark", MeteredFeature: "g", Window: "secondary", UsedPercent: 10, WindowMinutes: &week, ObservedAt: now}}},
	} {
		if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "refusal", APIKeyID: "key", AccountID: "refused",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 1}, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "refusal", domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{RequestID: "refusal-event",
		AccountID: "refused", Model: "gpt-test", Status: "error", ErrorCode: "usage_limit_reached", RequestedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAccountOutcome(ctx, "refused", "refusal", true, false); err != nil {
		t.Fatal(err)
	}
	ids := []string{"quota", "credits", "refused", "additional", "empty", "unknown"}
	states, err := store.AccountSelectionSnapshot(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		quotas, err := store.ListAccountQuota(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		credits, err := store.LoadAccountCreditStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		refusal, err := store.LoadAccountQuotaRefusalAt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		additional, err := store.ListAccountAdditionalQuotas(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.AccountSelectionState{Quotas: quotas, Credits: credits, RefusalAt: refusal, Additional: additional}
		if got := states[id]; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s snapshot %+v differs from per-account reads %+v", id, got, want)
		}
	}
	if states["refused"].RefusalAt == nil || len(states["quota"].Quotas) != 2 || states["credits"].Credits == nil || len(states["additional"].Additional) != 2 {
		t.Fatalf("fixture lost telemetry: %+v", states)
	}
}
