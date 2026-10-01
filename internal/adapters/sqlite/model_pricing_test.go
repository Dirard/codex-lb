package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func TestModelPricingOverridesAliasSnapshotAndRestore(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	saveTestAccount(t, store, "acct")
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	usage := domain.UsageAmount{InputTokens: 300000, CachedInputTokens: 50000, OutputTokens: 100000}
	builtin, err := service.ResolveCodex(ctx, "gpt-6-sol-2026-09-26")
	if err != nil {
		t.Fatal(err)
	}
	builtinCost, err := builtin.Cost(usage, "priority")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "already-settled",
		AccountID: "acct", Model: "gpt-6-sol", ServiceTier: "priority", RequestKind: "normal", Status: "success", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CachedInputTokens: usage.CachedInputTokens, CostMicrodollars: builtinCost}}); err != nil {
		t.Fatal(err)
	}
	custom := pricing.Price{Standard: pricing.Rates{Input: 3000000, Cached: 300000, Output: 12000000},
		PriorityMultiplierMilli: 2000, Flex: &pricing.Rates{Input: 1000000, Cached: 100000, Output: 6000000},
		Long: &pricing.Rates{Input: 6000000, Cached: 600000, Output: 18000000}, Threshold: 272000}
	entry, err := service.Save(ctx, "GPT-6-SOL", custom)
	if err != nil || entry.Model != "gpt-6-sol" || entry.Source != "custom" || !entry.HasBuiltin ||
		entry.Reprice == nil || !entry.Reprice.Applied || entry.Reprice.RecomputedRequests != 1 {
		t.Fatalf("builtin override: %+v, %v", entry, err)
	}
	customCost, err := custom.Cost(usage, "priority")
	if err != nil {
		t.Fatal(err)
	}
	if totals, err := store.UsageTotals(ctx, "", ""); err != nil || totals.Usage.CostMicrodollars != customCost {
		t.Fatalf("historical request not repriced: %+v, %v", totals, err)
	}
	externalRate := 7.0
	if err := store.SaveModelSource(ctx, domain.ModelSource{ID: "external", Name: "External",
		Kind: domain.ModelSourceOpenAICompatible, BaseURL: "https://provider.example.invalid/v1",
		Enabled: true, Chat: true, CreatedAt: fixedTime, UpdatedAt: fixedTime,
		Models: []domain.ModelSourceModel{{Model: "gpt-6-sol", Enabled: true, InputPerMillion: &externalRate}}}, nil); err != nil {
		t.Fatal(err)
	}
	sources := application.NewModelSourceService(store, nil)
	providerPrice, err := sources.Price(ctx, domain.Account{ID: "external", Kind: domain.AccountExternal}, "gpt-6-sol")
	if err != nil || providerPrice.Standard.Input != 7000000 {
		t.Fatalf("external source price replaced by global override: %+v, %v", providerPrice, err)
	}
	snapshot, err := service.ResolveCodex(ctx, "gpt-6-sol-2026-09-26")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := snapshot.Cost(usage, "priority")
	if err != nil || cost == builtinCost {
		t.Fatalf("override did not price long-context priority: %d vs builtin %d, %v", cost, builtinCost, err)
	}
	fresh := pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 4000000}}
	if entry, err := service.Save(ctx, "gpt-6-sol", fresh); err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 {
		t.Fatal(err)
	}
	freshCost, err := fresh.Cost(usage, "priority")
	if err != nil {
		t.Fatal(err)
	}
	if totals, err := store.UsageTotals(ctx, "", ""); err != nil || totals.Usage.CostMicrodollars != freshCost {
		t.Fatalf("second edit did not reprice old settlement: %+v, %v", totals, err)
	}
	current, err := service.ResolveCodex(ctx, "gpt-6-sol")
	if err != nil || current.Standard.Input != fresh.Standard.Input {
		t.Fatalf("next admission did not see new override: %+v, %v", current, err)
	}
	if _, err := service.Save(ctx, "gpt-7-codex", custom); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveCodex(ctx, "gpt-7-codex-2026"); !errors.Is(err, pricing.ErrUnpriced) {
		t.Fatalf("custom model accidentally got prefix alias: %v", err)
	}
	aliasPrice := pricing.Price{Standard: pricing.Rates{Input: 7000000, Cached: 700000, Output: 21000000}}
	if _, err := service.Save(ctx, "gpt-6-sol-2026-09-26", aliasPrice); err != nil {
		t.Fatal(err)
	}
	if exact, err := service.ResolveCodex(ctx, "gpt-6-sol-2026-09-26"); err != nil || exact.Standard.Input != aliasPrice.Standard.Input {
		t.Fatalf("exact alias override did not win: %+v, %v", exact, err)
	}
	listing, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]application.ModelPriceEntry{}
	for _, price := range listing.Prices {
		found[price.Model] = price
	}
	if found["gpt-6-sol"].Source != "custom" || !found["gpt-6-sol"].HasBuiltin ||
		found["gpt-6-sol-2026-09-26"].Source != "custom" || !found["gpt-6-sol-2026-09-26"].HasBuiltin ||
		found["gpt-7-codex"].HasBuiltin || found["gpt-7-codex"].Source != "custom" {
		t.Fatalf("merged price sources: %+v", found)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := application.NewModelPricingService(reopened, func() time.Time { return fixedTime })
	if persisted, err := restarted.ResolveCodex(ctx, "gpt-7-codex"); err != nil || persisted.Standard.Input != custom.Standard.Input {
		t.Fatalf("custom model price not restart-safe: %+v, %v", persisted, err)
	}
	if _, err := restarted.Delete(ctx, "gpt-6-sol-2026-09-26"); err != nil {
		t.Fatal(err)
	}
	if inherited, err := restarted.ResolveCodex(ctx, "gpt-6-sol-2026-09-26"); err != nil || inherited.Standard.Input != fresh.Standard.Input {
		t.Fatalf("alias delete did not restore canonical override: %+v, %v", inherited, err)
	}
	if summary, err := restarted.Delete(ctx, "gpt-6-sol"); err != nil || !summary.Applied || summary.RecomputedRequests != 1 {
		t.Fatal(err)
	}
	if restored, err := restarted.ResolveCodex(ctx, "gpt-6-sol"); err != nil || restored.Standard.Input != builtin.Standard.Input {
		t.Fatalf("builtin delete did not restore rate card: %+v, %v", restored, err)
	}
	if summary, err := restarted.Delete(ctx, "gpt-7-codex"); err != nil || summary.Applied {
		t.Fatal(err)
	}
	if _, err := restarted.ResolveCodex(ctx, "gpt-7-codex"); !errors.Is(err, pricing.ErrUnpriced) {
		t.Fatalf("deleted custom model remained priced: %v", err)
	}
	settled, err := reopened.UsageTotals(ctx, "", "")
	if err != nil || settled.RequestCount != 1 || settled.Usage.CostMicrodollars != builtinCost {
		t.Fatalf("restored builtin did not reprice history: %+v, %v", settled, err)
	}
}

