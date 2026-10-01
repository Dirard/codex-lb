package sqlite

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRefreshExpiredKeyLimitsPreservesActiveWindow(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{
		{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 100},
		{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100},
	}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE api_key_limits SET current_value=7,
 reset_at=? WHERE api_key_id='key' AND limit_window='daily'`, millis(fixedTime.Add(-25*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE api_key_limits SET current_value=9
 WHERE api_key_id='key' AND limit_window='weekly'`); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshExpiredKeyLimits(ctx, "key", fixedTime); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetAPIKey(ctx, "key")
	if err != nil || len(updated.Limits) != 2 {
		t.Fatalf("refreshed limits: %+v, %v", updated.Limits, err)
	}
	for _, limit := range updated.Limits {
		switch limit.Window {
		case domain.WindowDaily:
			if limit.CurrentValue != 0 || !limit.ResetAt.After(fixedTime) || limit.ResetAt.After(fixedTime.Add(24*time.Hour)) {
				t.Fatalf("expired daily limit did not advance by whole windows: %+v", limit)
			}
		case domain.WindowWeekly:
			if limit.CurrentValue != 9 {
				t.Fatalf("active weekly limit was reset: %+v", limit)
			}
		}
	}
}
