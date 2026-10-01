package application

import (
	"context"
	"math"
	"time"

	"codex-lb/internal/domain"
)

// DashboardProjections uses persisted quota observations. With fewer than two
// usable observations in a window, the corresponding projection stays null.
func (s *ReportsService) DashboardProjections(ctx context.Context, accounts Accounts) (domain.DashboardProjections, error) {
	var result domain.DashboardProjections
	now := s.now().UTC()
	allAccounts, err := accounts.ListAccounts(ctx)
	if err != nil {
		return result, err
	}
	history, err := s.repo.RecentAccountQuotaHistory(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		return result, err
	}
	byAccount := make(map[string]map[string][]domain.AccountQuota)
	accountQuotas := make(map[string][]domain.AccountQuota, len(allAccounts))
	for _, row := range history {
		if byAccount[row.AccountID] == nil {
			byAccount[row.AccountID] = make(map[string][]domain.AccountQuota)
		}
		byAccount[row.AccountID][row.Window] = append(byAccount[row.AccountID][row.Window], row)
	}
	for _, account := range allAccounts {
		quotas, err := accounts.ListAccountQuota(ctx, account.ID)
		if err != nil {
			return result, err
		}
		accountQuotas[account.ID] = quotas
		if account.Kind != domain.AccountChatGPT || account.Status == domain.AccountDeactivated {
			continue
		}
		windows := byAccount[account.ID]
		primary, secondary := dashboardQuotaWindows(quotas)
		if primary != nil {
			result.DepletionPrimary = worseDepletion(result.DepletionPrimary, quotaDepletion(windows[primary.Window], now, 300))
		}
		if secondary != nil {
			result.DepletionSecondary = worseDepletion(result.DepletionSecondary, quotaDepletion(windows[secondary.Window], now, 10080))
		}
	}
	result.WeeklyCreditPace, err = s.weeklyCreditPace(ctx, allAccounts, accountQuotas, history, now)
	return result, err
}

func worseDepletion(current, next *domain.DashboardDepletion) *domain.DashboardDepletion {
	if current == nil || next != nil && next.Risk > current.Risk {
		return next
	}
	return current
}

func quotaDepletion(history []domain.AccountQuota, now time.Time, defaultMinutes int) *domain.DashboardDepletion {
	if len(history) < 2 {
		return nil
	}
	latest := history[len(history)-1]
	minutes := defaultMinutes
	if latest.WindowMinutes != nil {
		minutes = *latest.WindowMinutes
	}
	if minutes <= 0 || minutes > 43200 {
		return nil
	}
	cutoff := now.Add(-time.Duration(minutes) * time.Minute)
	var last *domain.AccountQuota
	var rate *float64
	for i := range history {
		row := &history[i]
		if row.ObservedAt.Before(cutoff) {
			continue
		}
		if last == nil {
			last = row
			continue
		}
		seconds := row.ObservedAt.Sub(last.ObservedAt).Seconds()
		if seconds <= 0 {
			continue
		}
		resetChanged := row.ResetAt != nil && last.ResetAt != nil && !row.ResetAt.Equal(*last.ResetAt)
		if row.UsedPercent < last.UsedPercent || resetChanged {
			rate = nil
		} else {
			raw := max(0, (row.UsedPercent-last.UsedPercent)/seconds)
			if rate == nil {
				rate = &raw
			} else {
				updated := 0.4*raw + 0.6*(*rate)
				rate = &updated
			}
		}
		last = row
	}
	if rate == nil || last == nil {
		return nil
	}
	secondsUntilReset := float64(minutes * 60)
	if latest.ResetAt != nil {
		secondsUntilReset = latest.ResetAt.Sub(now).Seconds()
		if secondsUntilReset <= 0 {
			return nil
		}
	}
	remaining := 100 - latest.UsedPercent
	risk := max(0, min(1, (latest.UsedPercent+*rate*secondsUntilReset)/100))
	level := "safe"
	switch {
	case risk >= 0.95:
		level = "critical"
	case risk >= 0.8:
		level = "danger"
	case risk >= 0.6:
		level = "warning"
	}
	result := &domain.DashboardDepletion{Risk: risk, RiskLevel: level,
		SafeUsagePercent: max(0, min(100, 100*(1-secondsUntilReset/float64(minutes*60))))}
	if *rate > 0 && remaining > 0 {
		result.BurnRate = *rate / (remaining / secondsUntilReset)
		seconds := remaining / *rate
		if seconds >= 0 && seconds <= secondsUntilReset && !math.IsInf(seconds, 0) {
			result.SecondsUntilExhaustion = &seconds
			at := now.Add(time.Duration(seconds * float64(time.Second)))
			result.ProjectedExhaustionAt = &at
		}
	}
	return result
}
