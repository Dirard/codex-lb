package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestGroupKeysKeepExternalSourcesOutsideAccountGroup(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	saveTestAccount(t, s, "inside-chatgpt")
	saveTestAccount(t, s, "outside-chatgpt")
	group := domain.AccountGroup{ID: "chatgpt-group", Name: "ChatGPT", AccountIDs: []string{"inside-chatgpt"}}
	if err := s.SaveGroup(ctx, group, now); err != nil {
		t.Fatal(err)
	}
	source := domain.ModelSource{
		ID: "external-source", Name: "External", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://provider.example.invalid/v1", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "glm-5.3", Enabled: true}},
	}
	if err := s.SaveModelSource(ctx, source, &domain.AccountCredential{
		AccountID: source.ID, ExternalKeyEncrypted: testCiphertext(),
	}); err != nil {
		t.Fatal(err)
	}
	key := testKey("grouped-key", &group.ID)
	key.AllowedModels = []string{"glm-5.3"}
	key.AccountAssignmentScopeEnabled = true
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}

	accounts, err := s.EligibleAccounts(ctx, key.ID)
	if err != nil || len(accounts) != 2 || accounts[0].ID != source.ID || accounts[1].ID != "inside-chatgpt" {
		t.Fatalf("eligible accounts = %+v, err = %v", accounts, err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, key.ID, source.ID, now); err != nil {
		t.Fatalf("group blocked external owner: %v", err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, key.ID, "outside-chatgpt", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("outside-group owner admitted: %v", err)
	}
	reservation := domain.ReservationRequest{ID: "external-reservation", APIKeyID: key.ID,
		AccountID: source.ID, Model: "glm-5.3", Budget: domain.UsageAmount{InputTokens: 1}, Now: now}
	if _, err := s.ReserveUsage(ctx, reservation); err != nil {
		t.Fatalf("group blocked external reservation: %v", err)
	}
	reservation.ID, reservation.AccountID = "outside-reservation", "outside-chatgpt"
	if _, err := s.ReserveUsage(ctx, reservation); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("outside-group reservation admitted: %v", err)
	}

	key.SourceAssignmentScopeEnabled = true
	if err := s.SaveAPIKey(ctx, key, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	accounts, err = s.EligibleAccounts(ctx, key.ID)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "inside-chatgpt" {
		t.Fatalf("source-scoped eligible accounts = %+v, err = %v", accounts, err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, key.ID, source.ID, now.Add(time.Minute)); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("denied source owner admitted: %v", err)
	}
	reservation.ID, reservation.AccountID, reservation.Now = "denied-reservation", source.ID, now.Add(time.Minute)
	if _, err := s.ReserveUsage(ctx, reservation); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("denied source reservation admitted: %v", err)
	}

	group.AccountIDs = nil
	if err := s.SaveGroup(ctx, group, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	key.SourceAssignmentScopeEnabled = false
	if err := s.SaveAPIKey(ctx, key, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	accounts, err = s.EligibleAccounts(ctx, key.ID)
	if err != nil || len(accounts) != 1 || accounts[0].ID != source.ID {
		t.Fatalf("empty ChatGPT group eligible accounts = %+v, err = %v", accounts, err)
	}
	reservation.ID, reservation.AccountID, reservation.Now = "empty-group-reservation", "", now.Add(2*time.Minute)
	if _, err := s.ReserveUsage(ctx, reservation); err != nil {
		t.Fatalf("empty ChatGPT group blocked source reservation: %v", err)
	}
	reservation.ID, reservation.AccountID = "empty-group-outside", "outside-chatgpt"
	if _, err := s.ReserveUsage(ctx, reservation); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("empty ChatGPT group admitted outside account: %v", err)
	}
}