func TestModelPricingRejectsInvalidFinancialInputs(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	valid := pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 5000000}}
	for _, model := range []string{"", "gpt-7*", "../gpt-7", "gpt 7"} {
		if _, err := service.Save(ctx, model, valid); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("unsafe model ID accepted %q: %v", model, err)
		}
	}
	bad := valid
	bad.Standard.Input = -1
	if _, err := service.Save(ctx, "gpt-7", bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("negative tariff accepted: %v", err)
	}
}

func TestModelPricingReconcilesKeyLedgerAndInFlightSettlement(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 1000000}}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	reserve := func(id string, at time.Time) {
		t.Helper()
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key",
			AccountID: "acct", Model: "gpt-6-sol", Budget: domain.UsageAmount{CostMicrodollars: 100}, Now: at}); err != nil {
			t.Fatal(err)
		}
	}
	settle := func(id string, at time.Time) {
		t.Helper()
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized",
			Event: domain.UsageEvent{RequestID: "request-" + id, APIKeyID: "key", AccountID: "acct",
				Model: "gpt-6-sol", RequestKind: "normal", Status: "success", RequestedAt: at,
				Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 20}}}); err != nil {
			t.Fatal(err)
		}
	}
	assertAmounts := func(expected int64, count int64) {
		t.Helper()
		for _, pair := range []struct{ key, account string }{{"", ""}, {"", "acct"}, {"key", ""}} {
			totals, err := store.UsageTotals(ctx, pair.key, pair.account)
			if err != nil || totals.RequestCount != count || totals.Usage.CostMicrodollars != expected {
				t.Fatalf("scope %q/%q not reconciled: %+v, %v", pair.key, pair.account, totals, err)
			}
		}
		loaded, err := store.GetAPIKey(ctx, "key")
		if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != expected {
			t.Fatalf("cost limit not reconciled: %+v, %v", loaded.Limits, err)
		}
	}
	reserve("first", fixedTime)
	settle("first", fixedTime.Add(time.Minute))
	assertAmounts(20, 1)
	firstPrice := pricing.Price{Standard: pricing.Rates{Input: 4000000}}
	entry, err := service.Save(ctx, "gpt-6-sol", firstPrice)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 || entry.Reprice.CostDeltaMicrodollars != 20 {
		t.Fatalf("first reprice summary: %+v, %v", entry.Reprice, err)
	}
	assertAmounts(40, 1)
	var eventCost, reservationCost, actualDelta int64
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='request-first'`).Scan(&eventCost); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_reservations WHERE id='first'`).Scan(&reservationCost); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT actual_delta FROM usage_reservation_items
 WHERE reservation_id='first' AND limit_type='cost_usd'`).Scan(&actualDelta); err != nil {
		t.Fatal(err)
	}
	if eventCost != 40 || reservationCost != 40 || actualDelta != 40 {
		t.Fatalf("ledger detail not reconciled: event=%d reservation=%d item=%d", eventCost, reservationCost, actualDelta)
	}
	reserve("second", fixedTime.Add(2*time.Minute)) // Admitted at the earlier tariff.
	secondPrice := pricing.Price{Standard: pricing.Rates{Input: 6000000}}
	if _, err := service.Save(ctx, "gpt-6-sol", secondPrice); err != nil {
		t.Fatal(err)
	}
	settle("second", fixedTime.Add(3*time.Minute)) // App still passes its old cost snapshot.
	assertAmounts(120, 2)
	if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='request-second'`).Scan(&eventCost); err != nil || eventCost != 60 {
		t.Fatalf("in-flight request restored stale price: %d, %v", eventCost, err)
	}
	if folded, err := store.FoldAndPruneRequestLogs(ctx, fixedTime.Add(4*time.Minute), 100); err != nil || folded != 2 {
		t.Fatalf("fold for incomplete historical reprice: %d, %v", folded, err)
	}
	thirdPrice := pricing.Price{Standard: pricing.Rates{Input: 8000000}}
	entry, err = service.Save(ctx, "gpt-6-sol", thirdPrice)
	if err != nil || entry.Reprice == nil || entry.Reprice.PartialFoldedRequests != 2 ||
		entry.Reprice.PartialFoldedBuckets == 0 || entry.Reprice.RecomputedRequests != 0 {
		t.Fatalf("folded history not reported partial: %+v, %v", entry.Reprice, err)
	}
	assertAmounts(120, 2) // No invented per-request prices after detail was folded.
}

