package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"

	"github.com/google/uuid"
)

type ResetRedemption struct {
	CreditID string
	// Nil marks an older pin whose outcome checkpoint was not retained.
	OutcomeVersion *int64
}

type ResetCreditResult struct {
	ResetCreditConsume
	Status              string   `json:"status"`
	AccountID           string   `json:"accountId"`
	UsageWritten        bool     `json:"usageWritten"`
	PrimaryBefore       *float64 `json:"primaryUsedPercentBefore"`
	PrimaryAfter        *float64 `json:"primaryUsedPercentAfter"`
	SecondaryBefore     *float64 `json:"secondaryUsedPercentBefore"`
	SecondaryAfter      *float64 `json:"secondaryUsedPercentAfter"`
	AccountStatusBefore string   `json:"accountStatusBefore"`
	AccountStatusAfter  string   `json:"accountStatusAfter"`
}

// UsageResetCreditCount preserves the older admin read contract, which obtains
// fresh usage metadata. The separate rate-limit-reset-credits route is cached.
func (s *AccountUsageService) UsageResetCreditCount(ctx context.Context, accountID string) (int, error) {
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !usageEligible(account) {
		return 0, domain.ErrNotFound
	}
	if err := s.RefreshAccountUsage(ctx, accountID); err != nil {
		return 0, err
	}
	if snapshot := s.LastUsageSnapshot(account); snapshot != nil && snapshot.ResetCreditCount != nil {
		return max(0, *snapshot.ResetCreditCount), nil
	}
	return 0, nil
}

// ConsumeResetCredit pins one credit and the original provider-outcome version
// before dispatch. A lost-response retry cannot spend a second credit or clear
// a quota refusal observed after the original redemption started.
func (s *AccountUsageService) ConsumeResetCredit(ctx context.Context, accountID, redeemRequestID string) (ResetCreditResult, error) {
	return s.consumeResetCredit(ctx, accountID, redeemRequestID, true)
}

// ConsumeUsageResetCredit preserves the older server-selected reset contract:
// upstream chooses the credit, and only redeem_request_id is sent on the wire.
func (s *AccountUsageService) ConsumeUsageResetCredit(ctx context.Context, accountID, redeemRequestID string) (ResetCreditResult, error) {
	return s.consumeResetCredit(ctx, accountID, redeemRequestID, false)
}

func (s *AccountUsageService) consumeResetCredit(ctx context.Context, accountID, redeemRequestID string, selectCredit bool) (ResetCreditResult, error) {
	var result ResetCreditResult
	if len(redeemRequestID) > 256 || strings.TrimSpace(redeemRequestID) != redeemRequestID {
		return result, domain.ErrInvalid
	}
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return result, err
	}
	if !usageEligible(account) {
		return result, domain.ErrNotFound
	}
	lock := s.accountRedeemLock(account)
	lock.Lock()
	defer lock.Unlock()
	before, err := s.accounts.ListAccountQuota(ctx, accountID)
	if err != nil {
		return result, err
	}
	token, err := s.accessToken(ctx, account)
	if err != nil {
		return result, err
	}
	if redeemRequestID == "" {
		redeemRequestID = uuid.NewString()
	}
	pin, exists, err := s.pins.GetPinnedResetCredit(ctx, accountID, account.Generation, redeemRequestID)
	if err != nil {
		return result, err
	}
	if !exists {
		creditID := ""
		if selectCredit {
			fresh, err := s.fetchCreditsWithRefresh(ctx, account, token)
			if err != nil {
				return result, err
			}
			selected := soonestAvailableCredit(fresh)
			if selected == nil {
				s.cacheSnapshot(account, normalizeSnapshot(fresh))
				return result, ErrNoAvailableResetCredit
			}
			creditID = selected.ID
		}
		pin, err = s.pins.PinResetCredit(ctx, accountID, account.Generation, redeemRequestID, creditID)
		if err != nil {
			return result, err
		}
	}
	consumed, err := s.consumeWithRefresh(ctx, account, token, pin.CreditID, redeemRequestID)
	// Even a lost response may have consumed the credit. Do not keep exposing
	// an older snapshot, and fence concurrent background fetches immediately.
	s.invalidateCreditSnapshot(accountID)
	if err != nil {
		return result, err
	}
	result = ResetCreditResult{ResetCreditConsume: consumed, Status: "reset", AccountID: accountID,
		AccountStatusBefore: string(account.Status), AccountStatusAfter: string(account.Status),
		PrimaryBefore: quotaUsedPercent(before, "primary"), SecondaryBefore: quotaUsedPercent(before, "secondary")}
	result.PrimaryAfter, result.SecondaryAfter = result.PrimaryBefore, result.SecondaryBefore
	if consumed.Code == "reset" || consumed.Code == "already_redeemed" {
		started := s.now().UTC()
		_, fresh, refreshErr := s.refreshAccountUsage(ctx, accountID, account.Generation, false)
		result.UsageWritten = refreshErr == nil && len(fresh) > 0
		if result.UsageWritten {
			result.PrimaryAfter, result.SecondaryAfter = quotaUsedPercent(fresh, "primary"), quotaUsedPercent(fresh, "secondary")
			if s.quotaRecovery != nil && pin.OutcomeVersion != nil && quotaWindowsAvailable(before, fresh, started) {
				if _, err := s.quotaRecovery.RecoverAccountQuota(ctx, accountID, *pin.OutcomeVersion, account.Generation); err != nil {
					return result, err
				}
			}
		}
	}
	if after, err := s.accounts.GetAccount(ctx, accountID); err == nil {
		result.AccountStatusAfter = string(after.Status)
	}
	return result, nil
}

func quotaUsedPercent(quotas []domain.AccountQuota, window string) *float64 {
	if quota := quotaByWindow(quotas, window); quota != nil {
		value := quota.UsedPercent
		return &value
	}
	return nil
}

// A positive receipt supplies reset proof; all governing windows still need
// fresh availability. Missing telemetry cannot reactivate a blocked account.
func quotaWindowsAvailable(before, after []domain.AccountQuota, started time.Time) bool {
	seen := map[string]bool{}
	for _, quota := range after {
		if quota.Window != "primary" && quota.Window != "secondary" && quota.Window != "monthly" {
			continue
		}
		if quota.UsedPercent >= 100 || quota.ObservedAt.Before(started) {
			return false
		}
		seen[quota.Window] = true
	}
	for _, quota := range before {
		if (quota.Window == "primary" || quota.Window == "secondary" || quota.Window == "monthly") && !seen[quota.Window] {
			return false
		}
	}
	return len(seen) > 0
}

func (s *AccountUsageService) accountRedeemLock(account domain.Account) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := accountGenerationKey{accountID: account.ID, generation: account.Generation}
	if lock, ok := s.redeeming[key]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	s.redeeming[key] = lock
	return lock
}

func (s *AccountUsageService) invalidateCreditSnapshot(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.creditEpoch {
		if key.accountID == accountID {
			key := key
			s.creditEpoch[key]++
		}
	}
	for key := range s.snapshots {
		if key.accountID == accountID {
			delete(s.snapshots, key)
		}
	}
}

func (s *AccountUsageService) cacheSnapshot(account domain.Account, snapshot ResetCredits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := accountGenerationKey{accountID: account.ID, generation: account.Generation}
	s.creditEpoch[key]++
	s.snapshots[key] = snapshot
}
