package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func TestModelPricingReconcilesProvenLimitBackfillAndClearsItOnReset(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	if err := store.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reservation", APIKeyID: "key",
		AccountID: "acct", Model: "gpt-6-sol", Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "reservation", domain.UsageSettlement{Status: "finalized",
		Event: domain.UsageEvent{RequestID: "request", AccountID: "acct", Model: "gpt-6-sol",
			RequestKind: "normal", Status: "success", RequestedAt: fixedTime.Add(time.Minute),
			Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 20}}}); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 1000000}}
	if err := store.SaveAPIKey(ctx, key, fixedTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetAPIKey(ctx, "key")
	if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != 20 {
		t.Fatalf("historical cost not backfilled into new limit: %+v, %v", loaded.Limits, err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime.Add(2 * time.Hour) })
	if _, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 4000000}}); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetAPIKey(ctx, "key")
	if err != nil || loaded.Limits[0].CurrentValue != 40 {
		t.Fatalf("proved backfill cost not repriced: %+v, %v", loaded.Limits, err)
	}
	if err := store.ResetAPIKeyUsage(ctx, "key", fixedTime.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 6000000}}); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetAPIKey(ctx, "key")
	if err != nil || loaded.Limits[0].CurrentValue != 0 {
		t.Fatalf("reset limit resurrected backfilled charge: %+v, %v", loaded.Limits, err)
	}
}

func TestModelPricingKeepsMatchedGroupLimitChargeIdentity(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	groupID := "group"
	group := domain.AccountGroup{ID: groupID, Name: "Group", AccountIDs: []string{"acct"},
		Limits: []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 1000000}}}
	if err := store.SaveGroup(ctx, group, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, testKey("key", &groupID), fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reservation", APIKeyID: "key",
		AccountID: "acct", Model: "gpt-6-sol", Budget: domain.UsageAmount{CostMicrodollars: 100}, Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "reservation", domain.UsageSettlement{Status: "finalized",
		Event: domain.UsageEvent{RequestID: "request", AccountID: "acct", Model: "gpt-6-sol",
			RequestKind: "normal", Status: "success", RequestedAt: fixedTime.Add(time.Minute),
			Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 20}}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAPIKey(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	group.Limits[0].MaxValue = 2000000
	if err := store.SaveGroup(ctx, group, fixedTime.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime.Add(time.Hour) })
	if _, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 4000000}}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetAPIKey(ctx, "key")
	if err != nil || len(before.Limits) != 1 || len(after.Limits) != 1 ||
		after.Limits[0].ID != before.Limits[0].ID || after.Limits[0].CurrentValue != 40 ||
		after.Limits[0].MaxValue != 2000000 {
		t.Fatalf("matched group rule lost charge identity: %+v -> %+v, %v", before.Limits, after.Limits, err)
	}
}

func TestModelPricingRefusesUnprovenActiveCostLimitCharge(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 1000000}}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "reservation", APIKeyID: "key",
		AccountID: "acct", Model: "gpt-6-sol", Budget: domain.UsageAmount{CostMicrodollars: 100}, Now: fixedTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, "reservation", domain.UsageSettlement{Status: "finalized",
		Event: domain.UsageEvent{RequestID: "request", AccountID: "acct", Model: "gpt-6-sol",
			RequestKind: "normal", Status: "success", RequestedAt: fixedTime.Add(time.Minute),
			Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 20}}}); err != nil {
		t.Fatal(err)
	}
	// Simulate an older ledger whose active limit has no charge provenance.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM usage_reservation_items WHERE reservation_id='reservation'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE api_key_limits SET backfill_from=NULL,backfill_until=NULL WHERE api_key_id='key'`); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime.Add(time.Hour) })
	if _, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 4000000}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unproven current cost limit was guessed: %v", err)
	}
	price, err := service.ResolveCodex(ctx, "gpt-6-sol")
	if err != nil || price.Standard.Input != 2000000 {
		t.Fatalf("failed reprice left override committed: %+v, %v", price, err)
	}
	var cost int64
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='request'`).Scan(&cost); err != nil || cost != 20 {
		t.Fatalf("failed reprice changed event cost: %d, %v", cost, err)
	}
}

func TestModelPricingPreservesExplicitReconciledCost(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "manual", AccountID: "acct",
		Model: "gpt-6-sol", RequestKind: "reconciled", Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: 7, OutputTokens: 3, CostMicrodollars: 123}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "non-token", AccountID: "acct",
		Model: "gpt-6-sol", RequestKind: "normal", Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{CostMicrodollars: 77}}); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	entry, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 4000000, Output: 4000000}})
	if err != nil || entry.Reprice == nil || entry.Reprice.PartialRawRequests != 2 || entry.Reprice.RecomputedRequests != 0 {
		t.Fatalf("manual settlement not distinguished from tariff-priced history: %+v, %v", entry.Reprice, err)
	}
	var cost int64
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='manual'`).Scan(&cost); err != nil || cost != 123 {
		t.Fatalf("explicit reconciliation cost overwritten: %d, %v", cost, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='non-token'`).Scan(&cost); err != nil || cost != 77 {
		t.Fatalf("non-token charge overwritten: %d, %v", cost, err)
	}
}

func TestModelPricingDoesNotRepriceExternalSourceHistory(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	externalRate := 7.0
	if err := store.SaveModelSource(ctx, domain.ModelSource{ID: "external", Name: "External",
		Kind: domain.ModelSourceOpenAICompatible, BaseURL: "https://provider.example.invalid/v1",
		Enabled: true, Chat: true, CreatedAt: fixedTime, UpdatedAt: fixedTime,
		Models: []domain.ModelSourceModel{{Model: "gpt-6-sol", Enabled: true, InputPerMillion: &externalRate}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "external-usage",
		AccountID: "external", ModelSourceID: "external", Model: "gpt-6-sol", RequestKind: "normal",
		Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 77}}); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	entry, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 4000000}})
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 0 {
		t.Fatalf("external history entered Codex reprice: %+v, %v", entry.Reprice, err)
	}
	var cost int64
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='external-usage'`).Scan(&cost); err != nil || cost != 77 {
		t.Fatalf("external source cost changed: %d, %v", cost, err)
	}
}
