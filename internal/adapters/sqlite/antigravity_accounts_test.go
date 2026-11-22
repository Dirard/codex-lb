package sqlite

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func antigravityTestSource(t *testing.T, s *Store, credential []byte) domain.ModelSource {
	t.Helper()
	now := time.Now().UTC()
	source := domain.ModelSource{ID: "src_antigravity", Name: "agent@example.test", Kind: domain.ModelSourceAntigravity,
		BaseURL: domain.AntigravityBaseURL, Enabled: true, Chat: true, Responses: true, CreatedAt: now, UpdatedAt: now,
		ProviderConfig: domain.ModelSourceProviderConfig{Antigravity: &domain.AntigravityAccountInfo{Email: "agent@example.test", Project: "aicode-consumers"}},
		Models:         []domain.ModelSourceModel{{Model: "gemini-3.8-flash-high", Enabled: true}}}
	if err := s.SaveModelSource(context.Background(), source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: credential}); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestAntigravityAccountStatusWritesAreGuarded(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	source := antigravityTestSource(t, s, testCiphertext())
	link := "https://accounts.example.test/verify"
	// An operator pause wins over a verification request.
	paused := source
	paused.Enabled = false
	if err := s.SaveModelSource(ctx, paused, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAntigravityAccountStatus(ctx, source.ID, domain.AccountReauthRequired, "", &link); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountPaused || stored.ProviderConfig.Antigravity.ActionURL != "" {
		t.Fatalf("a paused account changed: %s %+v", stored.AccountStatus, stored.ProviderConfig.Antigravity)
	}
	if err := s.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAntigravityAccountStatus(ctx, source.ID, domain.AccountReauthRequired, "", &link); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountReauthRequired || stored.ProviderConfig.Antigravity.ActionURL != link || stored.Models[0].Model != "gemini-3.8-flash-high" {
		t.Fatalf("verification: %s %+v", stored.AccountStatus, stored.ProviderConfig.Antigravity)
	}
	cleared := ""
	if err := s.SetAntigravityAccountStatus(ctx, source.ID, domain.AccountActive, "", &cleared); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountActive || stored.ProviderConfig.Antigravity.ActionURL != "" || stored.ProviderConfig.Antigravity.Email != "agent@example.test" {
		t.Fatalf("re-activation: %s %+v", stored.AccountStatus, stored.ProviderConfig.Antigravity)
	}
	for _, invalid := range []struct {
		status domain.AccountStatus
		reason string
	}{{domain.AccountPaused, ""}, {domain.AccountDeactivated, "deleted"}, {domain.AccountReauthRequired, "tos_violation"}} {
		if err := s.SetAntigravityAccountStatus(ctx, source.ID, invalid.status, invalid.reason, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s/%s accepted: %v", invalid.status, invalid.reason, err)
		}
	}
	if err := s.SetAntigravityAccountStatus(ctx, "src_other", domain.AccountReauthRequired, "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown source: %v", err)
	}
	if err := s.DeleteModelSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAntigravityAccountStatus(ctx, source.ID, domain.AccountDeactivated, "tos_violation", &link); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted source was suspended: %v", err)
	}
}

func TestExternalCredentialRotationNeverOverwritesANewerSignIn(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	first := testCiphertext()
	raw := make([]byte, 73)
	raw[0], raw[1] = 0x80, 1
	rotated := []byte(base64.URLEncoding.EncodeToString(raw))
	raw[1] = 2
	newer := []byte(base64.URLEncoding.EncodeToString(raw))
	source := antigravityTestSource(t, s, first)
	if wrote, err := s.RotateExternalCredential(ctx, source.ID, first, rotated); err != nil || !wrote {
		t.Fatalf("rotation: %v %v", wrote, err)
	}
	if err := s.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: newer}); err != nil {
		t.Fatal(err)
	}
	if wrote, err := s.RotateExternalCredential(ctx, source.ID, rotated, first); err != nil || wrote {
		t.Fatalf("a stale rotation wrote: %v %v", wrote, err)
	}
	credential, err := s.GetAccountCredential(ctx, source.ID)
	if err != nil || string(credential.ExternalKeyEncrypted) != string(newer) {
		t.Fatalf("credential: %v", err)
	}
	if _, err := s.RotateExternalCredential(ctx, source.ID, newer, []byte("plain")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plain credential was accepted: %v", err)
	}
}
