package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// seedPricedHistory settles n gpt-6-sol requests of key "key" on account "acct".
func seedPricedHistory(t *testing.T, store *Store, n int) {
	t.Helper()
	ctx := context.Background()
	saveTestAccount(t, store, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 1 << 40}}
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < n; i += 32 {
				id := fmt.Sprintf("seed-%05d", i)
				if err := reserveAndSettle(ctx, store, id, fixedTime.Add(time.Duration(i)*time.Millisecond)); err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func reserveAndSettle(ctx context.Context, store *Store, id string, at time.Time) error {
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
		Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 10, OutputTokens: 10}, Now: at}); err != nil {
		return fmt.Errorf("reserve %s: %w", id, err)
	}
	if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
		RequestID: "event-" + id, APIKeyID: "key", AccountID: "acct", Model: "gpt-6-sol", RequestKind: "normal",
		Status: "success", RequestedAt: at, Usage: domain.UsageAmount{InputTokens: 1000, OutputTokens: 100}}}); err != nil {
		return fmt.Errorf("settle %s: %w", id, err)
	}
	return nil
}

func assertLedgerMatchesEvents(t *testing.T, store *Store, price pricing.Price) {
	t.Helper()
	ctx := context.Background()
	expected, err := price.Cost(domain.UsageAmount{InputTokens: 1000, OutputTokens: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	var stale, events, eventCost int64
	if err := store.readDB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(cost_microdollars!=?),0),coalesce(sum(cost_microdollars),0)
 FROM usage_events`, expected).Scan(&events, &stale, &eventCost); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("%d of %d events keep an obsolete price", stale, events)
	}
	for _, scope := range []struct{ key, account string }{{"", ""}, {"key", ""}, {"", "acct"}} {
		totals, err := store.UsageTotals(ctx, scope.key, scope.account)
		if err != nil || totals.RequestCount != events || totals.Usage.CostMicrodollars != eventCost {
			t.Fatalf("scope %+v totals %+v diverge from %d events costing %d: %v", scope, totals, events, eventCost, err)
		}
	}
	var pending int
	if err := store.readDB.QueryRowContext(ctx, `SELECT count(*) FROM model_reprice_pending`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("reprice still pending: %d %v", pending, err)
	}
}

// A long history reprice must neither fail nor starve concurrent reservations
// and settlements: they interleave with its bounded chunks.
func TestReserveAndSettleKeepWorkingDuringLongReprice(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	seedPricedHistory(t, store, 6*repriceRawChunk)
	service := application.NewModelPricingService(store, func() time.Time { return fixedTime.Add(24 * time.Hour) })
	price := pricing.Price{Standard: pricing.Rates{Input: 7000000, Cached: 700000, Output: 21000000}}
	type finished struct {
		at  time.Time
		err error
	}
	done := make(chan finished, 1)
	started := time.Now()
	go func() {
		entry, err := service.Save(ctx, "gpt-6-sol", price)
		if err == nil && (entry.Reprice == nil || entry.Reprice.RecomputedRequests < int64(6*repriceRawChunk)) {
			err = fmt.Errorf("incomplete reprice summary: %+v", entry.Reprice)
		}
		done <- finished{time.Now(), err}
	}()
	var completed []time.Time
	for i := 0; ; i++ {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			during := 0
			for _, at := range completed {
				if at.Before(result.at) {
					during++
				}
			}
			t.Logf("reprice of %d events took %v; %d reserve+settle pairs completed meanwhile", 6*repriceRawChunk, result.at.Sub(started), during)
			// One blocked pair could finish right after a monolithic commit; a
			// starving writer cannot complete several.
			if during < 3 {
				t.Fatalf("ledger starved during reprice: %d pairs completed", during)
			}
			assertLedgerMatchesEvents(t, store, price)
			return
		default:
		}
		if err := reserveAndSettle(ctx, store, fmt.Sprintf("live-%05d", i), fixedTime.Add(time.Hour)); err != nil {
			t.Fatalf("ledger write failed during reprice: %v", err)
		}
		completed = append(completed, time.Now())
	}
}

// simulateInterruptedReprice saves price as the first job of
// ApplyModelPriceChange does and leaves its history pass pending, as a crash
// right after that job would.
func simulateInterruptedReprice(t *testing.T, store *Store, price pricing.Price) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(price)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO codex_price_overrides(model,price_json,updated_at)
 VALUES('gpt-6-sol',?,?)`, string(raw), millis(fixedTime)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO model_reprice_pending(model,requested_at) VALUES('gpt-6-sol',?)`, millis(fixedTime)); err != nil {
		t.Fatal(err)
	}
}

func pendingReprices(t *testing.T, store *Store) int {
	t.Helper()
	var pending int
	if err := store.readDB.QueryRow(`SELECT count(*) FROM model_reprice_pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	return pending
}

// A tariff saved before a crash has its history repriced when the process
// that owns background work resumes it, not by merely opening the database:
// a worker opened beside an active one must not start background work.
func TestInterruptedRepriceResumesOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	seedPricedHistory(t, store, 2*repriceRawChunk+7)
	price := pricing.Price{Standard: pricing.Rates{Input: 9000000, Cached: 900000, Output: 27000000}}
	simulateInterruptedReprice(t, store, price)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if pending := pendingReprices(t, reopened); pending != 1 {
		t.Fatalf("open changed pending reprices: %d", pending)
	}
	expected, err := price.Cost(domain.UsageAmount{InputTokens: 1000, OutputTokens: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	var repriced int
	if err := reopened.readDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_events WHERE cost_microdollars=?`, expected).Scan(&repriced); err != nil || repriced != 0 {
		t.Fatalf("open repriced history: %d %v", repriced, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := reopened.ResumeModelReprices(cancelled); !errors.Is(err, context.Canceled) || pendingReprices(t, reopened) != 1 {
		t.Fatalf("cancelled resumption: %v", err)
	}
	if err := reopened.ResumeModelReprices(ctx); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := reopened.GetModelPriceOverride(ctx, "gpt-6-sol")
	if err != nil || !ok || stored.Standard.Output != price.Standard.Output {
		t.Fatalf("override fixture: %+v %v %v", stored, ok, err)
	}
	assertLedgerMatchesEvents(t, reopened, price)
	// Repeating a finished pass changes nothing.
	summary := application.ModelRepriceSummary{}
	if err := reopened.repriceModelHistory(ctx, "gpt-6-sol", 0, &summary); err != nil || summary.CostDeltaMicrodollars != 0 {
		t.Fatalf("repeated reprice changed costs: %+v %v", summary, err)
	}
	assertLedgerMatchesEvents(t, reopened, price)
}

// The dry run rejects a reconciliation failure before the tariff is saved.
func TestRepriceDryRunFailureLeavesTariffUnchanged(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	seedPricedHistory(t, store, 3)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM usage_totals WHERE scope='key'`); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyModelPriceChange(ctx, "gpt-6-sol", &pricing.Price{Standard: pricing.Rates{Input: 1}}, fixedTime)
	if err == nil {
		t.Fatal("reprice without key totals succeeded")
	}
	if _, ok, err := store.GetModelPriceOverride(ctx, "gpt-6-sol"); err != nil || ok {
		t.Fatalf("failed dry run saved the tariff: %v %v", ok, err)
	}
	var pending int
	if err := store.readDB.QueryRowContext(ctx, `SELECT count(*) FROM model_reprice_pending`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("failed dry run left pending work: %d %v", pending, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
