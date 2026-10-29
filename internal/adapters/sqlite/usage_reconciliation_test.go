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
		if count, err := store.MarkInterruptedReservations(ctx, nil); err != nil || count != want {
			t.Fatalf("mark interrupted: %d %v", count, err)
		}
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

// Owner decision: usage nobody reconciles is released without charge after the
// grace period; reservations still in flight or flagged recently stay.
func TestReleaseUnreconciledReservationsRefundsWithoutCharging(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "account")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(1000)}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	reserve := func(id string, at time.Time) {
		t.Helper()
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: key.ID, AccountID: "account", Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 40}, Now: at}); err != nil {
			t.Fatal(err)
		}
	}
	reserve("old-interrupted", fixedTime)
	reserve("recent-interrupted", fixedTime.Add(50*time.Minute))
	if count, err := store.MarkInterruptedReservations(ctx, nil); err != nil || count != 2 {
		t.Fatalf("mark interrupted: %d %v", count, err)
	}
	reserve("live", fixedTime)
	pending := func() bool {
		t.Helper()
		logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range logs.Requests {
			if entry.RequestID == "old-interrupted" {
				return true
			}
		}
		return false
	}
	if !pending() {
		t.Fatal("the interrupted reservation is not listed as pending before release")
	}
	cutoff := fixedTime.Add(30 * time.Minute)
	if released, err := store.ReleaseUnreconciledReservations(ctx, cutoff, 1000); err != nil || released != 1 {
		t.Fatalf("released %d, %v; want only the old interrupted reservation", released, err)
	}
	for id, want := range map[string]string{"old-interrupted": "released", "recent-interrupted": "reserved", "live": "reserved"} {
		reservation, err := store.GetReservation(ctx, id)
		if err != nil || reservation.Status != want {
			t.Fatalf("%s: status %q, %v; want %q", id, reservation.Status, err, want)
		}
	}
	key, err := store.GetAPIKey(ctx, key.ID)
	if err != nil || key.Limits[0].CurrentValue != 80 {
		t.Fatalf("limit holds %d, want the two remaining reservations' 80", key.Limits[0].CurrentValue)
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 0 {
		t.Fatalf("release recorded usage: %+v %v", totals, err)
	}
	if pending() {
		t.Fatal("a released reservation is still listed as pending")
	}
	if released, err := store.ReleaseUnreconciledReservations(ctx, cutoff, 1000); err != nil || released != 0 {
		t.Fatalf("second release %d, %v", released, err)
	}
	if _, err := store.ReleaseUnreconciledReservations(ctx, cutoff, 0); err == nil {
		t.Fatal("a zero batch is accepted")
	}
}
