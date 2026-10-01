package sqlite

import (
	"context"
	"testing"

	"codex-lb/internal/domain"
)

func TestAccountIdentityIsAtomicAndPreservesOperatorPolicy(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	account := domain.Account{ID: "identity", Kind: domain.AccountChatGPT, Provider: "openai", Status: domain.AccountPaused, RoutingPolicy: "preserve", RequiresEgressDecision: true, SecurityWorkAuthorized: true, LimitWarmupEnabled: true, Alias: "operator alias"}
	credential := domain.AccountCredential{AccountID: account.ID}
	if err := store.SaveAccountIdentity(ctx, account, credential); err == nil {
		t.Fatal("missing tokens accepted")
	}
	if _, err := store.GetAccount(ctx, account.ID); err != ErrNotFound {
		t.Fatal("partial account left after credential failure")
	}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	updated := domain.Account{ID: account.ID, Kind: domain.AccountChatGPT, Provider: "openai", Email: "new@example.invalid", Status: domain.AccountActive, RoutingPolicy: "normal"}
	credential.AccessTokenEncrypted = testCiphertext()
	credential.RefreshTokenEncrypted = testCiphertext()
	credential.IDTokenEncrypted = testCiphertext()
	if err := store.SaveAccountIdentity(ctx, updated, credential); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil || got.Email != updated.Email || got.Status != domain.AccountPaused || !got.RequiresEgressDecision || !got.SecurityWorkAuthorized || !got.LimitWarmupEnabled || got.RoutingPolicy != "preserve" || got.Alias != "operator alias" {
		t.Fatalf("login replaced policy: %+v %v", got, err)
	}
}
