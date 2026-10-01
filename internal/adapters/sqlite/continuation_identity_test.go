package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestTurnAliasesPreserveSourceAndGenerationAcrossRestart(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	for _, account := range []string{"acct-a", "acct-b"} {
		saveTestAccount(t, s, account)
	}
	for _, key := range []string{"key-a", "key-b"} {
		if err := s.SaveAPIKey(ctx, testKey(key, nil), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	reserve := func(id, account string) {
		t.Helper()
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key-a", AccountID: account,
			Model: "gpt-test", Continuation: true, Budget: domain.UsageAmount{InputTokens: 1}, Now: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
			RequestID: id, AccountID: account, Model: "gpt-test", Status: "success", RequestedAt: now}}); err != nil {
			t.Fatal(err)
		}
	}
	old := domain.Continuation{ResponseID: "session:turn:opaque", KeyID: "key-a", AccountID: "acct-a", ProviderID: "openai",
		Model: "gpt-test", CreatedAt: now, ExpiresAt: now.Add(time.Hour), TurnStateForwardable: true}
	bounds := domain.ContinuationBounds{MaxRecords: 20, MaxContextBytes: 1 << 20}
	reserve("old-generation", old.AccountID)
	if err := s.SaveSessionContinuation(ctx, old, "old-generation", bounds); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetContinuation(ctx, old.KeyID, old.ResponseID, now); err != nil || !got.TurnStateForwardable {
		t.Fatalf("upstream token source was lost: %+v %v", got, err)
	}
	current := old
	current.AccountID, current.TurnStateForwardable = "acct-b", false
	reserve("new-generation", current.AccountID)
	if err := s.SaveSessionContinuation(ctx, current, "new-generation", bounds); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSessionContinuation(ctx, old, "old-generation", bounds); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContinuation(ctx, old, bounds); !errors.Is(err, ErrInvalid) {
		t.Fatalf("provider response could forge an internal alias: %v", err)
	}
	old.ResponseID = "session:thread:thread"
	if err := s.SaveSessionContinuation(ctx, old, "old-generation", bounds); !errors.Is(err, ErrInvalid) {
		t.Fatalf("turn-state discriminator accepted for a thread alias: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetContinuation(ctx, current.KeyID, current.ResponseID, now)
	if err != nil || got.AccountID != "acct-b" || got.TurnStateForwardable {
		t.Fatalf("stale completion/restart restored the old upstream token: %+v %v", got, err)
	}
	if _, err := reopened.GetContinuation(ctx, "key-b", current.ResponseID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("turn alias crossed API-key scope: %v", err)
	}
}
