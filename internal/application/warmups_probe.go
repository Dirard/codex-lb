package application

import (
	"context"

	"codex-lb/internal/domain"
)

type AccountProbeResult struct {
	Status              string
	AccountID           string
	ProbeStatusCode     *int
	PrimaryBefore       *float64
	PrimaryAfter        *float64
	SecondaryBefore     *float64
	SecondaryAfter      *float64
	AccountStatusBefore string
	AccountStatusAfter  string
}

func (s *WarmupService) quotaWindow(ctx context.Context, accountID, window string) *float64 {
	quotas, err := s.accounts.ListAccountQuota(ctx, accountID)
	if err != nil {
		return nil
	}
	for _, quota := range quotas {
		if quota.Window == window {
			used := quota.UsedPercent
			return &used
		}
	}
	return nil
}

// ProbeAccount executes one pinned synthetic request and reports before/after
// quota snapshots. Paused is explicitly allowed here: force-probe is the admin
// wake-up path. A quota refusal never pauses the account from this path.
func (s *WarmupService) ProbeAccount(ctx context.Context, accountID, model string) (AccountProbeResult, error) {
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return AccountProbeResult{}, err
	}
	result := AccountProbeResult{
		Status: "completed", AccountID: accountID,
		PrimaryBefore:       s.quotaWindow(ctx, accountID, "primary"),
		SecondaryBefore:     s.quotaWindow(ctx, accountID, "secondary"),
		AccountStatusBefore: string(account.Status), AccountStatusAfter: string(account.Status),
	}
	probeErr := s.ExecuteWarmup(ctx, accountID, model, nil, domain.DefaultAutomationPrompt)
	// Real after-snapshot: re-poll upstream quota, not the cached row.
	if s.refresher != nil {
		_ = s.refresher.RefreshAccountUsageForGeneration(ctx, accountID, account.Generation)
	}
	statusCode := 200
	if probeErr != nil {
		statusCode = 502
		if probeErr == errWarmupIneligible {
			statusCode = 409
		}
	}
	result.ProbeStatusCode = &statusCode
	after, err := s.accounts.GetAccount(ctx, accountID)
	if err == nil {
		result.AccountStatusAfter = string(after.Status)
	}
	result.PrimaryAfter = s.quotaWindow(ctx, accountID, "primary")
	result.SecondaryAfter = s.quotaWindow(ctx, accountID, "secondary")
	if probeErr != nil && probeErr != errWarmupIneligible && probeErr != errWarmupQuotaRefused {
		return result, probeErr
	}
	return result, nil
}
