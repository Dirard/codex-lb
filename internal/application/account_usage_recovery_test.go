package application

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type recoveryTestStore struct {
	accounts *fakeAccountsStore
	version  int64
}

func (s *recoveryTestStore) CaptureQuotaOutcome(context.Context, string) (int64, error) {
	return s.version, nil
}
func (s *recoveryTestStore) RecoverAccountQuota(_ context.Context, id string, version, generation int64) (bool, error) {
	s.accounts.mu.Lock()
	defer s.accounts.mu.Unlock()
	a := s.accounts.accounts[id]
	if version != s.version || !quotaBlockedStatus(a.Status) || a.RequiresEgressDecision {
		return false, nil
	}
	a.Status = domain.AccountActive
	s.accounts.accounts[id] = a
	return true, nil
}

func TestUsageRefreshRecoversOnlyConfirmedResetWithoutOverwritingPolicy(t *testing.T) {
	for _, kind := range []string{"confirmed", "same window", "other exhausted", "missing window", "newer refusal", "pause during fetch", "active at zero", "required egress"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
			service, store, _, vault := newUsageTestService(t, stub)
			account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountQuotaExceeded)
			recovery := &recoveryTestStore{accounts: store, version: 1}
			service.ConfigureQuotaRecovery(recovery)
			now := service.now()
			oldReset, nextReset, weekReset := now.Add(-time.Minute), now.Add(5*time.Hour), now.Add(7*24*time.Hour)
			for _, quota := range []domain.AccountQuota{
				{AccountID: account.ID, Window: "primary", UsedPercent: 100, ResetAt: &oldReset, ObservedAt: now.Add(-2 * time.Minute)},
				{AccountID: account.ID, Window: "secondary", UsedPercent: 20, ResetAt: &weekReset, ObservedAt: now.Add(-2 * time.Minute)},
			} {
				if err := store.SaveAccountQuota(ctx, quota); err != nil {
					t.Fatal(err)
				}
			}
			primary, secondary := windowUsage(0), windowUsage(20)
			primary.ResetAt, secondary.ResetAt = &nextReset, &weekReset
			want := domain.AccountQuotaExceeded
			switch kind {
			case "confirmed":
				want = domain.AccountActive
			case "same window":
				primary.ResetAt = &oldReset
			case "other exhausted":
				secondary = windowUsage(100)
			case "missing window":
				secondary = nil
			case "newer refusal":
				stub.onFetch = func() { recovery.version++ }
			case "pause during fetch":
				want = domain.AccountPaused
				stub.onFetch = func() { account.Status = domain.AccountPaused; _ = store.SaveAccount(ctx, account) }
			case "active at zero":
				account.Status, want = domain.AccountActive, domain.AccountActive
				primary = windowUsage(100)
				if err := store.SaveAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
			case "required egress":
				account.RequiresEgressDecision = true
				if err := store.SaveAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
			}
			stub.usage[account.ChatGPTAccountID] = UsageSnapshot{Primary: primary, Secondary: secondary}
			if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := store.GetAccount(ctx, account.ID)
			if err != nil || saved.Status != want {
				t.Fatalf("status=%s want=%s error=%v", saved.Status, want, err)
			}
			if kind == "required egress" && stub.fetches != 0 {
				t.Fatal("unresolved proxy policy silently used direct usage polling")
			}
		})
	}
}

