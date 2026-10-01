package application

import (
	"context"
	"math"
	"time"

	"codex-lb/internal/domain"
)

type CodexKeyUsageWindow struct {
	UsedPercent        int    `json:"used_percent"`
	LimitWindowSeconds *int   `json:"limit_window_seconds"`
	ResetAfterSeconds  *int   `json:"reset_after_seconds"`
	ResetAt            *int64 `json:"reset_at"`
}

type CodexKeyRateLimit struct {
	Allowed         bool                 `json:"allowed"`
	LimitReached    bool                 `json:"limit_reached"`
	PrimaryWindow   *CodexKeyUsageWindow `json:"primary_window"`
	SecondaryWindow *CodexKeyUsageWindow `json:"secondary_window"`
	MonthlyWindow   *CodexKeyUsageWindow `json:"monthly_window"`
}

type CodexKeyCredits struct {
	HasCredits          bool     `json:"has_credits"`
	Unlimited           bool     `json:"unlimited"`
	Balance             string   `json:"balance"`
	ApproxLocalMessages []string `json:"approx_local_messages"`
	ApproxCloudMessages []string `json:"approx_cloud_messages"`
}

type CodexKeyUsage struct {
	PlanType              string             `json:"plan_type"`
	RateLimit             *CodexKeyRateLimit `json:"rate_limit"`
	Credits               *CodexKeyCredits   `json:"credits"`
	RateLimitResetCredits *struct{}          `json:"rate_limit_reset_credits"`
	AdditionalRateLimits  []struct{}         `json:"additional_rate_limits"`
}

// CodexUsage translates local credit-limit windows into the Codex-shaped view.
// The credits balance remains null: Go quota snapshots contain no spendable
// upstream balance, and local credit limits are display-only.
func (s *KeyUsageService) CodexUsage(ctx context.Context, keyID string) (CodexKeyUsage, error) {
	result := CodexKeyUsage{PlanType: "api_key", AdditionalRateLimits: []struct{}{}}
	key, err := s.key(ctx, keyID)
	if err != nil {
		return result, err
	}
	limits := make([]KeyUsageLimit, 0, len(key.Limits))
	for _, rule := range key.Limits {
		limits = append(limits, keyRuleLimit(rule))
	}
	selectLimit := func(windows ...domain.LimitWindow) *KeyUsageLimit {
		for _, window := range windows {
			for i := range limits {
				limit := &limits[i]
				if limit.LimitType == domain.LimitCredits && limit.ModelFilter == nil && limit.LimitWindow == window {
					return limit
				}
			}
		}
		return nil
	}
	primary := selectLimit(domain.WindowFiveHours, domain.WindowDaily)
	secondary := selectLimit(domain.WindowSevenDays, domain.WindowWeekly)
	monthly := selectLimit(domain.WindowMonthly)
	now := s.now().UTC()
	primaryView := codexKeyWindow(primary, now)
	secondaryView := codexKeyWindow(secondary, now)
	monthlyView := codexKeyWindow(monthly, now)
	if primaryView != nil || secondaryView != nil || monthlyView != nil {
		reached := false
		for _, window := range []*CodexKeyUsageWindow{primaryView, secondaryView, monthlyView} {
			if window != nil && window.UsedPercent >= 100 {
				reached = true
			}
		}
		result.RateLimit = &CodexKeyRateLimit{Allowed: !reached, LimitReached: reached,
			PrimaryWindow: primaryView, SecondaryWindow: secondaryView, MonthlyWindow: monthlyView}
	}
	return result, nil
}

func codexKeyWindow(limit *KeyUsageLimit, now time.Time) *CodexKeyUsageWindow {
	if limit == nil || limit.MaxValue <= 0 {
		return nil
	}
	seconds := 0
	switch limit.LimitWindow {
	case domain.WindowFiveHours:
		seconds = 5 * 3600
	case domain.WindowDaily:
		seconds = 24 * 3600
	case domain.WindowSevenDays, domain.WindowWeekly:
		seconds = 7 * 24 * 3600
	case domain.WindowMonthly:
		seconds = 30 * 24 * 3600
	}
	used := int(math.Floor(100 * float64(limit.CurrentValue) / float64(limit.MaxValue)))
	used = max(0, min(100, used))
	resetEpoch := limit.ResetAt.Unix()
	after := max(0, int(limit.ResetAt.Sub(now).Seconds()))
	return &CodexKeyUsageWindow{UsedPercent: used, LimitWindowSeconds: &seconds,
		ResetAfterSeconds: &after, ResetAt: &resetEpoch}
}
