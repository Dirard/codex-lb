package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func finalizeFixture(t *testing.T) (*Store, func(string) domain.ResponseFinalization) {
	t.Helper()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(1000)}
	if err := store.SaveAPIKey(context.Background(), key, fixedTime); err != nil {
		t.Fatal(err)
	}
	plan := func(id string) domain.ResponseFinalization {
		t.Helper()
		if _, err := store.ReserveUsage(context.Background(), domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
			Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 100}, Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		owner := domain.Continuation{ResponseID: "resp-" + id, KeyID: "key", AccountID: "acct", ProviderID: "openai", Model: "gpt-test",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), ContextEncrypted: testCiphertext()}
		alias := owner
		alias.ResponseID = "session:" + id
		event := usageEvent("event-"+id, 10)
		event.APIKeyID, event.AccountID = "key", "acct"
		return domain.ResponseFinalization{ReservationID: id,
			Settlement: domain.UsageSettlement{Status: "finalized", Event: event},
			Outcome:    &domain.AccountOutcome{AccountID: "acct", Success: true},
			Continuation: &owner, Aliases: []domain.Continuation{alias},
			Bounds: domain.ContinuationBounds{MaxRecords: 100, MaxContextBytes: 1 << 20}}
	}
	return store, plan
}

func TestFinalizeResponseCommitsSettlementOutcomeAndOwnershipTogether(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	result, err := store.FinalizeResponse(ctx, plan("ok"))
	if err != nil || result.OutcomeErr != nil || result.ContinuationErr != nil {
		t.Fatalf("finalize: %+v %v", result, err)
	}
	if reservation, err := store.GetReservation(ctx, "ok"); err != nil || reservation.Status != "finalized" {
		t.Fatalf("settlement: %+v %v", reservation, err)
	}
	for _, id := range []string{"resp-ok", "session:ok"} {
		if owner, err := store.GetContinuation(ctx, "key", id, time.Now()); err != nil || owner.AccountID != "acct" {
			t.Fatalf("ownership %s: %+v %v", id, owner, err)
		}
	}
	var outcomes int
	if err := store.readDB.QueryRow(`SELECT count(*) FROM account_outcomes WHERE account_id='acct'`).Scan(&outcomes); err != nil || outcomes != 1 {
		t.Fatalf("account outcome: %d %v", outcomes, err)
	}
	if key, err := store.GetAPIKey(ctx, "key"); err != nil || key.Limits[0].CurrentValue != 10 {
		t.Fatalf("settled budget: %+v %v", key.Limits, err)
	}
}

// Ownership failure must not undo the settlement before it.
func TestFinalizeResponseIsolatesOwnershipFailureFromSettlement(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	f := plan("conflict")
	taken := *f.Continuation
	taken.Model = "another-model"
	if err := store.SaveContinuation(ctx, taken, f.Bounds); err != nil {
		t.Fatal(err)
	}
	result, err := store.FinalizeResponse(ctx, f)
	if err != nil || result.OutcomeErr != nil || !errors.Is(result.ContinuationErr, ErrConflict) {
		t.Fatalf("ownership conflict not isolated: %+v %v", result, err)
	}
	if reservation, err := store.GetReservation(ctx, "conflict"); err != nil || reservation.Status != "finalized" {
		t.Fatalf("settlement rolled back with ownership: %+v %v", reservation, err)
	}
	if _, err := store.GetContinuation(ctx, "key", "session:conflict", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("alias published without its owner: %v", err)
	}
	if owner, err := store.GetContinuation(ctx, "key", "resp-conflict", time.Now()); err != nil || owner.Model != "another-model" {
		t.Fatalf("existing owner overwritten: %+v %v", owner, err)
	}
}

// Ownership is never published without its account outcome.
func TestFinalizeResponseSkipsOwnershipWhenOutcomeFails(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	f := plan("outcome")
	f.Outcome = &domain.AccountOutcome{AccountID: "acct", QuotaRefused: true} // a finalized bill cannot prove quota refusal
	result, err := store.FinalizeResponse(ctx, f)
	if err != nil || !errors.Is(result.OutcomeErr, ErrInvalid) || result.ContinuationErr != nil {
		t.Fatalf("outcome failure not isolated: %+v %v", result, err)
	}
	if reservation, err := store.GetReservation(ctx, "outcome"); err != nil || reservation.Status != "finalized" {
		t.Fatalf("settlement rolled back with outcome: %+v %v", reservation, err)
	}
	if _, err := store.GetContinuation(ctx, "key", "resp-outcome", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ownership published without outcome: %v", err)
	}
}

func TestFinalizeResponseSettlementFailurePersistsNothing(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	f := plan("settled-twice")
	f.ReservationID = "missing"
	if _, err := store.FinalizeResponse(ctx, f); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing reservation accepted: %v", err)
	}
	if _, err := store.GetContinuation(ctx, "key", "resp-settled-twice", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ownership published without settlement: %v", err)
	}
}

func TestFinalizeResponseRetainsUncertainUsageWithQuotaOutcome(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	f := plan("uncertain")
	f.RetainUncertain, f.Continuation, f.Aliases = true, nil, nil
	f.Outcome = &domain.AccountOutcome{AccountID: "acct", QuotaRefused: true}
	result, err := store.FinalizeResponse(ctx, f)
	if err != nil || !result.Retained || result.OutcomeErr != nil {
		t.Fatalf("retain: %+v %v", result, err)
	}
	if reservation, err := store.GetReservation(ctx, "uncertain"); err != nil || reservation.Status != "reserved" || !reservation.NeedsReconciliation {
		t.Fatalf("uncertain usage not retained: %+v %v", reservation, err)
	}
	if account, err := store.GetAccount(ctx, "acct"); err != nil || account.Status != domain.AccountQuotaExceeded {
		t.Fatalf("quota outcome: %+v %v", account, err)
	}
	if again, err := store.FinalizeResponse(ctx, f); err != nil || !again.Retained {
		t.Fatalf("retain is not idempotent: %+v %v", again, err)
	}
	if _, err := store.SettleUsage(ctx, "uncertain", f.Settlement); err != nil {
		t.Fatal(err)
	}
	if after, err := store.FinalizeResponse(ctx, f); err != nil || after.Retained {
		t.Fatalf("settled reservation reported retained: %+v %v", after, err)
	}
}
