package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestWarmupReservationIsPrivateAndReconcilesWithoutClientCharge(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	saveTestAccount(t, store, "warm-account")
	key := testKey("client", nil)
	key.Limits = []domain.LimitRule{tokenLimit(1)}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveWarmupUsage(ctx, "warm-held", "warm-account", "gpt-6-sol", 0, false, fixedTime); err != nil {
		t.Fatal(err)
	}
	principal, err := store.GetAPIKey(ctx, domain.WarmupKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindAPIKeyByHash(ctx, principal.KeyHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("internal principal authenticated: %v", err)
	}
	keys, err := store.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatal("internal principal leaked into client key list")
	}
	for _, err := range []error{store.SaveAPIKey(ctx, principal, fixedTime), store.DeleteAPIKey(ctx, principal.ID), store.ResetAPIKeyUsage(ctx, principal.ID, fixedTime)} {
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("internal principal could be edited: %v", err)
		}
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "client-bypass", APIKeyID: principal.ID, AccountID: "warm-account", Model: "gpt-6-sol", Now: fixedTime}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("admin reservation admitted as client traffic: %v", err)
	}
	if ok, err := store.MarkReservationUncertain(ctx, "warm-held"); err != nil || !ok {
		t.Fatalf("retain: %v %v", ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if n, err := store.ReleaseStaleReservations(ctx, fixedTime.Add(24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("warmup uncertainty was stale-released: %d %v", n, err)
	}
	logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
	if err != nil || len(logs.Requests) != 1 {
		t.Fatalf("pending projection: %+v %v", logs, err)
	}
	row := logs.Requests[0]
	if row.RequestKind != "warmup" || row.APIKeyID != nil || row.InputTokens != nil || row.CostUSD != nil || row.Status != "reconciliation_required" {
		t.Fatalf("pending warmup became paid client/free usage: %+v", row)
	}
	event := domain.UsageEvent{RequestID: "warm-held", AccountID: "warm-account", Model: "gpt-6-sol", Status: "ok", RequestedAt: fixedTime, Usage: domain.UsageAmount{InputTokens: 3}}
	if applied, err := store.SettleUsage(ctx, "warm-held", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("reconcile: %v %v", applied, err)
	}
	if applied, err := store.SettleUsage(ctx, "warm-held", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || applied {
		t.Fatalf("duplicate settlement: %v %v", applied, err)
	}
	logs, err = store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
	if err != nil || len(logs.Requests) != 1 || logs.Requests[0].APIKeyID != nil || logs.Requests[0].RequestKind != "warmup" || logs.Requests[0].InputTokens == nil || *logs.Requests[0].InputTokens != 3 {
		t.Fatalf("reconciled warmup lost identity/usage: %+v %v", logs, err)
	}
	key, err = store.GetAPIKey(ctx, key.ID)
	if err != nil || key.Limits[0].CurrentValue != 0 {
		t.Fatal("warmup consumed a client key limit")
	}
}

func TestWarmupReservationRechecksOperatorGate(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "warm-account")
	account, _ := store.GetAccount(ctx, "warm-account")
	account.Status = domain.AccountPaused
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveWarmupUsage(ctx, "manual", account.ID, "model", account.Generation, false, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveWarmupUsage(ctx, "automatic", account.ID, "model", account.Generation, true, fixedTime); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("automatic work bypassed operator pause: %v", err)
	}
	account.RequiresEgressDecision = true
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveWarmupUsage(ctx, "egress", account.ID, "model", account.Generation, false, fixedTime); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("warmup bypassed required egress: %v", err)
	}
}
