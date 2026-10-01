package httpapi

import (
	"context"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type accountQuotaView struct {
	RemainingPercent *float64   `json:"remainingPercent"`
	ResetAt          *time.Time `json:"resetAt"`
	WindowMinutes    *int       `json:"windowMinutes"`
	CapacityCredits  *float64   `json:"capacityCredits"`
	RemainingCredits *float64   `json:"remainingCredits"`
}

func quotaView(quota domain.AccountQuota, planType string) accountQuotaView {
	view := accountQuotaView{ResetAt: quota.ResetAt, WindowMinutes: quota.WindowMinutes}
	if quota.UsedPercent >= 0 && quota.UsedPercent <= 100 {
		remaining := 100 - quota.UsedPercent
		view.RemainingPercent = &remaining
	}
	if view.RemainingPercent != nil {
		if capacity := domain.SubscriptionCreditCapacity(planType, quota.Window); capacity != nil {
			view.CapacityCredits = capacity
			remaining := *capacity * *view.RemainingPercent / 100
			view.RemainingCredits = &remaining
		}
	}
	return view
}

func accountQuotasView(quotas []domain.AccountQuota, planType string) (accountQuotaView, accountQuotaView, accountQuotaView) {
	var primary, secondary, monthly accountQuotaView
	monthlyOnly := false
	if strings.EqualFold(planType, "free") {
		for _, quota := range quotas {
			monthlyOnly = monthlyOnly || quota.Window == "monthly"
		}
	}
	for _, quota := range quotas {
		switch quota.Window {
		case "primary":
			if !monthlyOnly {
				primary = quotaView(quota, planType)
			}
		case "secondary":
			if !monthlyOnly {
				secondary = quotaView(quota, planType)
			}
		case "monthly":
			monthly = quotaView(quota, planType)
		}
	}
	return primary, secondary, monthly
}

type resetCreditsView struct {
	AvailableCount   *int       `json:"availableResetCredits"`
	NearestExpiresAt *time.Time `json:"resetCreditNearestExpiresAt"`
}

func accountResetCreditsView(ctx context.Context, usage *application.AccountUsageService, account domain.Account) resetCreditsView {
	view := resetCreditsView{}
	if usage == nil {
		return view
	}
	snapshot, err := usage.ResetCreditsSnapshot(ctx, account.ID)
	if err != nil || snapshot == nil {
		return view
	}
	view.AvailableCount = &snapshot.AvailableCount
	view.NearestExpiresAt = snapshot.NearestExpiresAt
	return view
}
