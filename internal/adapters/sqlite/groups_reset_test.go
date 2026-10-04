package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestResetGroupUsageIsAtomicAndScoped(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	groupID, otherID := "group", "other"
	rules := []domain.LimitRule{
		{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 1000},
		{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1000000},
	}
	for _, id := range []string{groupID, otherID, "empty"} {
		if err := s.SaveGroup(ctx, domain.AccountGroup{ID: id, Name: id, AccountIDs: []string{"acct"}, Limits: rules}, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string][]domain.LimitRule{}
	for _, item := range []struct {
		id    string
		group *string
	}{{"a", &groupID}, {"b", &groupID}, {"inactive", &groupID}, {"deleted", &groupID}, {"other", &otherID}, {"solo", nil}} {
		key := testKey(item.id, item.group)
		key.Limits = rules
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: item.id, APIKeyID: item.id, AccountID: "acct",
			Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 100}, Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SettleUsage(ctx, item.id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
			RequestID: "request-" + item.id, Model: "gpt-test", Status: "success", RequestedAt: fixedTime,
			Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 100}}}); err != nil {
			t.Fatal(err)
		}
		key, err := s.GetAPIKey(ctx, item.id)
		if err != nil {
			t.Fatal(err)
		}
		before[item.id] = key.Limits
		if item.id == "inactive" {
			key.IsActive = false
			if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.DeleteAPIKey(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	totals, err := s.UsageTotals(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	quota := domain.AccountQuota{AccountID: "acct", Window: "weekly", UsedPercent: 90, ObservedAt: fixedTime}
	if err := s.SaveAccountQuota(ctx, quota); err != nil {
		t.Fatal(err)
	}
	now := fixedTime.Add(time.Hour)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_group_reset BEFORE UPDATE OF current_value ON api_key_limits
 WHEN OLD.api_key_id='b' BEGIN SELECT RAISE(ABORT,'synthetic reset failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetGroupUsage(ctx, groupID, now); err == nil {
		t.Fatal("failed group reset succeeded")
	}
	first, err := s.GetAPIKey(ctx, "a")
	if err != nil || !reflect.DeepEqual(first.Limits, before["a"]) {
		t.Fatalf("partial reset escaped rollback: %+v, %v", first.Limits, err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER fail_group_reset"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetGroupUsage(ctx, groupID, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "inactive", "other", "solo"} {
		key, err := s.GetAPIKey(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == "other" || id == "solo" {
			if !reflect.DeepEqual(key.Limits, before[id]) {
				t.Fatalf("unrelated key changed: %s", id)
			}
			continue
		}
		for _, limit := range key.Limits {
			duration, _ := limit.Window.Duration()
			if limit.CurrentValue != 0 || !limit.ResetAt.Equal(now.Add(duration)) {
				t.Fatalf("key %s limit not reset: %+v", id, limit)
			}
		}
		if id == "inactive" && key.IsActive {
			t.Fatal("reset activated a disabled key")
		}
	}
	for _, limit := range before["deleted"] {
		var current, reset int64
		if err := s.readDB.QueryRowContext(ctx, "SELECT current_value,reset_at FROM api_key_limits WHERE id=?", limit.ID).Scan(&current, &reset); err != nil || current != limit.CurrentValue || reset != millis(limit.ResetAt) {
			t.Fatalf("deleted key changed: current=%d reset=%d error=%v", current, reset, err)
		}
	}
	if after, err := s.UsageTotals(ctx, "", ""); err != nil || after != totals {
		t.Fatalf("historical usage changed: %+v, %v", after, err)
	}
	if quotas, err := s.ListAccountQuota(ctx, "acct"); err != nil || len(quotas) != 1 || quotas[0].UsedPercent != 90 {
		t.Fatalf("provider quota changed: %+v, %v", quotas, err)
	}
	if err := s.ResetGroupUsage(ctx, "empty", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetGroupUsage(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := s.ResetGroupUsage(ctx, groupID, time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid time: %v", err)
	}
}

func TestManualResetsNeverReuseReservationEpoch(t *testing.T) {
	for _, scope := range []string{"key", "group"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			s, _ := testStore(t)
			saveTestAccount(t, s, "acct")
			group := "group"
			if err := s.SaveGroup(ctx, domain.AccountGroup{ID: group, Name: group, AccountIDs: []string{"acct"}, Limits: []domain.LimitRule{tokenLimit(1000)}}, fixedTime); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveAPIKey(ctx, testKey("key", &group), fixedTime); err != nil {
				t.Fatal(err)
			}
			for n := range 2 {
				id := fmt.Sprintf("old-%d", n)
				if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct", Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 10}, Now: fixedTime}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.MarkReservationUncertain(ctx, id); err != nil {
					t.Fatal(err)
				}
				var err error
				if scope == "group" {
					err = s.ResetGroupUsage(ctx, group, fixedTime)
				} else {
					err = s.ResetAPIKeyUsage(ctx, "key", fixedTime)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if pending, err := s.ListReservationsNeedingReconciliation(ctx, "key", 10); err != nil || len(pending) != 2 {
				t.Fatalf("reset removed pending reservations: count=%d error=%v", len(pending), err)
			}
			if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "new", APIKeyID: "key", AccountID: "acct", Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 7}, Now: fixedTime}); err != nil {
				t.Fatal(err)
			}
			for n := range 2 {
				id := fmt.Sprintf("old-%d", n)
				if _, err := s.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "request-" + id, Model: "gpt-test", Status: "success", RequestedAt: fixedTime, Usage: domain.UsageAmount{InputTokens: 50}}}); err != nil {
					t.Fatal(err)
				}
			}
			key, err := s.GetAPIKey(ctx, "key")
			if err != nil || key.Limits[0].CurrentValue != 7 {
				t.Fatalf("late settlement charged the new period: %+v, %v", key.Limits, err)
			}
			if totals, err := s.UsageTotals(ctx, "key", ""); err != nil || totals.RequestCount != 2 || totals.Usage.InputTokens != 100 {
				t.Fatalf("historical settlement missing: %+v, %v", totals, err)
			}
		})
	}
}
