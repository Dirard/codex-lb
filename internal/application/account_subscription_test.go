package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// stubSubscriptions answers subscription reads from a queue of results and
// records the access token of every read.
type stubSubscriptions struct {
	mu      sync.Mutex
	results []error
	until   time.Time
	tokens  []string
}

func (s *stubSubscriptions) FetchSubscription(_ context.Context, accessToken, _ string) (Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, accessToken)
	if len(s.results) > 0 {
		err := s.results[0]
		s.results = s.results[1:]
		if err != nil {
			return Subscription{}, err
		}
	}
	renews := true
	return Subscription{ActiveUntil: s.until, WillRenew: &renews}, nil
}

// refreshingTokens hands out a new access token on a forced refresh.
type refreshingTokens struct{ vaultTokenSource }

func (refreshingTokens) ForceRefresh(context.Context, domain.Account, string) (string, error) {
	return "refreshed-token", nil
}

func newSubscriptionTestService(t *testing.T, subscriptions *stubSubscriptions, now *time.Time) (*AccountUsageService, *fakeAccountsStore, domain.Account) {
	t.Helper()
	service, store, _, vault := newUsageTestService(t, &stubUsageClient{usage: map[string]UsageSnapshot{}})
	service.tokens = refreshingTokens{vaultTokenSource{cipher: vault}}
	service.now = func() time.Time { return *now }
	service.ConfigureSubscriptions(subscriptions)
	return service, store, saveUsageAccountCredentialed(t, vault, store, "acct_sub", domain.AccountActive)
}

// Use case 1: a read is kept for its incarnation and ChatGPT account id, and
// attempts are spaced by subscriptionInterval whether they succeed or fail
// (R1, R3, R5).
func TestRefreshSubscriptionKeepsReadsAndSpacesAttempts(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	until := now.Add(72 * time.Hour)
	subscriptions := &stubSubscriptions{until: until}
	service, store, account := newSubscriptionTestService(t, subscriptions, &now)

	if service.CachedSubscription(account) != nil {
		t.Fatal("read before any attempt")
	}
	if err := service.RefreshSubscription(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	read := service.CachedSubscription(account)
	if read == nil || !read.ActiveUntil.Equal(until) || read.WillRenew == nil || !*read.WillRenew || !read.ObservedAt.Equal(now) {
		t.Fatalf("read = %+v", read)
	}
	now = now.Add(subscriptionInterval - time.Second)
	_ = service.RefreshSubscription(ctx, account.ID)
	if len(subscriptions.tokens) != 1 {
		t.Fatalf("reads within the interval = %d", len(subscriptions.tokens))
	}
	now = now.Add(time.Second)
	subscriptions.results = []error{&UsageError{Code: "network_error", Message: "down"}}
	if err := service.RefreshSubscription(ctx, account.ID); err == nil {
		t.Fatal("failed read reported success")
	}
	_ = service.RefreshSubscription(ctx, account.ID)
	if len(subscriptions.tokens) != 2 {
		t.Fatalf("a failed attempt was retried at once: %d reads", len(subscriptions.tokens))
	}
	if kept := service.CachedSubscription(account); kept == nil || !kept.ObservedAt.Equal(read.ObservedAt) {
		t.Fatalf("failed read replaced the previous one: %+v", kept)
	}

	moved := account
	moved.ChatGPTAccountID = "chatgpt-other"
	if service.CachedSubscription(moved) != nil {
		t.Fatal("read served for another ChatGPT account id")
	}
	next := account
	next.Generation++
	if err := store.SaveAccount(ctx, next); err != nil {
		t.Fatal(err)
	}
	if service.CachedSubscription(next) != nil {
		t.Fatal("read served for another incarnation")
	}
}

// A failed read changes nothing else about the account (R4).
func TestRefreshSubscriptionFailureLeavesTheAccount(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	subscriptions := &stubSubscriptions{results: []error{&UsageError{Code: "invalid_response", Message: "no end"}}}
	service, store, account := newSubscriptionTestService(t, subscriptions, &now)
	before, _ := store.GetAccount(ctx, account.ID)
	credential, _ := store.GetAccountCredential(ctx, account.ID)

	if err := service.RefreshSubscription(ctx, account.ID); err == nil {
		t.Fatal("failed read reported success")
	}
	after, _ := store.GetAccount(ctx, account.ID)
	afterCredential, _ := store.GetAccountCredential(ctx, account.ID)
	quotas, _ := store.ListAccountQuota(ctx, account.ID)
	if after.Status != before.Status || string(afterCredential.AccessTokenEncrypted) != string(credential.AccessTokenEncrypted) || len(quotas) != 0 {
		t.Fatalf("account %+v quotas %+v", after, quotas)
	}
	if service.CachedSubscription(account) != nil {
		t.Fatal("failed read kept")
	}
}

// A definitive 401 is retried once with the refreshed token; accounts the
// usage poller skips are not read (R2).
func TestRefreshSubscriptionRetriesAfterRefreshAndSkipsPausedAccounts(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	subscriptions := &stubSubscriptions{until: now.Add(time.Hour),
		results: []error{&UsageError{Code: "token_expired", Message: "expired", Status: 401}}}
	service, store, account := newSubscriptionTestService(t, subscriptions, &now)

	if err := service.RefreshSubscription(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if len(subscriptions.tokens) != 2 || subscriptions.tokens[1] != "refreshed-token" || service.CachedSubscription(account) == nil {
		t.Fatalf("tokens %v read %+v", subscriptions.tokens, service.CachedSubscription(account))
	}

	paused := saveUsageAccount(t, store, "acct_paused", domain.AccountPaused)
	if err := service.RefreshSubscription(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	if len(subscriptions.tokens) != 2 {
		t.Fatalf("paused account read: %v", subscriptions.tokens)
	}
	if err := service.RefreshSubscription(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing account = %v", err)
	}
}

// The usage poller reads each polled account's subscription (use case 1).
func TestRefreshAllReadsSubscriptions(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	subscriptions := &stubSubscriptions{until: now.Add(time.Hour)}
	service, _, account := newSubscriptionTestService(t, subscriptions, &now)

	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if service.CachedSubscription(account) == nil || len(subscriptions.tokens) != 1 {
		t.Fatalf("reads %v", subscriptions.tokens)
	}
}
