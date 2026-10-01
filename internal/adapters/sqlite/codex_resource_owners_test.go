package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestCodexResourceOwnerPersistsAndCannotSwitch(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	saveTestAccount(t, s, "acct-a")
	saveTestAccount(t, s, "acct-b")
	now := time.Now().UTC()
	for _, id := range []string{"key-a", "key-b"} {
		if err := s.SaveAPIKey(ctx, testKey(id, nil), now); err != nil {
			t.Fatal(err)
		}
	}
	owner := application.CodexResourceOwner{ResourceType: application.CodexResourceFile,
		ResourceID: "file-1", KeyID: "key-a", AccountID: "acct-a", ExpiresAt: now.Add(time.Hour)}
	if err := s.SaveCodexResourceOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetCodexResourceOwner(ctx, "file", "file-1", "key-a", now); err != nil || got.AccountID != "acct-a" {
		t.Fatalf("resource owner: %+v, %v", got, err)
	}
	if _, err := s.GetCodexResourceOwner(ctx, "file", "file-1", "key-b", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-key file owner leaked: %v", err)
	}
	if _, err := s.GetCodexResourceOwner(ctx, "file", "file-1", "key-a", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired file owner returned: %v", err)
	}
	other := owner
	other.AccountID = "acct-b"
	if err := s.SaveCodexResourceOwner(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("file switched account: %v", err)
	}
	other = owner
	other.KeyID = "key-b"
	if err := s.SaveCodexResourceOwner(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("file switched key: %v", err)
	}
	owner.ExpiresAt = now.Add(2 * time.Hour)
	if err := s.SaveCodexResourceOwner(ctx, owner); err != nil {
		t.Fatalf("same owner renewal: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.GetCodexResourceOwner(ctx, "file", "file-1", "key-a", now.Add(90*time.Minute)); err != nil || got.AccountID != "acct-a" {
		t.Fatalf("owner not durable: %+v, %v", got, err)
	}
	tooLong := owner
	tooLong.ResourceID = strings.Repeat("x", 513)
	if err := reopened.SaveCodexResourceOwner(ctx, tooLong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize file id accepted: %v", err)
	}
	tooLong = owner
	tooLong.ExpiresAt = now.Add(31 * 24 * time.Hour)
	if err := reopened.SaveCodexResourceOwner(ctx, tooLong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded expiry accepted: %v", err)
	}
}

func TestScopedOwnerAccountHonorsCurrentPolicyWithoutQuotaSelection(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct-a")
	now := time.Now().UTC()
	groupID := "group"
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: "Group",
		AccountIDs: []string{"acct-a"}}, now); err != nil {
		t.Fatal(err)
	}
	key := testKey("key-a", &groupID)
	key.AccountAssignmentScopeEnabled = true // Group membership replaces direct assignments.
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	account, err := s.GetAccount(ctx, "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountQuotaExceeded
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligibleAccounts(ctx, "key-a"); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("new session ignored quota state: %v", err)
	}
	if scoped, err := s.ScopedAccountForOwner(ctx, "key-a", "acct-a", now); err != nil || scoped.ID != "acct-a" {
		t.Fatalf("established owner denied by quota telemetry: %+v, %v", scoped, err)
	}
	group, err := s.GetGroup(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	group.AccountIDs = []string{}
	if err := s.SaveGroup(ctx, group, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, "key-a", "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("empty group kept owner authorized: %v", err)
	}
	group.AccountIDs = []string{"acct-a"}
	if err := s.SaveGroup(ctx, group, now); err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountPaused
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, "key-a", "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("paused owner authorized: %v", err)
	}
	account.Status = domain.AccountActive
	account.RequiresEgressDecision = true
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, "key-a", "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("unsafe egress owner authorized: %v", err)
	}
	account.RequiresEgressDecision = false
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	key.IsActive = false
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, "key-a", "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("revoked key retained owner access: %v", err)
	}
	key.IsActive = true
	expired := now.Add(-time.Second)
	key.ExpiresAt = &expired
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, key.ID, "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("expired key retained owner access: %v", err)
	}
	key.ExpiresAt = nil
	if err := s.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM account_credentials WHERE account_id=?", "acct-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, key.ID, "acct-a", now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("owner without credential authorized: %v", err)
	}
	ext := domain.Account{ID: "source-a", Kind: domain.AccountExternal, Provider: "openai_compatible",
		BaseURL: "https://provider.example.invalid/v1", Email: "source-a", PlanType: "external",
		Status: domain.AccountQuotaExceeded, CreatedAt: now}
	if err := s.SaveAccount(ctx, ext); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: ext.ID,
		ExternalKeyEncrypted: testCiphertext()}); err != nil {
		t.Fatal(err)
	}
	extKey := testKey("key-external", nil)
	extKey.SourceAssignmentScopeEnabled = true
	extKey.AssignedSourceIDs = []string{ext.ID}
	if err := s.SaveAPIKey(ctx, extKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, extKey.ID, ext.ID, now); err != nil {
		t.Fatalf("external established owner denied: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "external-continuation",
		APIKeyID: extKey.ID, AccountID: ext.ID, Continuation: true, Model: "model", Now: now}); err != nil {
		t.Fatalf("external established owner denied by ledger: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "external-new",
		APIKeyID: extKey.ID, AccountID: ext.ID, Model: "model", Now: now}); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("new external session ignored quota state: %v", err)
	}
	extKey.AssignedSourceIDs = []string{}
	if err := s.SaveAPIKey(ctx, extKey, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, extKey.ID, ext.ID, now); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("source assignment removal retained access: %v", err)
	}
}
