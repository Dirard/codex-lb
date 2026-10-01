package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRetainedQuotaOutcomeRequiresDurableDecisionAndKeepsFences(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	owner := domain.Continuation{ResponseID: "resp_owner", KeyID: "key", AccountID: "acct", ProviderID: "openai", Model: "gpt-test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.SaveContinuation(ctx, owner, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	reserve := func(id string) {
		t.Helper()
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct", Model: "gpt-test", Continuation: true, Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	reserve("unknown")
	if err := s.RecordAccountOutcome(ctx, "acct", "unknown", true, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("live reservation authorized quota state")
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key", owner.ResponseID, "acct", "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatal("live reservation authorized owner state")
	}
	if marked, err := s.MarkReservationUncertain(ctx, "unknown"); err != nil || !marked {
		t.Fatalf("retain: %v %v", marked, err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "unknown", false, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown usage authorized healthy outcome")
	}
	if err := s.RecordAccountOutcome(ctx, "other", "unknown", true, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("quota state crossed accounts")
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "unknown", true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key", owner.ResponseID, "acct", "unknown"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetContinuation(ctx, "key", owner.ResponseID, now); err != nil || !got.QuotaRefused {
		t.Fatalf("retained owner quota lost: %+v %v", got, err)
	}
	account, _ := s.GetAccount(ctx, "acct")
	if account.Status != domain.AccountQuotaExceeded {
		t.Fatal("retained quota proof was lost")
	}
	if _, err := s.SettleUsage(ctx, "unknown", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "unknown", AccountID: "acct", Model: "gpt-test", Status: "success", RequestedAt: now, Usage: domain.UsageAmount{InputTokens: 3}}}); err != nil {
		t.Fatal(err)
	}
	if at, err := s.LoadAccountQuotaRefusalAt(ctx, "acct"); err != nil || at == nil {
		t.Fatalf("reconciled bill erased quota proof: %v %v", at, err)
	}
	reserve("late-refusal")
	if _, err := s.MarkReservationUncertain(ctx, "late-refusal"); err != nil {
		t.Fatal(err)
	}
	reserve("new-success")
	if _, err := s.SettleUsage(ctx, "new-success", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "new-success", AccountID: "acct", Model: "gpt-test", Status: "success", RequestedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "new-success", false, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "late-refusal", true, false); err != nil {
		t.Fatal(err)
	}
	account, _ = s.GetAccount(ctx, "acct")
	if account.Status != domain.AccountActive {
		t.Fatal("older retained quota overwrote newer success")
	}
	owner.ResponseID = "session:newer"
	if err := s.SaveSessionContinuation(ctx, owner, "new-success", domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key", owner.ResponseID, "acct", "late-refusal"); err != nil {
		t.Fatal(err)
	}
	if current, err := s.GetContinuation(ctx, "key", owner.ResponseID, now); err != nil || current.QuotaRefused {
		t.Fatalf("retained old refusal overwrote a new session generation: %+v %v", current, err)
	}
}
