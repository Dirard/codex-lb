package application

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type stubLimitWarmupClaims struct{ attempts []LimitWarmupAttempt }

func (s *stubLimitWarmupClaims) ClaimLimitWarmupAttempt(_ context.Context, accountID, window string, resetAt time.Time, model string, cooldown time.Duration, now time.Time) (LimitWarmupAttempt, bool, error) {
	for _, attempt := range s.attempts {
		if attempt.AccountID == accountID && attempt.Window == window && attempt.ResetAt.Equal(resetAt) ||
			cooldown > 0 && attempt.AccountID == accountID && now.Sub(attempt.AttemptedAt) < cooldown {
			return LimitWarmupAttempt{}, false, nil
		}
	}
	attempt := LimitWarmupAttempt{AccountID: accountID, Attempt: int64(len(s.attempts) + 1), Window: window, ResetAt: resetAt, Status: "claimed", Model: model, AttemptedAt: now}
	s.attempts = append(s.attempts, attempt)
	return attempt, true, nil
}

func (s *stubLimitWarmupClaims) CompleteLimitWarmupAttempt(_ context.Context, accountID string, attempt int64, status string, finishedAt time.Time, errorCode *string) error {
	for i := range s.attempts {
		if s.attempts[i].AccountID == accountID && s.attempts[i].Attempt == attempt {
			s.attempts[i].Status, s.attempts[i].CompletedAt, s.attempts[i].ErrorCode = status, &finishedAt, errorCode
		}
	}
	return nil
}

type warmupSettingsStore struct {
	fakeSettingsStore
	value domain.RuntimeSettings
}

func (s *warmupSettingsStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return s.value, nil
}

func quota(accountID, window string, used float64, resetAt, observedAt time.Time, minutes int) domain.AccountQuota {
	return domain.AccountQuota{AccountID: accountID, Window: window, UsedPercent: used, ResetAt: &resetAt, ObservedAt: observedAt, WindowMinutes: &minutes}
}

func newLimitWarmupTestService(t *testing.T, now time.Time) (*LimitWarmupService, *fakeAccountsStore, *stubResponseProvider, *stubLimitWarmupClaims, *warmupSettingsStore) {
	t.Helper()
	accounts := newFakeAccounts()
	provider := &stubResponseProvider{result: ResponseResult{ResponseID: "r", UsageKnown: true}}
	warmups := NewWarmupService(accounts, provider, &stubLedger{}, nil, nil, func() time.Time { return now })
	settings := &warmupSettingsStore{value: domain.RuntimeSettings{
		LimitWarmupEnabled: true, LimitWarmupModel: "gpt-5.4-mini", LimitWarmupWindows: "both",
		LimitWarmupPrompt: "Say OK.", LimitWarmupCooldownSeconds: 3600,
		LimitWarmupExhaustedPercent: 99, LimitWarmupMinAvailablePercent: 100, LimitWarmupIdlePercent: 1,
	}}
	claims := &stubLimitWarmupClaims{}
	service := NewLimitWarmupService(settings, accounts, warmups, claims, func() time.Time { return now })
	return service, accounts, provider, claims, settings
}

func TestLimitWarmupRequiresConfirmedResetAndFreshOptIn(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	service, accounts, provider, claims, settings := newLimitWarmupTestService(t, now)
	account := warmupAccount("acct_reset", domain.AccountActive)
	account.LimitWarmupEnabled = true
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	before := []domain.AccountQuota{quota(account.ID, "primary", 100, now.Add(-30*time.Second), now.Add(-2*time.Minute), 300)}
	after := []domain.AccountQuota{quota(account.ID, "primary", 0, now.Add(5*time.Hour), now, 300)}
	refreshStarted := now.Add(-time.Minute)
	settings.value.LimitWarmupEnabled = false
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted); err != nil || count != 0 {
		t.Fatalf("disabled = %d %v", count, err)
	}
	settings.value.LimitWarmupEnabled = true
	account.LimitWarmupEnabled = false
	_ = accounts.SaveAccount(ctx, account)
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted); err != nil || count != 0 {
		t.Fatalf("opted out = %d %v", count, err)
	}
	account.LimitWarmupEnabled = true
	_ = accounts.SaveAccount(ctx, account)
	settings.value.LimitWarmupMinAvailablePercent = 0
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted); err != domain.ErrInvalid || count != 0 {
		t.Fatalf("invalid setting = %d %v", count, err)
	}
	settings.value.LimitWarmupMinAvailablePercent = 100
	for _, changed := range []struct {
		name string
		row  domain.AccountQuota
	}{
		{"jitter", quota(account.ID, "primary", 0, now.Add(20*time.Second), now, 300)},
		{"stale", quota(account.ID, "primary", 0, now.Add(5*time.Hour), now.Add(-2*time.Minute), 300)},
		{"not exhausted", quota(account.ID, "primary", 0, now.Add(5*time.Hour), now, 300)},
	} {
		old := before
		newRows := []domain.AccountQuota{changed.row}
		if changed.name == "not exhausted" {
			old = []domain.AccountQuota{quota(account.ID, "primary", 90, now.Add(-30*time.Second), now.Add(-2*time.Minute), 300)}
		}
		if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, old, newRows, refreshStarted); err != nil || count != 0 {
			t.Fatalf("%s = %d %v", changed.name, count, err)
		}
	}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted); err != nil || count != 1 {
		t.Fatalf("confirmed reset = %d %v", count, err)
	}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted); err != nil || count != 0 {
		t.Fatalf("duplicate reset = %d %v", count, err)
	}
	if len(provider.targets) != 1 || len(claims.attempts) != 1 || claims.attempts[0].Window != "primary" {
		t.Fatalf("dispatch/claims = %v %+v", provider.targets, claims.attempts)
	}
}

