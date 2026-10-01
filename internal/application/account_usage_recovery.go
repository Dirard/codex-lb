package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// AccountQuotaRecovery applies the confirmed-reset transition without writing
// a stale full Account snapshot over credentials or administrator policy.
type AccountQuotaRecovery interface {
	CaptureQuotaOutcome(context.Context, string) (int64, error)
	RecoverAccountQuota(context.Context, string, int64, int64) (bool, error)
}

func (s *AccountUsageService) ConfigureQuotaRecovery(store AccountQuotaRecovery) {
	s.quotaRecovery = store
}

func quotaBlockedStatus(status domain.AccountStatus) bool {
	return status == domain.AccountRateLimited || status == domain.AccountQuotaExceeded
}

func quotaResetAvailable(before, after []domain.AccountQuota, started, observed time.Time) bool {
	current := make(map[string]domain.AccountQuota, len(after))
	for _, quota := range after {
		if quota.Window != "primary" && quota.Window != "secondary" && quota.Window != "monthly" {
			continue
		}
		if quota.UsedPercent >= 100 || quota.ObservedAt.Before(started) {
			return false
		}
		current[quota.Window] = quota
	}
	reset := false
	for _, old := range before {
		if old.Window != "primary" && old.Window != "secondary" && old.Window != "monthly" {
			continue
		}
		fresh, ok := current[old.Window]
		if !ok {
			return false // An omitted governing window is not proof of recovery.
		}
		if old.ResetAt != nil && !old.ResetAt.After(started) && fresh.ResetAt != nil &&
			fresh.ResetAt.After(observed) && fresh.ResetAt.Sub(*old.ResetAt) >= time.Minute {
			reset = true
		}
	}
	return reset
}
