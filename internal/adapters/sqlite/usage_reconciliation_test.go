package sqlite

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestInterruptedReservationKeepsBudgetAndSettlesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "account")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(100)}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "uncertain", APIKeyID: key.ID, AccountID: "account", Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 40}, Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{1, 0} {
		if count, err := store.MarkInterruptedReservations(ctx); err != nil || count != want {
			t.Fatalf("mark interrupted: %d %v", count, err)
		}
	}
	if count, err := store.ReleaseStaleReservations(ctx, fixedTime.Add(time.Hour)); err != nil || count != 0 {
		t.Fatal("unknown upstream usage was automatically forgiven")
	}
	key, err := store.GetAPIKey(ctx, key.ID)
	if err != nil || key.Limits[0].CurrentValue != 40 {
		t.Fatal("interruption changed held budget")
	}
	event := usageEvent("confirmed", 15)
	event.RequestKind = "compaction"
	for _, want := range []bool{true, false} {
		if applied, err := store.SettleUsage(ctx, "uncertain", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || applied != want {
			t.Fatalf("settlement: %v %v", applied, err)
		}
	}
	key, err = store.GetAPIKey(ctx, key.ID)
	if err != nil || key.Limits[0].CurrentValue != 15 {
		t.Fatal("reconciliation duplicated or lost consumption")
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 15 {
		t.Fatal("billable ancillary usage missing from key totals")
	}
}

func TestNonbillableKeyEventIncrementsTotalsOnce(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "account")
	key := testKey("key", nil)
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	event := usageEvent("control", 0)
	event.APIKeyID, event.AccountID, event.RequestKind = key.ID, "account", "codex_control"
	if err := store.RecordCodexContentFreeUsage(ctx, event); err != nil {
		t.Fatal(err)
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 1 {
		t.Fatalf("control request counted more than once: %+v %v", totals, err)
	}
}
