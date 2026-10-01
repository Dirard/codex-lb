package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestKeyReportLimitsCurrentWindowsAreReadOnly(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	saveTestAccount(t, store, "acct")
	key := testKey("key-limits", nil)
	model := "gpt-6-luna"
	key.Limits = []domain.LimitRule{
		{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 100},
		{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1_000_000, ModelFilter: &model},
	}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "held", APIKeyID: key.ID, AccountID: "acct",
		Model: model, Budget: domain.UsageAmount{InputTokens: 25, CostMicrodollars: 300_000}, Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE api_key_limits SET current_value=120 WHERE api_key_id=? AND limit_type='total_tokens'`, key.ID); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{time.Hour, 25 * time.Hour} {
		snapshot, err := store.KeyReportLimits(ctx, key.ID, fixedTime.Add(elapsed))
		if err != nil || len(snapshot.Limits) != 2 || snapshot.Group != nil {
			t.Fatalf("personal limits: %+v %v", snapshot, err)
		}
		for _, limit := range snapshot.Limits {
			want := int64(300_000)
			if limit.Type == domain.LimitTotalTokens {
				want = 120
				if elapsed > 24*time.Hour {
					want = 0
				}
			} else if limit.ModelFilter == nil || *limit.ModelFilter != model {
				t.Fatal("lost model-specific rule")
			}
			if limit.CurrentValue != want || !limit.ResetAt.After(fixedTime.Add(elapsed)) {
				t.Fatalf("incorrect current window: %+v", limit)
			}
		}
	}
	after, err := store.GetAPIKey(ctx, key.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("report read mutated persisted limits")
	}
	if reservation, err := store.GetReservation(ctx, "held"); err != nil || reservation.Status != "reserved" {
		t.Fatal("report read settled a reservation")
	}
	for _, id := range []string{"missing", domain.LocalProxyKeyID, domain.WarmupKeyID} {
		if _, err := store.KeyReportLimits(ctx, id, fixedTime); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("invalid caller %q admitted: %v", id, err)
		}
	}
}
