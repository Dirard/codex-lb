package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestLateOperationalWritesDoNotBindReimportedAccount(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "account-a")
	if err := store.SaveAPIKey(ctx, testKey("key-a", nil), time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := domain.Continuation{ResponseID: "response-old", KeyID: "key-a", AccountID: "account-a",
		ProviderID: "openai", Model: "gpt-test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	bounds := domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1024}
	if err := store.SaveContinuation(ctx, old, bounds); err != nil {
		t.Fatal(err)
	}
	file := application.CodexResourceOwner{ResourceType: application.CodexResourceFile,
		ResourceID: "file-old", KeyID: "key-a", AccountID: "account-a", ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveCodexResourceOwner(ctx, file); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-outcome", "old-affinity"} {
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key-a", AccountID: "account-a",
			Model: "gpt-test", Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SettleUsage(ctx, "old-outcome", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{
		RequestID: "old-outcome", AccountID: "account-a", Model: "gpt-test", Status: "success", RequestedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	// Simulate the account lifecycle transaction: old operational rows are removed
	// before generation advances, while the old reservations remain durable.
	if _, err := store.db.ExecContext(ctx, `UPDATE accounts SET status='deactivated',deactivation_reason='deleted' WHERE id='account-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM continuations WHERE account_id='account-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM codex_resource_owners WHERE account_id='account-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE accounts SET generation=1,status='active',deactivation_reason='' WHERE id='account-a'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContinuation(ctx, old, bounds); !errors.Is(err, ErrConflict) {
		t.Fatalf("old response rebound: %v", err)
	}
	if err := store.SaveCodexResourceOwner(ctx, file); !errors.Is(err, ErrConflict) {
		t.Fatalf("old file rebound: %v", err)
	}
	if changed, err := store.SaveAffinity(ctx, domain.AffinityBinding{Key: "hint", Kind: domain.AffinityPromptCache,
		APIKeyID: "key-a", AccountID: "account-a", UpdatedAt: now}, 0, "old-affinity"); !errors.Is(err, ErrNoAccounts) || changed {
		t.Fatalf("old affinity rebound: changed=%v err=%v", changed, err)
	}
	if err := store.RecordAccountOutcome(ctx, "account-a", "old-outcome", false, true); err != nil {
		t.Fatal(err)
	}
	if account, err := store.GetAccount(ctx, "account-a"); err != nil || account.Status != domain.AccountActive {
		t.Fatalf("old outcome changed new account: %+v %v", account, err)
	}
	if err := store.SaveErrorArchive(ctx, domain.ErrorArchive{RequestID: "old-error", AccountID: "account-a",
		OccurredAt: now, ExpiresAt: now.Add(time.Hour), ContentEncrypted: testCiphertext()}, 1<<20); err != nil {
		t.Fatal(err)
	}
	page, err := store.QueryErrorArchives(ctx, domain.ErrorArchiveFilter{RequestID: "old-error", Limit: 10}, now)
	if err != nil || page.Total != 0 {
		t.Fatalf("old error archive returned: %+v %v", page, err)
	}
	old.AccountGeneration, old.ResponseID = 1, "response-new"
	if err := store.SaveContinuation(ctx, old, bounds); err != nil {
		t.Fatalf("new continuation denied: %v", err)
	}
	file.AccountGeneration, file.ResourceID = 1, "file-new"
	if err := store.SaveCodexResourceOwner(ctx, file); err != nil {
		t.Fatalf("new file denied: %v", err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "new-affinity", APIKeyID: "key-a", AccountID: "account-a",
		AccountGeneration: 1, Model: "gpt-test", Now: now}); err != nil {
		t.Fatalf("new reservation denied: %v", err)
	}
	if changed, err := store.SaveAffinity(ctx, domain.AffinityBinding{Key: "hint", Kind: domain.AffinityPromptCache,
		APIKeyID: "key-a", AccountID: "account-a", UpdatedAt: now}, 0, "new-affinity"); err != nil || !changed {
		t.Fatalf("new affinity denied: changed=%v err=%v", changed, err)
	}
}
