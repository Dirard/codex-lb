package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"codex-lb/internal/domain"
)

func TestResetCreditPinsAreDurableAndFirstWins(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saveTestAccount(t, store, "acct_1")

	if _, ok, err := store.GetPinnedResetCredit(ctx, "acct_1", 0, "redeem_1"); err != nil || ok {
		t.Fatalf("empty pin lookup = %v %v", ok, err)
	}
	pinned, err := store.PinResetCredit(ctx, "acct_1", 0, "redeem_1", "credit_first")
	if err != nil || pinned.CreditID != "credit_first" || pinned.OutcomeVersion == nil || *pinned.OutcomeVersion != 0 {
		t.Fatalf("pin = %+v err %v", pinned, err)
	}
	pinned, err = store.PinResetCredit(ctx, "acct_1", 0, "redeem_1", "credit_second")
	if err != nil || pinned.CreditID != "credit_first" {
		t.Fatalf("racing pin = %+v err %v", pinned, err)
	}
	if pinned, ok, err := store.GetPinnedResetCredit(ctx, "acct_1", 0, "redeem_1"); err != nil || !ok || pinned.CreditID != "credit_first" {
		t.Fatalf("lookup = %+v %v %v", pinned, ok, err)
	}
	if _, err := store.PinResetCredit(ctx, "", 0, "redeem_1", "credit_x"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid pin err = %v", err)
	}
	// A legacy pin has no provider-outcome checkpoint; retry cannot invent one.
	if _, err := store.db.ExecContext(ctx, "INSERT INTO reset_credit_redeem_requests(account_id,redeem_request_id,credit_id,created_at) VALUES('acct_1','old','old-credit',0)"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, request := range []string{"redeem_1", "old"} {
		pinned, err := store.PinResetCredit(ctx, "acct_1", 0, request, "replacement")
		if err != nil {
			t.Fatal(err)
		}
		if request == "old" {
			if pinned.CreditID != "old-credit" || pinned.OutcomeVersion != nil {
				t.Fatalf("old pin acquired a new checkpoint: %+v", pinned)
			}
		} else if pinned.CreditID != "credit_first" || pinned.OutcomeVersion == nil || *pinned.OutcomeVersion != 0 {
			t.Fatalf("checkpoint lost after reopen: %+v", pinned)
		}
	}
}

func TestResetCreditPinDoesNotCrossReimportedIncarnation(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	defer store.Close()
	saveTestAccount(t, store, "acct")
	if _, err := store.PinResetCredit(ctx, "acct", 0, "redeem", "old-credit"); err != nil {
		t.Fatal(err)
	}
	account, _ := store.GetAccount(ctx, "acct")
	credential, _ := store.GetAccountCredential(ctx, "acct")
	if err := store.DeleteAccount(ctx, "acct", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinResetCredit(ctx, "acct", 0, "late", "late-credit"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("deleted account acquired new redemption: %v", err)
	}
	if err := store.SaveAccountIdentity(ctx, account, credential); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinResetCredit(ctx, "acct", 0, "late", "late-credit"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("old callback acquired new redemption: %v", err)
	}
	if _, err := store.PinResetCredit(ctx, "acct", 1, "redeem", "new-credit"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reimport inherited pin: %v", err)
	}
	if _, ok, err := store.GetPinnedResetCredit(ctx, "acct", 1, "redeem"); err != nil || ok {
		t.Fatalf("new incarnation lookup = %v %v", ok, err)
	}
	if pinned, ok, err := store.GetPinnedResetCredit(ctx, "acct", 0, "redeem"); err != nil || !ok || pinned.CreditID != "old-credit" {
		t.Fatalf("old receipt lost = %+v %v %v", pinned, ok, err)
	}
}