func TestUsageRefreshRecoversOnExplicitProviderPermission(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         domain.AccountStatus
		beforeUsed     float64
		withReset      bool
		permission     string
		missingWindow  bool
		exhausted      bool
		newerRefusal   bool
		pauseDuringUse bool
		want           domain.AccountStatus
	}{
		{name: "rate limited same deadline", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "allowed", want: domain.AccountActive},
		{name: "quota exceeded same deadline", status: domain.AccountQuotaExceeded, beforeUsed: 100, withReset: true, permission: "allowed", want: domain.AccountActive},
		{name: "without deadline", status: domain.AccountRateLimited, beforeUsed: 100, permission: "allowed", want: domain.AccountActive},
		{name: "before already zero", status: domain.AccountRateLimited, beforeUsed: 0, withReset: true, permission: "allowed", want: domain.AccountActive},
		{name: "permission missing", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, want: domain.AccountRateLimited},
		{name: "permission denied", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "denied", want: domain.AccountRateLimited},
		{name: "sibling missing", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "allowed", missingWindow: true, want: domain.AccountRateLimited},
		{name: "sibling exhausted", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "allowed", exhausted: true, want: domain.AccountRateLimited},
		{name: "newer refusal", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "allowed", newerRefusal: true, want: domain.AccountRateLimited},
		{name: "pause during fetch", status: domain.AccountRateLimited, beforeUsed: 100, withReset: true, permission: "allowed", pauseDuringUse: true, want: domain.AccountPaused},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
			service, store, _, vault := newUsageTestService(t, stub)
			account := saveUsageAccountCredentialed(t, vault, store, "acct", test.status)
			recovery := &recoveryTestStore{accounts: store, version: 1}
			service.ConfigureQuotaRecovery(recovery)
			now := service.now()
			var reset *time.Time
			if test.withReset {
				deadline := now.Add(time.Hour)
				reset = &deadline
			}
			for _, window := range []string{"primary", "secondary"} {
				if err := store.SaveAccountQuota(ctx, domain.AccountQuota{
					AccountID: account.ID, Window: window, UsedPercent: test.beforeUsed,
					ResetAt: reset, ObservedAt: now.Add(-time.Minute),
				}); err != nil {
					t.Fatal(err)
				}
			}
			secondary := windowUsage(0)
			secondary.ResetAt = reset
			if test.exhausted {
				secondary = windowUsage(100)
				secondary.ResetAt = reset
			}
			if test.missingWindow {
				secondary = nil
			}
			primary := windowUsage(0)
			primary.ResetAt = reset
			snapshot := UsageSnapshot{Primary: primary, Secondary: secondary}
			yes, no := true, false
			if test.permission == "allowed" {
				snapshot.RateLimitAllowed, snapshot.RateLimitReached = &yes, &no
			}
			if test.permission == "denied" {
				snapshot.RateLimitAllowed, snapshot.RateLimitReached = &no, &yes
			}
			switch {
			case test.newerRefusal:
				stub.onFetch = func() { recovery.version++ }
			case test.pauseDuringUse:
				stub.onFetch = func() {
					account.Status = domain.AccountPaused
					_ = store.SaveAccount(ctx, account)
				}
			}
			stub.usage[account.ChatGPTAccountID] = snapshot
			if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := store.GetAccount(ctx, account.ID)
			if err != nil || saved.Status != test.want {
				t.Fatalf("status=%s want=%s error=%v", saved.Status, test.want, err)
			}
		})
	}
}

func TestExplicitRateLimitDenialVetoesTemporalRecovery(t *testing.T) {
	for _, permission := range []string{"allowed false", "limit reached"} {
		t.Run(permission, func(t *testing.T) {
			ctx := context.Background()
			stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
			service, store, _, vault := newUsageTestService(t, stub)
			account := saveUsageAccountCredentialed(t, vault, store, "acct", domain.AccountQuotaExceeded)
			recovery := &recoveryTestStore{accounts: store, version: 1}
			service.ConfigureQuotaRecovery(recovery)
			now := service.now()
			oldReset, nextReset := now.Add(-time.Minute), now.Add(time.Hour)
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{
				AccountID: account.ID, Window: "primary", UsedPercent: 100,
				ResetAt: &oldReset, ObservedAt: now.Add(-2 * time.Minute),
			}); err != nil {
				t.Fatal(err)
			}
			primary := windowUsage(0)
			primary.ResetAt = &nextReset
			snapshot := UsageSnapshot{Primary: primary}
			yes, no := true, false
			if permission == "allowed false" {
				snapshot.RateLimitAllowed, snapshot.RateLimitReached = &no, &no
			} else {
				snapshot.RateLimitAllowed, snapshot.RateLimitReached = &yes, &yes
			}
			stub.usage[account.ChatGPTAccountID] = snapshot
			if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := store.GetAccount(ctx, account.ID)
			if err != nil || saved.Status != domain.AccountQuotaExceeded {
				t.Fatalf("status=%s error=%v", saved.Status, err)
			}
		})
	}
}
