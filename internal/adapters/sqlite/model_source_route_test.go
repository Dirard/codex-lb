package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestModelSourceEditFencesLateOwnersWithoutLosingSettlement(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	source := domain.ModelSource{ID: "route-source", Name: "Source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://old.example.invalid/v1", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}}}
	if err := s.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: testCiphertext()}); err != nil {
		t.Fatal(err)
	}
	key := testKey("route-key", nil)
	key.SourceAssignmentScopeEnabled, key.AssignedSourceIDs = true, []string{source.ID}
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "old-attempt", APIKeyID: key.ID,
		AccountID: source.ID, Model: "model", Now: now}); err != nil {
		t.Fatal(err)
	}
	c := domain.Continuation{ResponseID: "old-response", KeyID: key.ID, AccountID: source.ID,
		ProviderID: source.ID, Model: "model", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	bounds := domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1000}
	if err := s.SaveContinuation(ctx, c, bounds); err != nil {
		t.Fatal(err)
	}
	source.BaseURL = "https://new.example.invalid/v1"
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	// This call already ran on the old endpoint: retiring its route must not
	// discard usage or permit a second settlement.
	settlement := domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{
		RequestID: "old-attempt", AccountID: source.ID, Model: "model", Status: "error", RequestedAt: now,
		Usage: domain.UsageAmount{InputTokens: 7, OutputTokens: 3}}}
	for range 2 {
		if _, err := s.SettleUsage(ctx, "old-attempt", settlement); err != nil {
			t.Fatalf("retired route lost its bill: %v", err)
		}
	}
	var events, input, output int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),sum(input_tokens),sum(output_tokens) FROM usage_events WHERE request_id='old-attempt'`).Scan(&events, &input, &output); err != nil || events != 1 || input != 7 || output != 3 {
		t.Fatalf("settlement not retained exactly once: %d %d %d %v", events, input, output, err)
	}
	if err := s.SaveContinuation(ctx, c, bounds); !errors.Is(err, ErrConflict) {
		t.Errorf("late old-route response restored owner: %v", err)
	}
	c.ResponseID = "session:old-session"
	if err := s.SaveSessionContinuation(ctx, c, "old-attempt", bounds); !errors.Is(err, ErrConflict) {
		t.Errorf("late old-route response restored session: %v", err)
	}
	if err := s.RecordAccountOutcome(ctx, source.ID, "old-attempt", true, false); err != nil {
		t.Fatal(err)
	}
	account, err := s.GetAccount(ctx, source.ID)
	if err != nil || account.Status != domain.AccountActive {
		t.Errorf("old quota outcome blocked new route: %+v %v", account, err)
	}
	source.BaseURL = "https://old.example.invalid/v1"
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	c.ResponseID = "old-response"
	if err := s.SaveContinuation(ctx, c, bounds); !errors.Is(err, ErrConflict) {
		t.Errorf("A-to-B-to-A restored old owner: %v", err)
	}
}

func TestModelSourceMetadataEditPreservesQuotaStatus(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	source := domain.ModelSource{ID: "metadata-source", Name: "Source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://provider.example.invalid/v1", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}}}
	if err := s.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: testCiphertext()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAPIKey(ctx, testKey("metadata-key", nil), now); err != nil {
		t.Fatal(err)
	}
	c := domain.Continuation{ResponseID: "metadata-response", KeyID: "metadata-key", AccountID: source.ID,
		ProviderID: source.ID, Model: "model", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.SaveContinuation(ctx, c, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET status='quota_exceeded' WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	source.Name = "Renamed"
	price := 2.0
	source.Models[0].InputPerMillion = &price
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	account, err := s.GetAccount(ctx, source.ID)
	if err != nil || account.Status != domain.AccountQuotaExceeded {
		t.Fatalf("metadata save cleared quota status: %+v %v", account, err)
	}
	if got, err := s.GetContinuation(ctx, c.KeyID, c.ResponseID, time.Now()); err != nil || got.RouteRevision != c.RouteRevision || got.AccountGeneration != c.AccountGeneration {
		t.Fatalf("metadata save broke the current conversation: %+v %v", got, err)
	}
}
