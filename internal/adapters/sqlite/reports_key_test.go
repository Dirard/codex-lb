package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestAPIKeyReportsUseKeyedTrafficBeforeAndAfterRetention(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct-a")
	saveTestAccount(t, store, "acct-b")
	for _, id := range []string{"key-a", "key-b"} {
		if err := store.SaveAPIKey(ctx, testKey(id, nil), fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		key, account, kind string
		cost               int64
	}{
		{"key-a", "acct-a", "normal", 1250000},
		{"key-a", "acct-a", "warmup", 500000},
		{"key-b", "acct-b", "normal", 2000000},
	} {
		id := item.key + "-" + item.kind
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reservation-" + id,
			APIKeyID: item.key, AccountID: item.account, Model: "gpt-test", Now: fixedTime.Add(-2 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, "reservation-"+id, domain.UsageSettlement{Status: "finalized",
			Event: domain.UsageEvent{RequestID: "request-" + id, APIKeyID: item.key,
				AccountID: item.account, Model: "gpt-test", RequestKind: item.kind, Status: "success",
				RequestedAt: fixedTime.Add(-2 * time.Hour), Usage: domain.UsageAmount{
					InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2, CostMicrodollars: item.cost}}}); err != nil {
			t.Fatal(err)
		}
	}
	service := application.NewReportsService(store, func() time.Time { return fixedTime })
	check := func() {
		t.Helper()
		trends, err := service.APIKeyTrends(ctx, "key-a", store)
		if err != nil || len(trends.Tokens) != 168 || len(trends.Cost) != 168 {
			t.Fatalf("key trend grid: %+v, %v", trends, err)
		}
		var tokens, cost float64
		for i := range trends.Tokens {
			tokens += trends.Tokens[i].V
			cost += trends.Cost[i].V
		}
		if tokens != 15 || cost != 1.25 {
			t.Fatalf("key trends include warmup or another key: tokens=%g cost=%g", tokens, cost)
		}
		usage, err := service.APIKeyUsage7Day(ctx, "key-a", store)
		if err != nil || usage.KeyID != "key-a" || usage.TotalRequests != 1 || usage.TotalTokens != 15 ||
			usage.CachedInputTokens != 2 || usage.TotalCostUSD != 1.25 || len(usage.AccountCosts) != 1 ||
			usage.AccountCosts[0].AccountID == nil || *usage.AccountCosts[0].AccountID != "acct-a" ||
			usage.AccountCosts[0].CostUSD != 1.25 {
			t.Fatalf("key usage 7d: %+v, %v", usage, err)
		}
	}
	check()
	if _, err := service.APIKeyTrends(ctx, "missing", store); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing key trends not rejected: %v", err)
	}
	if _, err := service.APIKeyUsage7Day(ctx, "missing", store); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing key usage not rejected: %v", err)
	}
	if folded, err := store.FoldAndPruneRequestLogs(ctx, fixedTime, 100); err != nil || folded != 3 {
		t.Fatalf("retention fold: %d, %v", folded, err)
	}
	check()
}
