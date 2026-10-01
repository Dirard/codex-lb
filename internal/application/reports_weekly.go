package application

import (
	"context"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// weeklyCreditPace joins persisted observations and operator settings for UC5.2.
// It never fetches provider data or changes routing, quota, or warmup state.
func (s *ReportsService) weeklyCreditPace(ctx context.Context, accounts []domain.Account, quotas map[string][]domain.AccountQuota, history []domain.AccountQuota, now time.Time) (*domain.WeeklyCreditPace, error) {
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return nil, err
	}
	demand, err := s.repo.PositiveQuotaDeltas(ctx, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	byAccount := make(map[string]map[string][]domain.AccountQuota)
	for _, row := range history {
		if byAccount[row.AccountID] == nil {
			byAccount[row.AccountID] = make(map[string][]domain.AccountQuota)
		}
		byAccount[row.AccountID][row.Window] = append(byAccount[row.AccountID][row.Window], row)
	}
	workingDays := []int{}
	for _, raw := range strings.Split(settings.WeeklyPaceWorkingDays, ",") {
		day, err := strconv.Atoi(raw)
		if err != nil {
			return nil, domain.ErrInvalid
		}
		workingDays = append(workingDays, day)
	}
	input := WeeklyCreditPaceInput{Now: now, RefreshInterval: time.Minute,
		WorkingDays: workingDays, SmoothingMinutes: settings.WeeklyPaceSmoothingMinutes,
		TrailingDemandUsedPercent: make(map[string]float64)}
	for _, account := range accounts {
		_, secondary := dashboardQuotaWindows(quotas[account.ID])
		if secondary == nil {
			continue
		}
		input.Accounts = append(input.Accounts, WeeklyCreditPaceAccountInput{Account: account,
			Quota: *secondary, History: byAccount[account.ID][secondary.Window]})
		input.TrailingDemandUsedPercent[account.ID] = demand[account.ID][secondary.Window]
	}
	pace := BuildWeeklyCreditPace(input)
	if pace != nil {
		pace.TopAPIKeys, err = s.repo.WeeklyPaceTopKeys(ctx, now.Add(-2*time.Hour), now)
	}
	return pace, err
}

// dashboardQuotaWindows normalizes weekly-only and monthly windows once for
// overview, depletion and weekly pace; raw reset timestamps remain unchanged.
func dashboardQuotaWindows(quotas []domain.AccountQuota) (primary, secondary *domain.AccountQuota) {
	var monthly *domain.AccountQuota
	for _, quota := range quotas {
		switch strings.ToLower(quota.Window) {
		case "primary":
			primary = &quota
		case "secondary":
			secondary = &quota
		case "monthly":
			monthly = &quota
		}
	}
	if secondary == nil {
		secondary = monthly
	}
	if primary != nil && primary.WindowMinutes != nil && *primary.WindowMinutes >= 10080 {
		if secondary == nil {
			secondary = primary
		}
		primary = nil
	}
	return primary, secondary
}
