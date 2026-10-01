package sqlite

import (
	"context"
	"testing"

	"codex-lb/internal/domain"
)

func TestTokenRotationIsFencedByAccountGeneration(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	expected, err := store.GetAccountCredential(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccount(ctx, "acct", false); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 73)
	raw[0], raw[1] = 0x80, 1
	fresh := []byte(testCiphertext())
	fresh[72] = '1'
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	reimported := domain.AccountCredential{AccountID: "acct", Generation: 1,
		AccessTokenEncrypted: fresh, RefreshTokenEncrypted: fresh, IDTokenEncrypted: fresh}
	if err := store.SaveAccountIdentity(ctx, account, reimported); err != nil {
		t.Fatal(err)
	}

	next := expected
	next.AccessTokenEncrypted, next.RefreshTokenEncrypted, next.IDTokenEncrypted = fresh, fresh, fresh
	changed, err := store.RotateAccountCredential(ctx, expected, next, fixedTime)
	if err != nil || changed {
		t.Fatalf("stale rotation changed=%v err=%v", changed, err)
	}
	if err := store.MarkAccountReauthRequired(ctx, expected, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAccount(ctx, "acct")
	if err != nil || current.Status != domain.AccountActive || current.Generation != 1 {
		t.Fatalf("reimport was changed by stale token work: %+v %v", current, err)
	}
}
