package sqlite

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestNativeUncertainReservationKeepsOnlyItsOwnBudget(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(100)}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"uncertain", "other"} {
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
			Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 40}, Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	for _, attempt := range []int{1, 2} {
		if marked, err := store.MarkReservationUncertain(ctx, "uncertain"); err != nil || !marked {
			t.Fatalf("attempt %d did not retain uncertainty: %v, %v", attempt, marked, err)
		}
	}
	if released, err := store.ReleaseStaleReservations(ctx, fixedTime.Add(time.Hour)); err != nil || released != 1 {
		t.Fatalf("uncertain reservation was stale-released or another call marked: %d, %v", released, err)
	}
	reservation, err := store.GetReservation(ctx, "uncertain")
	if err != nil || reservation.Status != "reserved" || !reservation.NeedsReconciliation {
		t.Fatalf("unknown spend was settled: %+v, %v", reservation, err)
	}
	key, err = store.GetAPIKey(ctx, "key")
	if err != nil || key.Limits[0].CurrentValue != 40 {
		t.Fatalf("held budget changed: %+v, %v", key.Limits, err)
	}
	event := domain.UsageEvent{RequestID: "confirmed", AccountID: "acct", Model: "gpt-test",
		RequestKind: "embeddings", Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: 15}}
	if applied, err := store.SettleUsage(ctx, "uncertain", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("explicit reconciliation did not settle once: %v, %v", applied, err)
	}
	key, err = store.GetAPIKey(ctx, "key")
	if err != nil || key.Limits[0].CurrentValue != 15 {
		t.Fatalf("explicit usage was not settled: %+v, %v", key.Limits, err)
	}
}