func TestModelPricingRepricesAliasAndPreviouslyUnpricedZeroCost(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	for _, item := range []struct{ id, model string }{
		{"alias", "gpt-6-sol-2026-09-26"}, {"new", "gpt-7-codex"},
	} {
		if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: item.id,
			AccountID: "acct", Model: item.model, RequestKind: "normal", Status: "success", RequestedAt: fixedTime,
			Usage: domain.UsageAmount{InputTokens: 10}}); err != nil {
			t.Fatal(err)
		}
	}
	readCost := func(id string) int64 {
		t.Helper()
		var value int64
		if err := store.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id=?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if readCost("alias") != 20 || readCost("new") != 0 {
		t.Fatal("fixture did not preserve initially unpriced model")
	}
	price := pricing.Price{Standard: pricing.Rates{Input: 4000000}}
	entry, err := service.Save(ctx, "gpt-6-sol", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 || readCost("alias") != 40 {
		t.Fatalf("canonical override did not reprice alias: %+v, %v", entry.Reprice, err)
	}
	price.Standard.Input = 6000000
	entry, err = service.Save(ctx, "gpt-6-sol-2026-09-26", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 || readCost("alias") != 60 {
		t.Fatalf("exact alias override did not reprice: %+v, %v", entry.Reprice, err)
	}
	price.Standard.Input = 8000000
	entry, err = service.Save(ctx, "gpt-6-sol", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 0 || readCost("alias") != 60 {
		t.Fatalf("canonical edit overwrote exact alias override: %+v, %v", entry.Reprice, err)
	}
	if summary, err := service.Delete(ctx, "gpt-6-sol-2026-09-26"); err != nil ||
		!summary.Applied || summary.RecomputedRequests != 1 || readCost("alias") != 80 {
		t.Fatalf("alias delete did not restore canonical custom price: %+v, %v", summary, err)
	}
	price.Standard.Input = 5000000
	entry, err = service.Save(ctx, "gpt-7-codex", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 || readCost("new") != 50 {
		t.Fatalf("new tariff did not reprice historical zero cost: %+v, %v", entry.Reprice, err)
	}
	if summary, err := service.Delete(ctx, "gpt-7-codex"); err != nil || summary.Applied || readCost("new") != 50 {
		t.Fatalf("custom-only delete erased settled cost: %+v, %v", summary, err)
	}
}

func TestModelPricingImportedRawAndFoldedHistoryStaySeparated(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	store, _ := testStore(t)
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	beforeAll, err := store.UsageTotals(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := store.UsageTotals(ctx, "", "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	beforeKey, err := store.UsageTotals(ctx, "key-a", "")
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	price := pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 2000000}}
	entry, err := service.Save(ctx, "gpt-test", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 2 ||
		entry.Reprice.PartialFoldedRequests != 8 || entry.Reprice.PartialFoldedBuckets != 1 {
		t.Fatalf("imported history summary: %+v, %v", entry.Reprice, err)
	}
	delta := entry.Reprice.CostDeltaMicrodollars
	winnerCost, err := price.Cost(domain.UsageAmount{InputTokens: 30, OutputTokens: 6, CachedInputTokens: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	accountDelta := winnerCost - 200000 // Legacy account rollup deduplicates matching request IDs.
	for _, pair := range []struct {
		key, account string
		before       domain.UsageTotals
		delta        int64
	}{
		{"", "", beforeAll, delta}, {"", "acct-a", beforeAccount, accountDelta}, {"key-a", "", beforeKey, delta},
	} {
		after, err := store.UsageTotals(ctx, pair.key, pair.account)
		if err != nil || after.RequestCount != pair.before.RequestCount ||
			after.Usage.CostMicrodollars != pair.before.Usage.CostMicrodollars+pair.delta {
			t.Fatalf("imported total %q/%q double-counted or lost: %+v -> %+v, %v", pair.key, pair.account, pair.before, after, err)
		}
	}
	var foldedCost float64
	if err := store.db.QueryRowContext(ctx, `SELECT sum(cost_usd) FROM legacy_hourly_usage WHERE model='gpt-test'`).Scan(&foldedCost); err != nil || foldedCost != 1 {
		t.Fatalf("folded aggregate silently guessed new price: %g, %v", foldedCost, err)
	}
	entry, err = service.Save(ctx, "gpt-test", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.CostDeltaMicrodollars != 0 ||
		entry.Reprice.PartialFoldedRequests != 8 {
		t.Fatalf("repeat reprice not idempotent: %+v, %v", entry.Reprice, err)
	}
}

func TestModelPricingDoesNotResurrectManuallyResetCostLimit(t *testing.T) {
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
	if err := store.ResetAPIKeyUsage(ctx, "key", fixedTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime.Add(2 * time.Hour) })
	price := pricing.Price{Standard: pricing.Rates{Input: 4000000}}
	entry, err := service.Save(ctx, "gpt-6-sol", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 1 {
		t.Fatalf("historical edit failed: %+v, %v", entry.Reprice, err)
	}
	loaded, err := store.GetAPIKey(ctx, "key")
	if err != nil || len(loaded.Limits) != 1 || loaded.Limits[0].CurrentValue != 0 {
		t.Fatalf("manual cost-limit reset was resurrected: %+v, %v", loaded.Limits, err)
	}
	totals, err := store.UsageTotals(ctx, "key", "")
	if err != nil || totals.Usage.CostMicrodollars != 40 {
		t.Fatalf("historical key total did not reprice: %+v, %v", totals, err)
	}
}

func TestModelPricingRepricesFullyCoveredLegacyHourlyOnce(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC).Unix()
	if _, err := legacy.ExecContext(ctx, `UPDATE request_usage_hourly_rollups SET bucket_epoch=?,
 request_count=2,error_count=1,input_tokens=50,output_tokens=11,reasoning_tokens=3,
 output_or_reasoning_tokens=11,cached_input_tokens=5,cached_input_tokens_clamped=5,
 cost_usd=0,cost_count=2 WHERE model='gpt-test'`, bucket); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `UPDATE request_logs SET cost_usd=0 WHERE id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `UPDATE account_usage_rollup_state SET
 folded_through='2026-09-25 02:00:00.000000',hourly_folded_through='2026-09-25 02:00:00.000000' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, _ := testStore(t)
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	beforeAll, err := store.UsageTotals(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := store.UsageTotals(ctx, "", "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	beforeKey, err := store.UsageTotals(ctx, "key-a", "")
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime })
	price := pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 2000000}}
	entry, err := service.Save(ctx, "gpt-test", price)
	if err != nil || entry.Reprice == nil || entry.Reprice.RecomputedRequests != 2 ||
		entry.Reprice.PartialFoldedRequests != 0 || entry.Reprice.PartialFoldedBuckets != 0 {
		t.Fatalf("fully covered rollup was left partial: %+v, %v", entry.Reprice, err)
	}
	firstCost, err := price.Cost(domain.UsageAmount{InputTokens: 20, OutputTokens: 5, CachedInputTokens: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	secondCost, err := price.Cost(domain.UsageAmount{InputTokens: 30, OutputTokens: 6, CachedInputTokens: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	delta := firstCost + secondCost
	if entry.Reprice.CostDeltaMicrodollars != delta {
		t.Fatalf("zero-cost rows not charged exactly once: %+v", entry.Reprice)
	}
	for _, pair := range []struct {
		key, account string
		before       domain.UsageTotals
		delta        int64
	}{
		{"", "", beforeAll, delta}, {"", "acct-a", beforeAccount, secondCost}, {"key-a", "", beforeKey, delta},
	} {
		after, err := store.UsageTotals(ctx, pair.key, pair.account)
		if err != nil || after.RequestCount != pair.before.RequestCount ||
			after.Usage.CostMicrodollars != pair.before.Usage.CostMicrodollars+pair.delta {
			t.Fatalf("covered rollup scope %q/%q: %+v -> %+v, %v", pair.key, pair.account, pair.before, after, err)
		}
	}
	var foldedCost float64
	if err := store.db.QueryRowContext(ctx, `SELECT cost_usd FROM legacy_hourly_usage WHERE model='gpt-test'`).Scan(&foldedCost); err != nil ||
		int64(foldedCost*1000000+0.5) != delta {
		t.Fatalf("hourly cost not updated once: %g, %v", foldedCost, err)
	}
	keyReport, err := store.KeyUsage7Day(ctx, "key-a", fixedTime.Add(-7*24*time.Hour), fixedTime)
	if err != nil || keyReport.TotalRequests != 2 || keyReport.TotalCostUSD != float64(delta)/1000000 {
		t.Fatalf("key report double-counted covered raw: %+v, %v", keyReport, err)
	}
}
