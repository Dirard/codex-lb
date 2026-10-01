package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type delayedCredits struct {
	UsageClient
	entered chan struct{}
	release chan struct{}
	value   ResetCredits
}

func (s *delayedCredits) FetchResetCredits(ctx context.Context, _, _ string) (ResetCredits, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return ResetCredits{}, ctx.Err()
	case <-s.release:
		return s.value, nil
	}
}

func TestResetCreditInvalidationFencesOlderFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stub := &stubUsageClient{}
	service, store, pins, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountActive)
	if _, err := pins.PinResetCredit(ctx, account.ID, account.Generation, "attempt", "credit"); err != nil {
		t.Fatal(err)
	}
	old := ResetCredits{AvailableCount: 1, Credits: []ResetCredit{{ID: "credit", Status: "available"}}}
	service.cacheSnapshot(account, normalizeSnapshot(old))
	delayed := &delayedCredits{UsageClient: stub, entered: make(chan struct{}), release: make(chan struct{}), value: old}
	service.usage = delayed
	done := make(chan error, 1)
	go func() { done <- service.RefreshResetCredits(ctx, account.ID) }()
	select {
	case <-delayed.entered:
	case <-ctx.Done():
		t.Fatal("poll did not enter")
	}
	if result, err := service.ConsumeResetCredit(ctx, account.ID, "attempt"); err != nil || result.Code != "reset" {
		t.Fatalf("consume: %+v %v", result, err)
	}
	close(delayed.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snapshot, err := service.ResetCreditsSnapshot(ctx, account.ID); err != nil || snapshot != nil {
		t.Fatalf("stale fetch restored credit: %+v %v", snapshot, err)
	}
}

func TestNoOpResetCreditResultDoesNotInventRedemptionOrUsageRefresh(t *testing.T) {
	for _, code := range []string{"no_credit", "nothing_to_reset"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			stub := &stubUsageClient{consumeCode: code, credits: ResetCredits{AvailableCount: 1, Credits: []ResetCredit{{ID: "credit", Status: "available"}}}}
			service, store, _, vault := newUsageTestService(t, stub)
			account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountQuotaExceeded)
			service.cacheSnapshot(account, normalizeSnapshot(stub.credits))
			result, err := service.ConsumeResetCredit(ctx, account.ID, "attempt")
			if err != nil || result.Code != code || result.UsageWritten || result.AccountStatusAfter != "quota_exceeded" || stub.fetches != 0 {
				t.Fatalf("no-op consume changed usage/status: %+v %v calls=%d", result, err, stub.fetches)
			}
			if snapshot, _ := service.ResetCreditsSnapshot(ctx, account.ID); snapshot != nil {
				t.Fatal("no-op consume left an unverified cached credit snapshot")
			}
		})
	}
}

func TestUsageResetCreditUsesServerSelectionAndKeepsRetryContract(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{creditsErr: errors.New("credit list must not be fetched")}
	service, store, pins, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountActive)
	if result, err := service.ConsumeUsageResetCredit(ctx, account.ID, "direct"); err != nil || result.Code != "reset" {
		t.Fatalf("direct consume: %+v %v", result, err)
	}
	pin, exists, err := pins.GetPinnedResetCredit(ctx, account.ID, account.Generation, "direct")
	if err != nil || !exists || pin.CreditID != "" || pin.OutcomeVersion == nil {
		t.Fatalf("direct pin: %+v %v %v", pin, exists, err)
	}
	stub.consumeCode = "already_redeemed"
	if _, err := service.ConsumeResetCredit(ctx, account.ID, "direct"); err != nil {
		t.Fatal(err)
	}
	if len(stub.consumed) != 2 || stub.consumed[0] != "" || stub.consumed[1] != "" {
		t.Fatalf("retry changed upstream contract: %v", stub.consumed)
	}
}

func TestResetCreditZeroCountOverridesStaleAvailableItem(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{credits: ResetCredits{AvailableCount: 0, Credits: []ResetCredit{{ID: "stale", Status: "available"}}}}
	service, store, pins, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountActive)
	service.cacheSnapshot(account, ResetCredits{AvailableCount: 1, Credits: stub.credits.Credits})
	if _, err := service.ConsumeResetCredit(ctx, account.ID, "unproven-retry"); !errors.Is(err, ErrNoAvailableResetCredit) {
		t.Fatalf("zero count: %v", err)
	}
	if _, exists, err := pins.GetPinnedResetCredit(ctx, account.ID, account.Generation, "unproven-retry"); err != nil || exists || len(stub.consumed) != 0 {
		t.Fatalf("zero count created redemption: exists=%v err=%v consumed=%v", exists, err, stub.consumed)
	}
	if snapshot, err := service.ResetCreditsSnapshot(ctx, account.ID); err != nil || snapshot == nil || snapshot.AvailableCount != 0 {
		t.Fatalf("stale positive cache: %+v %v", snapshot, err)
	}
}

func TestLostResetCreditResponseCanRecoverOriginalOutcomeOnRetry(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{consumeErr: errors.New("response lost"), credits: ResetCredits{AvailableCount: 1, Credits: []ResetCredit{{ID: "credit", Status: "available"}}}}
	service, store, pins, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountQuotaExceeded)
	recovery := &recoveryTestStore{accounts: store, version: 7}
	service.ConfigureQuotaRecovery(recovery)
	pins.outcome = func(string) int64 { return recovery.version }
	reset := service.now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: service.now()}); err != nil {
		t.Fatal(err)
	}
	stub.usage = map[string]UsageSnapshot{account.ChatGPTAccountID: {Primary: windowUsage(0)}}
	if _, err := service.ConsumeResetCredit(ctx, account.ID, "lost"); err == nil {
		t.Fatal("lost response appeared successful")
	}
	stub.consumeErr, stub.consumeCode, stub.credits = nil, "already_redeemed", ResetCredits{}
	result, err := service.ConsumeResetCredit(ctx, account.ID, "lost")
	if err != nil || !result.UsageWritten || result.Code != "already_redeemed" || result.AccountStatusAfter != "active" {
		t.Fatalf("retry failed to reconcile original reset: %+v %v", result, err)
	}
	if len(stub.consumed) != 2 || stub.consumed[0] != "credit" || stub.consumed[1] != "credit" {
		t.Fatalf("retry changed credit: %v", stub.consumed)
	}
}

func TestResetCreditReadInvalidatesIneligibleAndMissingAccount(t *testing.T) {
	ctx := context.Background()
	service, store, _, vault := newUsageTestService(t, &stubUsageClient{})
	account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountActive)
	for _, missing := range []bool{false, true} {
		service.cacheSnapshot(account, ResetCredits{AvailableCount: 1})
		if missing {
			store.mu.Lock()
			delete(store.accounts, account.ID)
			store.mu.Unlock()
		} else {
			account.Status = domain.AccountPaused
			_ = store.SaveAccount(ctx, account)
		}
		if value, err := service.ResetCreditsSnapshot(ctx, account.ID); err != nil || value != nil {
			t.Fatalf("ineligible read: %+v %v", value, err)
		}
		account.Status = domain.AccountActive
		_ = store.SaveAccount(ctx, account)
		if value, err := service.ResetCreditsSnapshot(ctx, account.ID); err != nil || value != nil {
			t.Fatalf("read did not evict stale cache: %+v %v", value, err)
		}
	}
}
