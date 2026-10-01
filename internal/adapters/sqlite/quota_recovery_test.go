package sqlite

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestQuotaRecoveryFencesNewProviderOutcomeAndOperatorPause(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	if err := store.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	refuse := func(id string) {
		t.Helper()
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct", Continuation: true, Model: "gpt-test", Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{RequestID: id, AccountID: "acct", Model: "gpt-test", Status: "error", ErrorCode: "quota_exceeded", RequestedAt: fixedTime}}); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordAccountOutcome(ctx, "acct", id, true, false); err != nil {
			t.Fatal(err)
		}
	}
	refuse("old")
	checkpoint, err := store.CaptureQuotaOutcome(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	refuse("new") // Arrives while the quota fetch is outstanding.
	if recovered, err := store.RecoverAccountQuota(ctx, "acct", checkpoint, 0); err != nil || recovered {
		t.Fatalf("newer refusal overwritten: %v %v", recovered, err)
	}
	latest, err := store.CaptureQuotaOutcome(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverAccountQuota(ctx, "acct", latest, 0); err != nil || !recovered {
		t.Fatalf("confirmed reset failed: %v %v", recovered, err)
	}
	for _, status := range []domain.AccountStatus{domain.AccountPaused, domain.AccountDeactivated, domain.AccountReauthRequired} {
		account, err := store.GetAccount(ctx, "acct")
		if err != nil {
			t.Fatal(err)
		}
		account.Status, account.LastRefresh = status, nil
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if recovered, err := store.RecoverAccountQuota(ctx, "acct", latest, 0); err != nil || recovered {
			t.Fatalf("operator status %s overwritten: %v %v", status, recovered, err)
		}
	}
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	account.Status, account.RequiresEgressDecision = domain.AccountQuotaExceeded, true
	account.CreatedAt = fixedTime.Add(-time.Hour)
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverAccountQuota(ctx, "acct", latest, 0); err != nil || recovered {
		t.Fatalf("egress decision bypassed: %v %v", recovered, err)
	}
}

func TestQuotaRecoveryFencesReimportWithZeroOutcomeVersion(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountQuotaExceeded
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	version, err := store.CaptureQuotaOutcome(ctx, "acct")
	if err != nil || version != 0 {
		t.Fatalf("unexpected old checkpoint: %d %v", version, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE accounts SET generation=1 WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverAccountQuota(ctx, "acct", version, 0); err != nil || recovered {
		t.Fatalf("old zero checkpoint recovered new incarnation: %v %v", recovered, err)
	}
	if recovered, err := store.RecoverAccountQuota(ctx, "acct", version, 1); err != nil || !recovered {
		t.Fatalf("new incarnation could not recover: %v %v", recovered, err)
	}
}