func TestLimitWarmupSecondarySelectionAndThresholds(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	service, accounts, provider, claims, settings := newLimitWarmupTestService(t, now)
	settings.value.LimitWarmupWindows = "secondary"
	settings.value.LimitWarmupExhaustedPercent = 98
	settings.value.LimitWarmupMinAvailablePercent = 90
	account := warmupAccount("acct_secondary", domain.AccountActive)
	account.LimitWarmupEnabled = true
	_ = accounts.SaveAccount(ctx, account)
	before := []domain.AccountQuota{
		quota(account.ID, "primary", 25, now.Add(-30*time.Second), now.Add(-2*time.Minute), 300),
		quota(account.ID, "secondary", 98.5, now.Add(-30*time.Second), now.Add(-2*time.Minute), 10080),
	}
	after := []domain.AccountQuota{
		quota(account.ID, "primary", 25, now.Add(5*time.Hour), now, 300),
		quota(account.ID, "secondary", 10, now.Add(7*24*time.Hour), now, 10080),
	}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, now.Add(-time.Minute)); err != nil || count != 1 {
		t.Fatalf("secondary = %d %v", count, err)
	}
	if len(provider.targets) != 1 || len(claims.attempts) != 1 || claims.attempts[0].Window != "secondary" {
		t.Fatalf("secondary dispatch/claims = %v %+v", provider.targets, claims.attempts)
	}
}

func TestLimitWarmupSecondaryUsesWeeklyPrimaryWhenItIsTheRealLongWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	service, accounts, _, claims, settings := newLimitWarmupTestService(t, now)
	settings.value.LimitWarmupWindows = "secondary"
	account := warmupAccount("acct_weekly", domain.AccountActive)
	account.LimitWarmupEnabled = true
	_ = accounts.SaveAccount(ctx, account)
	before := []domain.AccountQuota{quota(account.ID, "primary", 100, now.Add(-30*time.Second), now.Add(-2*time.Minute), 10080)}
	after := []domain.AccountQuota{quota(account.ID, "primary", 0, now.Add(7*24*time.Hour), now, 10080)}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, now.Add(-time.Minute)); err != nil || count != 1 {
		t.Fatalf("weekly primary as secondary = %d %v", count, err)
	}
	if len(claims.attempts) != 1 || claims.attempts[0].Window != "secondary" {
		t.Fatalf("logical window = %+v", claims.attempts)
	}
}

func TestLimitWarmupStaggeredIdleIsSeparateOptIn(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0).UTC()
	now := start.Add(30 * time.Second)
	service, accounts, provider, claims, settings := newLimitWarmupTestService(t, now)
	account := warmupAccount("acct_idle", domain.AccountActive)
	account.LimitWarmupEnabled = true
	_ = accounts.SaveAccount(ctx, account)
	after := []domain.AccountQuota{quota(account.ID, "primary", 0.5, start.Add(5*time.Hour), now, 300)}
	refreshStarted := start.Add(10 * time.Second)
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, nil, after, refreshStarted); err != nil || count != 0 {
		t.Fatalf("idle opt-out = %d %v", count, err)
	}
	settings.value.LimitWarmupStaggeredIdleEnabled = true
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, nil, after, refreshStarted); err != nil || count != 1 {
		t.Fatalf("idle due = %d %v", count, err)
	}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, nil, after, refreshStarted); err != nil || count != 0 {
		t.Fatalf("idle duplicate = %d %v", count, err)
	}
	if len(provider.targets) != 1 || claims.attempts[0].Window != "primary_idle" {
		t.Fatalf("idle dispatch/claims = %v %+v", provider.targets, claims.attempts)
	}
	busy := []domain.AccountQuota{quota(account.ID, "primary", 2, start.Add(5*time.Hour), now, 300)}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, nil, busy, refreshStarted); err != nil || count != 0 {
		t.Fatalf("busy idle = %d %v", count, err)
	}
	long := []domain.AccountQuota{quota(account.ID, "primary", 0, start.Add(25*time.Hour), now, 1500)}
	if count, err := service.RunAfterUsageRefresh(ctx, account.ID, account.Generation, nil, long, refreshStarted); err != nil || count != 0 {
		t.Fatalf("long idle = %d %v", count, err)
	}
}

func TestLimitWarmupAutoResolvesCheapestEligibleCatalogModel(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, accounts, _, _, settings := newLimitWarmupTestService(t, now)
	settings.value.LimitWarmupModel = "auto"
	account := warmupAccount("acct_auto", domain.AccountActive)
	account.LimitWarmupEnabled = true
	_ = accounts.SaveAccount(context.Background(), account)
	if got := service.resolveModel(context.Background(), "auto", account); got != "" {
		t.Fatalf("auto without catalog = %q", got)
	}
	service.ConfigureCatalog(&ModelCatalogService{snapshot: &domain.CatalogSnapshot{
		Models: map[string]domain.CatalogModel{
			"gpt-5.4-mini": {Slug: "gpt-5.4-mini", SourceKind: domain.ModelCatalogSourceSubscription, SupportedInAPI: true, InputModalities: []string{"text"}},
			"gpt-5.4-nano": {Slug: "gpt-5.4-nano", SourceKind: domain.ModelCatalogSourceSubscription, SupportedInAPI: true, InputModalities: []string{"text"}},
		},
		ModelAccounts: map[string][]string{"gpt-5.4-mini": {account.ID}, "gpt-5.4-nano": {account.ID}},
	}})
	if got := service.resolveModel(context.Background(), "auto", account); got != "gpt-5.4-nano" {
		t.Fatalf("auto = %q", got)
	}
}
