package application

import (
	"math"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

const (
	proWeeklyCapacityCredits = 50400
	weeklyRecentBurnWindow   = 6 * time.Hour
	weeklyFleetBurnWindow    = 3 * time.Hour
	weeklyDemandWindow       = 7 * 24 * time.Hour
	weeklyMinFreshness       = 5 * time.Minute
)

type WeeklyCreditPaceInput struct {
	Now                       time.Time
	RefreshInterval           time.Duration
	WorkingDays               []int
	SmoothingMinutes          int
	Accounts                  []WeeklyCreditPaceAccountInput
	TopAPIKeys                []domain.WeeklyCreditAPIKeyAttribution
	TrailingDemandUsedPercent map[string]float64
}

type WeeklyCreditPaceAccountInput struct {
	Account domain.Account
	Quota   domain.AccountQuota
	History []domain.AccountQuota
}

type weeklyPaceAccount struct {
	accountID   string
	fullCredits float64
	remaining   float64
	resetAtMS   float64
	windowMS    float64
}

type weeklyPaceProjection struct {
	shortfallCredits float64
	depletionHours   *float64
	minimumRemaining float64
}

func BuildWeeklyCreditPace(input WeeklyCreditPaceInput) *domain.WeeklyCreditPace {
	if input.Now.IsZero() || input.RefreshInterval <= 0 || input.RefreshInterval > math.MaxInt64/3 {
		return nil
	}
	smoothingMinutes, ok := weeklyPaceSmoothing(input.SmoothingMinutes)
	if !ok {
		return nil
	}
	workingDays, ok := weeklyPaceWorkingDays(input.WorkingDays)
	if !ok {
		return nil
	}
	now := input.Now.UTC()
	nowMS := float64(now.UnixMilli())
	if !finitePositive(nowMS) {
		return nil
	}
	freshnessCutoff := now.Add(-max(weeklyMinFreshness, 3*input.RefreshInterval))

	paceAccounts := make([]weeklyPaceAccount, 0)
	historyByAccount := make(map[string][]domain.AccountQuota, len(input.Accounts))
	staleAccounts, inactiveAccounts, rateSamples := 0, 0, 0
	totalFull, totalActual, totalSmoothed, totalExpected, scheduledRate := 0.0, 0.0, 0.0, 0.0, 0.0
	forecastRateSum := 0.0

	for _, item := range input.Accounts {
		timing, ok := weeklyPaceTiming(item, nowMS)
		if !ok {
			continue
		}
		if !weeklyPaceEligible(item.Account) {
			inactiveAccounts++
			continue
		}
		if item.Quota.ObservedAt.Before(freshnessCutoff) {
			staleAccounts++
			continue
		}
		history := weeklyPaceHistory(item.History, item.Account.ID, item.Quota.Window)
		historyByAccount[item.Account.ID] = history

		usedScheduleFraction := weeklyUsedScheduleFraction(timing.resetAtMS, timing.windowMS, nowMS, workingDays)
		expected := timing.fullCredits * (1 - usedScheduleFraction)
		accountRate := weeklyRecentBurnRate(history, timing.fullCredits, now)
		smoothed := weeklySmoothedRemaining(history, timing.fullCredits, timing.remaining, now, smoothingMinutes)

		totalFull += timing.fullCredits
		totalActual += timing.remaining
		totalSmoothed += smoothed
		totalExpected += expected
		scheduledRate += timing.fullCredits * weeklyScheduleSharePerHour(timing.resetAtMS, timing.windowMS, workingDays)
		if accountRate != nil {
			rateSamples++
			forecastRateSum += *accountRate
		}
		paceAccounts = append(paceAccounts, weeklyPaceAccount{
			accountID: item.Account.ID, fullCredits: timing.fullCredits, remaining: timing.remaining,
			resetAtMS: timing.resetAtMS, windowMS: timing.windowMS,
		})
	}

	if len(paceAccounts) == 0 || !finitePositive(totalFull) || !finite(totalActual) || !finite(totalSmoothed) ||
		!finite(totalExpected) || !finite(scheduledRate) || !finite(forecastRateSum) {
		return nil
	}
	actualUsed := 100 * (totalFull - totalActual) / totalFull
	scheduledUsed := 100 * (totalFull - totalExpected) / totalFull
	smoothedUsed := 100 * (totalFull - totalSmoothed) / totalFull
	scheduleGap := max(0, totalExpected-totalActual)
	smoothedGap := max(0, totalExpected-totalSmoothed)

	var forecastRate *float64
	if rateSamples > 0 {
		value := forecastRateSum
		forecastRate = &value
	}
	projection := weeklyProjectPool(paceAccounts, nowMS, forecastRate)
	if projection == nil {
		return nil
	}
	pauseHours, paceMultiplier, throttlePercent, reducePercent := weeklyForecastAdvice(
		projection.shortfallCredits, forecastRate, scheduledRate,
	)
	proEquivalent, proAccounts := weeklyProEquivalent(projection.shortfallCredits)

	headroomPercent := 100 * totalActual / totalFull
	recentBurnRate := weeklyFleetBurnRate(paceAccounts, historyByAccount, now)
	var depletionETA *float64
	if recentBurnRate != nil && *recentBurnRate > 0 {
		value := totalActual / *recentBurnRate
		depletionETA = &value
	}
	nextReliefHours, nextReliefCredits := weeklyNextRelief(paceAccounts, nowMS)
	runwayStatus := weeklyRunwayStatus(depletionETA, nextReliefHours, headroomPercent)
	saturatedAccounts := 0
	for _, account := range paceAccounts {
		if 100*(account.fullCredits-account.remaining)/account.fullCredits >= 99.5 {
			saturatedAccounts++
		}
	}
	addProAccounts := weeklyAddProAccounts(input.TrailingDemandUsedPercent, paceAccounts, totalFull, runwayStatus, saturatedAccounts)

	projectedMinimum := projection.minimumRemaining
	topAPIKeys := append([]domain.WeeklyCreditAPIKeyAttribution(nil), input.TopAPIKeys...)
	if topAPIKeys == nil {
		topAPIKeys = []domain.WeeklyCreditAPIKeyAttribution{}
	}

	return &domain.WeeklyCreditPace{
		TotalFullCredits: totalFull, TotalActualRemainingCredits: totalActual,
		TotalExpectedRemainingCredits: totalExpected, ActualUsedPercent: actualUsed,
		ScheduledUsedPercent: scheduledUsed, DeltaPercent: actualUsed - scheduledUsed,
		ScheduleGapCredits: scheduleGap, SmoothedDeltaPercent: smoothedUsed - scheduledUsed,
		SmoothedScheduleGapCredits: smoothedGap, PaceGapSmoothingMinutes: smoothingMinutes,
		OverPlanCredits: scheduleGap, ProjectedShortfallCredits: projection.shortfallCredits,
		PauseForBreakEvenHours: pauseHours, PaceMultiplier: paceMultiplier,
		ThrottleToPercent: throttlePercent, ReduceByPercent: reducePercent,
		ProAccountEquivalentToCoverOverPlan: proEquivalent, ProAccountsToCoverOverPlan: proAccounts,
		ProjectedDepletionHours: projection.depletionHours, ProjectedMinimumRemainingCredits: &projectedMinimum,
		ForecastBurnRateCreditsPerHour: forecastRate, ScheduledBurnRateCreditsPerHour: scheduledRate,
		HeadroomPercent: headroomPercent, HeadroomCredits: totalActual,
		BurnRateRecentCreditsPerHour: recentBurnRate, DepletionETAHours: depletionETA,
		NextReliefInHours: nextReliefHours, NextReliefCredits: nextReliefCredits,
		ResetEvents: weeklyResetEvents(paceAccounts, nowMS), RunwayStatus: runwayStatus,
		SaturatedAccountCount: saturatedAccounts, TopAPIKeys: topAPIKeys,
		AddProAccounts: addProAccounts, Status: weeklyLegacyStatus(runwayStatus),
		AccountCount: len(paceAccounts), StaleAccountCount: staleAccounts,
		InactiveAccountCount: inactiveAccounts, Confidence: weeklyConfidence(len(paceAccounts), rateSamples, staleAccounts),
	}
}

func weeklyPaceEligible(account domain.Account) bool {
	if account.Kind != domain.AccountChatGPT || account.RequiresEgressDecision {
		return false
	}
	switch account.Status {
	case domain.AccountActive, domain.AccountReauthRequired, domain.AccountRateLimited, domain.AccountQuotaExceeded:
		return true
	default:
		return false
	}
}

func weeklyConfidence(accountCount, rateSamples, staleAccounts int) domain.WeeklyCreditConfidence {
	if rateSamples >= accountCount && staleAccounts == 0 {
		return domain.WeeklyCreditConfidenceHigh
	}
	if rateSamples > 0 {
		return domain.WeeklyCreditConfidenceMedium
	}
	return domain.WeeklyCreditConfidenceLow
}

func weeklyLegacyStatus(status domain.WeeklyCreditRunwayStatus) domain.WeeklyCreditPaceStatus {
	switch status {
	case domain.WeeklyCreditRunwayRunsDry:
		return domain.WeeklyCreditPaceDanger
	case domain.WeeklyCreditRunwayTight:
		return domain.WeeklyCreditPaceAhead
	default:
		return domain.WeeklyCreditPaceOnTrack
	}
}

func weeklyForecastAdvice(shortfall float64, forecastRate *float64, scheduledRate float64) (*float64, *float64, *float64, *float64) {
	if forecastRate == nil || *forecastRate <= 0 || scheduledRate <= 0 || shortfall <= 0 {
		return nil, nil, nil, nil
	}
	pause := shortfall / *forecastRate
	multiplier := *forecastRate / scheduledRate
	throttle := clamp(scheduledRate / *forecastRate * 100, 0, 100)
	reduce := 100 - throttle
	return &pause, &multiplier, &throttle, &reduce
}

func weeklyProEquivalent(shortfall float64) (*float64, *int) {
	if shortfall <= 0 {
		return nil, nil
	}
	equivalent := shortfall / proWeeklyCapacityCredits
	count, ok := ceilCount(equivalent)
	if !ok {
		return nil, nil
	}
	return &equivalent, &count
}

func weeklyAddProAccounts(
	trailingDemand map[string]float64,
	accounts []weeklyPaceAccount,
	totalFull float64,
	runway domain.WeeklyCreditRunwayStatus,
	saturatedAccounts int,
) *int {
	if trailingDemand == nil {
		return nil
	}
	demandCredits := 0.0
	for _, account := range accounts {
		usedPercent := max(0, trailingDemand[account.accountID])
		if !finite(usedPercent) {
			return nil
		}
		demandCredits += account.fullCredits * usedPercent / 100
	}
	surplus := demandCredits/proWeeklyCapacityCredits - totalFull/proWeeklyCapacityCredits
	if surplus <= 0 || (runway != domain.WeeklyCreditRunwayRunsDry && saturatedAccounts == 0) {
		return nil
	}
	count, ok := ceilCount(surplus)
	if !ok {
		return nil
	}
	return &count
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clamp(value, minimum, maximum float64) float64 {
	return min(maximum, max(minimum, value))
}

func ceilCount(value float64) (int, bool) {
	count := math.Ceil(value)
	if !finite(count) || count < 0 || count > float64(math.MaxInt) {
		return 0, false
	}
	return int(count), true
}

func weeklyPaceHistory(history []domain.AccountQuota, accountID, window string) []domain.AccountQuota {
	rows := make([]domain.AccountQuota, 0, len(history))
	for _, row := range history {
		if row.AccountID != accountID || row.Window != window || row.ObservedAt.IsZero() ||
			row.ResetAt == nil || row.ResetAt.IsZero() || row.WindowMinutes == nil || *row.WindowMinutes <= 0 ||
			!finite(row.UsedPercent) {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ObservedAt.Before(rows[j].ObservedAt) })
	return rows
}
