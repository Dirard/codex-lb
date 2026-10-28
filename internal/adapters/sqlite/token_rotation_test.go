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

// A rotation that wins the compare-and-set on the refresh token another
// worker's losing refresh was rejected for reactivates an account marked
// refresh_token_reused; other reasons and failed rotations change nothing.
func TestRotationReactivatesOnlyAccountsMarkedForAReusedRefreshToken(t *testing.T) {
	ctx := context.Background()
	fresh := []byte(testCiphertext())
	fresh[72] = '1'
	for _, tc := range []struct {
		reason, wantStatus, wantReason string
		stale, wantChanged             bool
	}{
		{reason: "refresh_token_reused", wantStatus: "active", wantReason: "", wantChanged: true},
		{reason: "invalid_grant", wantStatus: "reauth_required", wantReason: "invalid_grant", wantChanged: true},
		{reason: "refresh_token_reused", stale: true, wantStatus: "reauth_required", wantReason: "refresh_token_reused"},
	} {
		store, _ := testStore(t)
		saveTestAccount(t, store, "acct")
		expected, err := store.GetAccountCredential(ctx, "acct")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkAccountReauthRequired(ctx, expected, tc.reason); err != nil {
			t.Fatal(err)
		}
		next := expected
		next.AccessTokenEncrypted, next.RefreshTokenEncrypted = fresh, fresh
		if tc.stale {
			expected.RefreshTokenEncrypted = fresh
		}
		changed, err := store.RotateAccountCredential(ctx, expected, next, fixedTime)
		if err != nil || changed != tc.wantChanged {
			t.Fatalf("%s stale=%v: rotation changed=%v err=%v", tc.reason, tc.stale, changed, err)
		}
		account, err := store.GetAccount(ctx, "acct")
		if err != nil || string(account.Status) != tc.wantStatus || account.DeactivationReason != tc.wantReason {
			t.Fatalf("%s stale=%v: account %s (%q), want %s (%q): %v", tc.reason, tc.stale,
				account.Status, account.DeactivationReason, tc.wantStatus, tc.wantReason, err)
		}
	}
}
