package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestModelSourceRepositoryAndOwnerInvalidation(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	source := domain.ModelSource{
		ID: "source-test", Name: "Source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://provider.example.invalid/v1", Enabled: true, Health: "unknown",
		Chat: true, Responses: true, CreatedAt: now, UpdatedAt: now,
		Models: []domain.ModelSourceModel{{Model: "model-test", Streaming: true, Tools: true, Enabled: true}},
	}
	credential := &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: testCiphertext()}
	if err := s.SaveModelSource(ctx, source, credential); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelSource(ctx, source.ID)
	if err != nil || len(got.Models) != 1 || got.Models[0].Model != "model-test" || !got.Responses {
		t.Fatalf("model source roundtrip: %+v, %v", got, err)
	}
	key := testKey("key-model", nil)
	key.SourceAssignmentScopeEnabled, key.AssignedSourceIDs = true, []string{source.ID}
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.EligibleAccounts(ctx, "key-model")
	if err != nil || len(accounts) != 1 || accounts[0].ID != source.ID {
		t.Fatalf("explicit external source unavailable: %+v, %v", accounts, err)
	}
	c := domain.Continuation{ResponseID: "response", KeyID: "key-model", AccountID: source.ID,
		ProviderID: source.ID, Model: "model-test", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		ContextEncrypted: testCiphertext()}
	if err := s.SaveContinuation(ctx, c, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 10000}); err != nil {
		t.Fatal(err)
	}
	source.Name, source.TimeoutSeconds, source.MaxConcurrency = "Renamed", 5, 2
	price := 3.0
	source.Models[0].InputPerMillion = &price
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContinuation(ctx, "key-model", "response", time.Now()); err != nil {
		t.Fatalf("metadata edit invalidated current owner: %v", err)
	}
	if account, err := s.GetAccount(ctx, source.ID); err != nil || account.RouteRevision != 0 {
		t.Fatalf("metadata edit changed route revision: %+v %v", account, err)
	}
	source.BaseURL = "https://other.example.invalid/v1"
	source.UpdatedAt = now.Add(time.Minute)
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContinuation(ctx, "key-model", "response", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old owner context survived endpoint change: %v", err)
	}
	if err := s.DeleteModelSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetModelSource(ctx, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted source still visible: %v", err)
	}
	account, err := s.GetAccount(ctx, source.ID)
	if err != nil || account.Status != domain.AccountDeactivated {
		t.Fatalf("deleted source routable: %+v, %v", account, err)
	}
	var credentials int
	if err := s.db.QueryRow("SELECT count(*) FROM account_credentials WHERE account_id=?", source.ID).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("deleted external credential retained: %d %v", credentials, err)
	}
	key, err = s.GetAPIKey(ctx, key.ID)
	if err != nil || !key.SourceAssignmentScopeEnabled || len(key.AssignedSourceIDs) != 0 {
		t.Fatalf("deleted source opened key scope: %+v %v", key, err)
	}
	key.AssignedSourceIDs = []string{source.ID}
	if err := s.SaveAPIKey(ctx, key, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale key save restored deleted source assignment: %v", err)
	}
	if err := s.SaveModelSource(ctx, source, credential); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale source save resurrected deletion: %v", err)
	}
}

func TestModelSourceProtocolChangeAdvancesRouteRevision(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	source := domain.ModelSource{ID: "source-protocol", Name: "Protocol", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://provider.example.invalid/v1", Enabled: true, Chat: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}}}
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	for revision, responses := range []bool{false, true} {
		source.Responses = responses
		if err := s.SaveModelSource(ctx, source, nil); err != nil {
			t.Fatal(err)
		}
		account, err := s.GetAccount(ctx, source.ID)
		if err != nil || account.RouteRevision != int64(revision+1) {
			t.Fatalf("protocol edit reused route identity: %+v %v", account, err)
		}
	}
	account, err := s.GetAccount(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	account.Status, account.RequiresEgressDecision = domain.AccountPaused, true
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	source.BaseURL = "https://new.example.invalid/v1"
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	account, err = s.GetAccount(ctx, source.ID)
	if err != nil || account.RouteRevision != 3 || account.Status != domain.AccountPaused || !account.RequiresEgressDecision {
		t.Fatalf("route edit removed operator policy: %+v %v", account, err)
	}
}
