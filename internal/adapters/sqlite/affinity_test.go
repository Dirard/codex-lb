package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func affinityFixture(t *testing.T) (*Store, func(string, string, string, domain.AffinityKind, int64, time.Time) (bool, error)) {
	t.Helper()
	store, _ := testStore(t)
	saveTestAccount(t, store, "account-a")
	saveTestAccount(t, store, "account-b")
	for index, id := range []string{"key-a", "key-b"} {
		if err := store.SaveAPIKey(context.Background(), domain.APIKey{ID: id, Name: id, IsActive: true,
			KeyHash: strings.Repeat(fmt.Sprint(index), 64), KeyPrefix: "synthetic"}, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	sequence := 0
	save := func(keyID, account, key string, kind domain.AffinityKind, expected int64, at time.Time) (bool, error) {
		t.Helper()
		sequence++
		reservation := fmt.Sprintf("affinity-reservation-%d", sequence)
		if _, err := store.ReserveUsage(context.Background(), domain.ReservationRequest{ID: reservation, APIKeyID: keyID,
			AccountID: account, Model: "gpt-6-sol", Now: at, Budget: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}}); err != nil {
			t.Fatal(err)
		}
		return store.SaveAffinity(context.Background(), domain.AffinityBinding{Key: key, Kind: kind, APIKeyID: keyID,
			AccountID: account, UpdatedAt: at}, expected, reservation)
	}
	return store, save
}

func TestAffinityCASIsKeyScopedAndSurvivesDeleteRecreate(t *testing.T) {
	ctx := context.Background()
	store, save := affinityFixture(t)
	if changed, err := save("key-a", "account-a", "opaque", domain.AffinityPromptCache, 0, fixedTime); err != nil || !changed {
		t.Fatalf("initial affinity: %v %v", changed, err)
	}
	original, err := store.LookupAffinity(ctx, "key-a", domain.AffinityPromptCache, "opaque")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupAffinity(ctx, "key-b", domain.AffinityPromptCache, "opaque"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key-scoped affinity leaked: %v", err)
	}
	if changed, err := save("key-b", "account-b", "opaque", domain.AffinityPromptCache, original.Version, fixedTime); err != nil || changed {
		t.Fatalf("another key overwrote affinity: %v %v", changed, err)
	}
	if changed, err := save("key-a", "account-b", "opaque", domain.AffinityPromptCache, original.Version, fixedTime); err != nil || !changed {
		t.Fatalf("affinity CAS rebind: %v %v", changed, err)
	}
	if changed, err := save("key-a", "account-a", "opaque", domain.AffinityPromptCache, original.Version, fixedTime); err != nil || changed {
		t.Fatalf("stale affinity overwrote winner: %v %v", changed, err)
	}
	identifier := domain.AffinityIdentifier{Key: "opaque", Kind: domain.AffinityPromptCache}
	if result, err := store.DeleteAffinities(ctx, []domain.AffinityIdentifier{identifier}); err != nil || result.DeletedCount != 1 {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if changed, err := save("key-a", "account-a", "opaque", domain.AffinityPromptCache, original.Version, fixedTime); err != nil || changed {
		t.Fatalf("stale refresh recreated an operator-deleted row: %v %v", changed, err)
	}
	if changed, err := save("key-a", "account-b", "opaque", domain.AffinityPromptCache, 0, fixedTime); err != nil || !changed {
		t.Fatalf("fresh request did not recreate locality: %v %v", changed, err)
	}
	if changed, err := save("key-a", "account-a", "opaque", domain.AffinityPromptCache, original.Version, fixedTime); err != nil || changed {
		t.Fatalf("delete/recreate reused the old generation: %v %v", changed, err)
	}
}

func TestAffinityAdminFilteringPurgeAndAccountInvalidationPreserveHardOwners(t *testing.T) {
	ctx := context.Background()
	store, save := affinityFixture(t)
	for _, item := range []struct {
		key     string
		kind    domain.AffinityKind
		updated time.Time
	}{
		{"old_cache", domain.AffinityPromptCache, fixedTime.Add(-2 * time.Hour)},
		{"literal_%", domain.AffinityPromptCache, fixedTime.Add(-time.Hour)},
		{"fresh", domain.AffinityPromptCache, fixedTime},
		{"durable", domain.AffinityCodexSession, fixedTime.Add(-2 * time.Hour)},
		{"sticky", domain.AffinityStickyThread, fixedTime.Add(-2 * time.Hour)},
	} {
		if changed, err := save("key-a", "account-a", item.key, item.kind, 0, item.updated); err != nil || !changed {
			t.Fatalf("seed affinity: %v %v", changed, err)
		}
	}
	page, err := store.ListAffinities(ctx, domain.AffinityFilter{StaleOnly: true, Limit: 1, SortBy: "updated_at", SortDir: "asc"}, fixedTime, 30*time.Minute)
	if err != nil || page.Total != 2 || page.StalePromptCacheCount != 2 || !page.HasMore || len(page.Entries) != 1 || page.Entries[0].Key != "old_cache" || !page.Entries[0].IsStale {
		t.Fatalf("stale filtering/pagination: %+v %v", page, err)
	}
	page, err = store.ListAffinities(ctx, domain.AffinityFilter{KeyQuery: "%", Limit: 100}, fixedTime, 30*time.Minute)
	if err != nil || page.Total != 1 || page.Entries[0].Key != "literal_%" {
		t.Fatalf("search treated user text as SQL pattern: %+v %v", page, err)
	}
	if _, err := store.ListAffinities(ctx, domain.AffinityFilter{SortBy: "updated_at; DELETE FROM accounts", Limit: 10}, fixedTime, time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatal("untrusted sort accepted")
	}
	owner := domain.Continuation{ResponseID: "hard", KeyID: "key-a", AccountID: "account-a", ProviderID: "openai",
		Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.SaveContinuation(ctx, owner, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.PruneAffinities(ctx, fixedTime, 30*time.Minute, 1000); err != nil || count != 2 {
		t.Fatalf("stale prune: %d %v", count, err)
	}
	page, err = store.ListAffinities(ctx, domain.AffinityFilter{Limit: 100}, fixedTime, 30*time.Minute)
	if err != nil || page.Total != 3 || page.StalePromptCacheCount != 0 {
		t.Fatalf("prune removed durable/fresh hints: %+v %v", page, err)
	}
	account, _ := store.GetAccount(ctx, "account-a")
	account.Status = domain.AccountDeactivated
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListAffinities(ctx, domain.AffinityFilter{Limit: 100}, fixedTime, time.Hour)
	if err != nil || page.Total != 0 {
		t.Fatalf("unusable account kept locality: %+v %v", page, err)
	}
	if actual, err := store.GetContinuation(ctx, "key-a", "hard", time.Now()); err != nil || actual.AccountID != owner.AccountID {
		t.Fatalf("affinity cleanup deleted correctness owner: %+v %v", actual, err)
	}
}
