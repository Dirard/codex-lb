package application

import (
	"time"

	"codex-lb/internal/domain"
)

func selectionQuotaWindows(account domain.Account, quotas []domain.AccountQuota) (primary, long *domain.AccountQuota) {
	return domain.SubscriptionQuotaWindows(account.PlanType, quotas)
}

func quotaSelectionValue(used float64, reset *time.Time, now time.Time) (float64, *time.Time) {
	if reset != nil && !reset.After(now) {
		return 0, nil
	}
	return used, reset
}

// EffectiveAccountQuotaStatus is shared by new-session selection and account
// summaries. Operator-disabled states and unresolved rate-limit blocks win.
func EffectiveAccountQuotaStatus(account domain.Account, quotas []domain.AccountQuota, credits *domain.AccountCreditStatus, refusalAt *time.Time, now time.Time) domain.AccountStatus {
	switch account.Status {
	case domain.AccountPaused, domain.AccountDeactivated, domain.AccountReauthRequired, domain.AccountRateLimited:
		return account.Status
	}
	if credits != nil && credits.Usable() && (account.Status != domain.AccountQuotaExceeded || credits.UsableAfter(refusalAt)) {
		return domain.AccountActive
	}
	if account.Status == domain.AccountQuotaExceeded {
		return account.Status
	}
	primary, long := selectionQuotaWindows(account, quotas)
	if primary != nil {
		used, _ := quotaSelectionValue(primary.UsedPercent, primary.ResetAt, now)
		if used >= 100 {
			return domain.AccountRateLimited
		}
	}
	if long != nil {
		used, _ := quotaSelectionValue(long.UsedPercent, long.ResetAt, now)
		if used >= 100 {
			return domain.AccountQuotaExceeded
		}
	}
	return account.Status
}

func additionalQuotaBlockRecovered(refusalAt *time.Time, primary, secondary *domain.AccountAdditionalQuota, now time.Time) bool {
	if refusalAt == nil || now.Before(refusalAt.Add(domain.AdditionalQuotaBlockCooldown)) {
		return false
	}
	for _, row := range []*domain.AccountAdditionalQuota{primary, secondary} {
		if row != nil && !row.ObservedAt.After(*refusalAt) {
			return false
		}
	}
	return true
}

// additionalQuotaEligibility mirrors the legacy fresh-evidence gate. The
// returned windows are exclusively from the requested canonical quota.
func additionalQuotaEligibility(account domain.Account, definition domain.AdditionalQuotaDefinition, rows []domain.AccountAdditionalQuota, requireFresh bool, now time.Time) (applies bool, state string, primary, secondary *domain.AccountAdditionalQuota) {
	if !requireFresh && !domain.AdditionalQuotaAppliesToPlan(account.PlanType, definition) {
		return false, "eligible", nil, nil
	}
	for i := range rows {
		if rows[i].QuotaKey != definition.QuotaKey {
			continue
		}
		switch rows[i].Window {
		case "primary":
			primary = &rows[i]
		case "secondary":
			secondary = &rows[i]
		}
	}
	if primary == nil && secondary == nil {
		return true, "data_unavailable", nil, nil
	}
	for _, row := range []*domain.AccountAdditionalQuota{primary, secondary} {
		if row == nil {
			continue
		}
		if row.ObservedAt.Before(now.Add(-domain.AdditionalQuotaFreshness)) {
			return true, "data_unavailable", primary, secondary
		}
	}
	for _, row := range []*domain.AccountAdditionalQuota{primary, secondary} {
		if row != nil {
			used, _ := quotaSelectionValue(row.UsedPercent, row.ResetAt, now)
			if used >= 100 {
				return true, "quota_exhausted", primary, secondary
			}
		}
	}
	return true, "eligible", primary, secondary
}
