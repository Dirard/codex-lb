package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// subscriptionInterval spaces the subscription reads of one account
// incarnation; a failed attempt waits as long as a successful one.
const subscriptionInterval = 15 * time.Minute

// Subscription is an account's current paid period as ChatGPT reports it.
type Subscription struct {
	ActiveUntil time.Time
	WillRenew   *bool
}

// AccountSubscription is the last successful read, tied to the ChatGPT
// account id it was taken for.
type AccountSubscription struct {
	Subscription
	ChatGPTAccountID string
	ObservedAt       time.Time
}

// SubscriptionClient is the ChatGPT subscription port; tests provide stubs.
type SubscriptionClient interface {
	FetchSubscription(ctx context.Context, accessToken, chatgptAccountID string) (Subscription, error)
}

// ConfigureSubscriptions enables subscription reads during usage polling;
// call it before RunUsagePoller.
func (s *AccountUsageService) ConfigureSubscriptions(client SubscriptionClient) {
	s.subscriptions = client
}

// RefreshSubscription is the use-case step "take an access token and read the
// subscription endpoint" for an account the usage poller polls: at most once
// per subscriptionInterval per account incarnation. A failure keeps the
// previous read and changes nothing else about the account.
func (s *AccountUsageService) RefreshSubscription(ctx context.Context, accountID string) error {
	if s.subscriptions == nil {
		return nil
	}
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil || !usageEligible(account) {
		return err
	}
	key := accountGenerationKey{accountID: account.ID, generation: account.Generation}
	now := s.now().UTC()
	s.mu.Lock()
	if last, ok := s.subscriptionTried[key]; ok && now.Sub(last) < subscriptionInterval {
		s.mu.Unlock()
		return nil
	}
	s.subscriptionTried[key] = now
	s.mu.Unlock()
	token, err := s.accessToken(ctx, account)
	if err != nil {
		return err
	}
	subscription, err := s.subscriptions.FetchSubscription(ctx, token, account.ChatGPTAccountID)
	if definitiveAuthRejection(err) {
		refreshed, refreshErr := s.tokens.ForceRefresh(ctx, account, token)
		if refreshErr != nil {
			return refreshErr
		}
		subscription, err = s.subscriptions.FetchSubscription(ctx, refreshed, account.ChatGPTAccountID)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.subscriptionSeen[key] = AccountSubscription{Subscription: subscription,
		ChatGPTAccountID: account.ChatGPTAccountID, ObservedAt: s.now().UTC()}
	s.mu.Unlock()
	return nil
}

// CachedSubscription returns the last read of the account's current
// incarnation and ChatGPT account id, or nil, without storage or provider reads.
func (s *AccountUsageService) CachedSubscription(account domain.Account) *AccountSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	read, ok := s.subscriptionSeen[accountGenerationKey{accountID: account.ID, generation: account.Generation}]
	if !ok || read.ChatGPTAccountID != account.ChatGPTAccountID {
		return nil
	}
	return &read
}
