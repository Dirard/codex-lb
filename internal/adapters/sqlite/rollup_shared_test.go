package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// settleRollupEvent settles one request of account on key "key" through the
// schema 41 settlement path of store.
func settleRollupEvent(t *testing.T, store *Store, id, account, conversation string, at time.Time, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: account,
		Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 10}, Now: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
		RequestID: "event-" + id, APIKeyID: "key", AccountID: account, Model: "gpt-6-sol", RequestKind: "normal",
		Status: status, ErrorCode: map[bool]string{true: "upstream_error"}[status == "error"], RequestedAt: at,
		ConversationID: conversation, UserAgentGroup: "codex", FirstTokenMS: 300, TotalLatencyMS: 2000,
		Usage: domain.UsageAmount{InputTokens: 1000, OutputTokens: 120, CachedInputTokens: 200}}}); err != nil {
		t.Fatal(err)
	}
}

// Store A folds; store B never folds and stands in for a schema 41 worker
// during an update overlap: it runs only statements schema 41 has
// (settlement, repricing, retention, account history cleanup). After every
// step that A folds, the rollups equal the raw events.
func TestUsageRollupsStayConsistentWithAnOlderWorker(t *testing.T) {
	ctx := context.Background()
	active, path := testStore(t)
	old := openPeerStore(t, path, Options{})
	seedPricedHistory(t, old, 40)
	for _, account := range []string{"anonymized", "discarded"} {
		saveTestAccount(t, old, account)
	}
	step := 0
	settle := func(account, conversation string, at time.Time, status string) {
		step++
		settleRollupEvent(t, old, fmt.Sprintf("overlap-%d", step), account, conversation, at, status)
	}
	foldRollups(t, active, 16)
	assertRollupsMatchRaw(t, active)
	for i := range 6 {
		settle("anonymized", "conv-a", fixedTime.Add(time.Duration(i)*20*time.Minute), "success")
		settle("discarded", " conv-d ", fixedTime.Add(time.Duration(i)*25*time.Minute), []string{"success", "error", "cancelled"}[i%3])
	}
	// The active worker folds part of the overlap's inserts mid-way.
	if _, err := active.FoldUsageRollups(ctx, 5, 5); err != nil {
		t.Fatal(err)
	}
	settle("acct", "conv-a", fixedTime.Add(3*time.Hour), "success")
	foldRollups(t, active, 16)
	assertRollupsMatchRaw(t, active)

	if _, err := old.ApplyModelPriceChange(ctx, "gpt-6-sol", &pricing.Price{Standard: pricing.Rates{Input: 2, Output: 9}}, fixedTime); err != nil {
		t.Fatal(err)
	}
	settle("acct", "conv-b", fixedTime.Add(4*time.Hour), "success")
	if fold := foldRollups(t, active, 16); fold.Journal == 0 {
		t.Fatal("repricing journaled nothing")
	}
	assertRollupsMatchRaw(t, active)

	if err := old.DeleteAccount(ctx, "anonymized", false); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, active, 16)
	assertRollupsMatchRaw(t, active)
	if err := old.DeleteAccount(ctx, "discarded", true); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, active, 16)
	assertRollupsMatchRaw(t, active)

	if n, err := old.FoldAndPruneRequestLogs(ctx, fixedTime.Add(50*time.Minute), 30); err != nil || n == 0 {
		t.Fatalf("retention %d %v", n, err)
	}
	settle("acct", "conv-a", fixedTime.Add(time.Hour), "success")
	foldRollups(t, active, 16)
	assertRollupsMatchRaw(t, active)
}

// A rollback to schema 41 runs no folder for a long time: the journal grows
// with every change of folded events and inserts stay in the tail. The next
// active worker drains both to the raw events.
func TestUsageRollupsDrainALongRollback(t *testing.T) {
	ctx := context.Background()
	active, path := testStore(t)
	old := openPeerStore(t, path, Options{})
	seedPricedHistory(t, old, 300)
	foldRollups(t, active, 1000)
	for i := range 50 {
		settleRollupEvent(t, old, fmt.Sprintf("rollback-%d", i), "acct", fmt.Sprintf("conv-%d", i%4),
			fixedTime.Add(time.Duration(i)*time.Minute), "success")
	}
	// Rates are microdollars per million tokens: both prices change the cost
	// of every event, so each journals one cost row per folded event. (Rates
	// of a few microdollars per million round every event to 0, and the second
	// change would reprice nothing.)
	for _, price := range []pricing.Rates{{Input: 2_000_000, Output: 9_000_000}, {Input: 3_000_000, Output: 1_000_000}} {
		if _, err := old.ApplyModelPriceChange(ctx, "gpt-6-sol", &pricing.Price{Standard: price}, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	// The seed's first hundred events and the first rollback event.
	if n, err := old.FoldAndPruneRequestLogs(ctx, fixedTime.Add(100*time.Millisecond), 500); err != nil || n != 101 {
		t.Fatalf("retention %d %v", n, err)
	}
	var journal, tail int
	if err := active.readDB.QueryRow(`SELECT (SELECT count(*) FROM usage_rollup_journal),
 (SELECT count(*) FROM usage_events WHERE rowid>(SELECT folded_rowid FROM usage_rollup_state))`).Scan(&journal, &tail); err != nil {
		t.Fatal(err)
	}
	// 300 folded events repriced twice and 100 folded events pruned; the
	// pruned rollback event and the repriced ones were never folded.
	if journal != 700 || tail != 49 {
		t.Fatalf("rollback left journal %d tail %d", journal, tail)
	}
	if fold := foldRollups(t, active, 64); fold.Journal != journal || fold.Rows != tail {
		t.Fatalf("drain %+v, journal %d tail %d", fold, journal, tail)
	}
	assertRollupsMatchRaw(t, active)
}
