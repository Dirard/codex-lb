package application

import (
	"math"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

type weeklyPaceTimingData struct {
	fullCredits float64
	remaining   float64
	resetAtMS   float64
	windowMS    float64
}

func weeklyPaceSmoothing(minutes int) (int, bool) {
	if minutes == 0 {
		return 30, true
	}
	switch minutes {
	case 15, 30, 60, 120, 240:
		return minutes, true
	default:
		return 0, false
	}
}

func weeklyPaceWorkingDays(days []int) (map[int]struct{}, bool) {
	if len(days) == 0 {
		return nil, true
	}
	working := make(map[int]struct{}, len(days))
	for _, day := range days {
		if day < 0 || day > 6 {
			return nil, false
		}
		working[day] = struct{}{}
	}
	if len(working) == 7 {
		return nil, true
	}
	return working, true
}

func weeklyPaceTiming(item WeeklyCreditPaceAccountInput, nowMS float64) (weeklyPaceTimingData, bool) {
	account, quota := item.Account, item.Quota
	if account.ID == "" || quota.AccountID != account.ID || quota.Window == "" ||
		quota.ResetAt == nil || quota.ResetAt.IsZero() || quota.WindowMinutes == nil || *quota.WindowMinutes <= 0 || *quota.WindowMinutes > 32*24*60 ||
		quota.ObservedAt.IsZero() || float64(quota.ObservedAt.UnixMilli()) > nowMS || !finite(quota.UsedPercent) {
		return weeklyPaceTimingData{}, false
	}
	fullCredits := keyUsageCapacity(account.PlanType, "secondary")
	resetAtMS := float64(quota.ResetAt.UnixMilli())
	windowMS := float64(*quota.WindowMinutes) * 60_000
	if !finitePositive(fullCredits) || !finitePositive(resetAtMS) || !finitePositive(windowMS) ||
		quota.UsedPercent < 0 || !finite(resetAtMS-windowMS) || !finite(nowMS+2*windowMS) {
		return weeklyPaceTimingData{}, false
	}
	resetAtMS = weeklyAdvanceReset(resetAtMS, windowMS, nowMS)
	if !finitePositive(resetAtMS) {
		return weeklyPaceTimingData{}, false
	}
	return weeklyPaceTimingData{
		fullCredits: fullCredits,
		remaining:   clamp(fullCredits*(1-clamp(quota.UsedPercent, 0, 100)/100), 0, fullCredits),
		resetAtMS:   resetAtMS,
		windowMS:    windowMS,
	}, true
}

func weeklyAdvanceReset(resetAtMS, windowMS, nowMS float64) float64 {
	if resetAtMS > nowMS {
		return resetAtMS
	}
	missedWindows := math.Floor((nowMS-resetAtMS)/windowMS) + 1
	return resetAtMS + missedWindows*windowMS
}

func weeklyUsedScheduleFraction(resetAtMS, windowMS, nowMS float64, workingDays map[int]struct{}) float64 {
	windowStartMS := resetAtMS - windowMS
	elapsedMS := clamp(nowMS-windowStartMS, 0, windowMS)
	if elapsedMS <= 0 {
		return 0
	}
	if workingDays == nil {
		return elapsedMS / windowMS
	}
	totalWorkingMS := weeklyWorkingDurationMS(windowStartMS, resetAtMS, workingDays)
	if totalWorkingMS <= 0 {
		return elapsedMS / windowMS
	}
	return clamp(weeklyWorkingDurationMS(windowStartMS, windowStartMS+elapsedMS, workingDays)/totalWorkingMS, 0, 1)
}

func weeklyScheduleSharePerHour(resetAtMS, windowMS float64, workingDays map[int]struct{}) float64 {
	if workingDays == nil {
		return 3_600_000 / windowMS
	}
	windowStartMS := resetAtMS - windowMS
	totalWorkingMS := weeklyWorkingDurationMS(windowStartMS, resetAtMS, workingDays)
	if totalWorkingMS <= 0 {
		return 3_600_000 / windowMS
	}
	return 3_600_000 / totalWorkingMS
}

func weeklyWorkingDurationMS(startMS, endMS float64, workingDays map[int]struct{}) float64 {
	if endMS <= startMS {
		return 0
	}
	cursorMS, totalMS := startMS, 0.0
	for cursorMS < endMS {
		dayStartMS := math.Floor(cursorMS/86_400_000) * 86_400_000
		nextDayMS := dayStartMS + 86_400_000
		segmentEndMS := min(endMS, nextDayMS)
		if _, working := workingDays[weeklyUTCWeekday(cursorMS)]; working {
			totalMS += segmentEndMS - cursorMS
		}
		cursorMS = segmentEndMS
	}
	return totalMS
}

func weeklyUTCWeekday(epochMS float64) int {
	days := math.Floor(epochMS / 86_400_000)
	return int(((int64(days)+3)%7 + 7) % 7)
}

func weeklyRecentBurnRate(history []domain.AccountQuota, fullCredits float64, now time.Time) *float64 {
	rows := make([]domain.AccountQuota, 0, len(history))
	for _, row := range history {
		if !row.ObservedAt.After(now) && row.ObservedAt.After(now.Add(-weeklyRecentBurnWindow)) {
			rows = append(rows, row)
		}
	}
	if len(rows) < 2 {
		return nil
	}
	var state *weeklyEWMAStruct
	for _, row := range rows {
		state = weeklyEWMAUpdate(state, row)
	}
	if state == nil || state.rate == nil || !finite(*state.rate) {
		return nil
	}
	rate := max(0, *state.rate*fullCredits*36)
	if !finite(rate) {
		return nil
	}
	return &rate
}

type weeklyEWMAStruct struct {
	rate      *float64
	lastUsed  float64
	lastAt    float64
	lastReset *time.Time
}

func weeklyEWMAUpdate(state *weeklyEWMAStruct, row domain.AccountQuota) *weeklyEWMAStruct {
	timestamp := float64(row.ObservedAt.Unix())
	resetAt := row.ResetAt
	if state == nil {
		return &weeklyEWMAStruct{lastUsed: row.UsedPercent, lastAt: timestamp, lastReset: resetAt}
	}
	dt := timestamp - state.lastAt
	if dt == 0 {
		return state
	}
	windowChanged := state.lastReset != nil && resetAt != nil && !state.lastReset.Equal(*resetAt)
	if state.lastUsed-row.UsedPercent > 0 || windowChanged {
		return &weeklyEWMAStruct{lastUsed: row.UsedPercent, lastAt: timestamp, lastReset: resetAt}
	}
	rawRate := max(0, (row.UsedPercent-state.lastUsed)/dt)
	rate := rawRate
	if state.rate != nil {
		rate = 0.4*rawRate + 0.6**state.rate
	}
	return &weeklyEWMAStruct{rate: &rate, lastUsed: row.UsedPercent, lastAt: timestamp, lastReset: resetAt}
}

func weeklySmoothedRemaining(
	history []domain.AccountQuota,
	fullCredits, currentRemaining float64,
	now time.Time,
	smoothingMinutes int,
) float64 {
	smoothingStart := now.Add(-time.Duration(smoothingMinutes) * time.Minute)
	if len(history) == 0 {
		return currentRemaining
	}
	latest := history[len(history)-1]
	totalRemaining, samples := 0.0, 0
	for _, row := range history {
		if row.ObservedAt.Before(smoothingStart) || row.ObservedAt.After(now) ||
			latest.ResetAt == nil || row.ResetAt == nil || !latest.ResetAt.Equal(*row.ResetAt) ||
			latest.WindowMinutes == nil || row.WindowMinutes == nil || *latest.WindowMinutes != *row.WindowMinutes {
			continue
		}
		totalRemaining += fullCredits * (1 - clamp(row.UsedPercent, 0, 100)/100)
		samples++
	}
	if samples == 0 {
		return currentRemaining
	}
	return clamp(totalRemaining/float64(samples), 0, fullCredits)
}

func weeklyFleetBurnRate(accounts []weeklyPaceAccount, history map[string][]domain.AccountQuota, now time.Time) *float64 {
	windowStart := now.Add(-weeklyFleetBurnWindow)
	totalBurn := 0.0
	considered := make([]time.Time, 0)
	for _, account := range accounts {
		rows := make([]domain.AccountQuota, 0, len(history[account.accountID]))
		for _, row := range history[account.accountID] {
			if !row.ObservedAt.Before(windowStart) && !row.ObservedAt.After(now) {
				rows = append(rows, row)
			}
		}
		if len(rows) < 2 {
			continue
		}
		considered = append(considered, rows[0].ObservedAt, rows[len(rows)-1].ObservedAt)
		for i := 1; i < len(rows); i++ {
			if delta := rows[i].UsedPercent - rows[i-1].UsedPercent; delta > 0 {
				totalBurn += account.fullCredits * delta / 100
			}
		}
	}
	if len(considered) == 0 || !finite(totalBurn) {
		return nil
	}
	earliest, latest := considered[0], considered[0]
	for _, at := range considered[1:] {
		earliest = minTime(earliest, at)
		latest = maxTime(latest, at)
	}
	spanHours := latest.Sub(earliest).Hours()
	rate := totalBurn / max(0.5, spanHours)
	if !finite(rate) {
		return nil
	}
	return &rate
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func weeklyNextRelief(accounts []weeklyPaceAccount, nowMS float64) (float64, float64) {
	cohort := make([]weeklyPaceAccount, 0, len(accounts))
	for _, account := range accounts {
		if weeklyUsedPercent(account) >= 95 {
			cohort = append(cohort, account)
		}
	}
	if len(cohort) == 0 {
		cohort = accounts
	}
	soonestResetMS := cohort[0].resetAtMS
	for _, account := range cohort[1:] {
		soonestResetMS = min(soonestResetMS, account.resetAtMS)
	}
	clusterEndMS := soonestResetMS + 3_600_000
	reliefCredits := 0.0
	for _, account := range cohort {
		if account.resetAtMS <= clusterEndMS {
			reliefCredits += account.fullCredits * weeklyUsedPercent(account) / 100
		}
	}
	return max(0, (soonestResetMS-nowMS)/3_600_000), reliefCredits
}

func weeklyUsedPercent(account weeklyPaceAccount) float64 {
	return 100 * (account.fullCredits - account.remaining) / account.fullCredits
}

func weeklyResetEvents(accounts []weeklyPaceAccount, nowMS float64) []domain.WeeklyCreditResetEvent {
	horizonMS := nowMS + float64(weeklyDemandWindow.Milliseconds())
	events := make([]domain.WeeklyCreditResetEvent, 0, len(accounts))
	sorted := append([]weeklyPaceAccount(nil), accounts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].resetAtMS < sorted[j].resetAtMS })
	for _, account := range sorted {
		if account.resetAtMS > horizonMS {
			continue
		}
		events = append(events, domain.WeeklyCreditResetEvent{
			At: time.UnixMilli(int64(account.resetAtMS)).UTC(), CreditsReturned: account.fullCredits * weeklyUsedPercent(account) / 100,
		})
	}
	return events
}

func weeklyRunwayStatus(depletionETA *float64, nextReliefHours, headroomPercent float64) domain.WeeklyCreditRunwayStatus {
	if depletionETA != nil && *depletionETA < nextReliefHours {
		return domain.WeeklyCreditRunwayRunsDry
	}
	if (depletionETA != nil && *depletionETA-nextReliefHours < 24) || headroomPercent < 12 {
		return domain.WeeklyCreditRunwayTight
	}
	return domain.WeeklyCreditRunwaySafe
}

type weeklySimulationAccount struct {
	fullCredits float64
	balance     float64
	resetAtMS   float64
	windowMS    float64
}

func weeklyProjectPool(accounts []weeklyPaceAccount, nowMS float64, forecastRate *float64) *weeklyPaceProjection {
	totalRemaining := 0.0
	for _, account := range accounts {
		totalRemaining += account.remaining
	}
	if forecastRate == nil || *forecastRate <= 0 {
		return &weeklyPaceProjection{shortfallCredits: 0, depletionHours: nil, minimumRemaining: totalRemaining}
	}
	burnPerMS := *forecastRate / 3_600_000
	simulation := make([]weeklySimulationAccount, 0, len(accounts))
	maxWindowMS := 0.0
	for _, account := range accounts {
		simulation = append(simulation, weeklySimulationAccount{
			fullCredits: account.fullCredits, balance: account.remaining,
			resetAtMS: account.resetAtMS, windowMS: account.windowMS,
		})
		maxWindowMS = max(maxWindowMS, account.windowMS)
	}
	horizonMS := nowMS + 2*maxWindowMS
	cursorMS := nowMS
	minimumRemaining := totalRemaining

	// ponytail: bounded reset simulation; persisted quota windows are minutes,
	// so 100k cycles is far above real inputs and prevents hostile tiny windows.
	for cycles := 0; cursorMS < horizonMS && cycles < 100_000; cycles++ {
		sort.SliceStable(simulation, func(i, j int) bool { return simulation[i].resetAtMS < simulation[j].resetAtMS })
		next := &simulation[0]
		nextEventMS := min(next.resetAtMS, horizonMS)
		intervalMS := max(0, nextEventMS-cursorMS)
		intervalBurn := burnPerMS * intervalMS
		totalBalance := weeklySimulationBalance(simulation)
		if intervalBurn > totalBalance {
			depletionHours := (cursorMS - nowMS + totalBalance/burnPerMS) / 3_600_000
			return &weeklyPaceProjection{
				shortfallCredits: intervalBurn - totalBalance, depletionHours: &depletionHours,
				minimumRemaining: 0,
			}
		}
		weeklyConsumeSimulation(simulation, intervalBurn)
		minimumRemaining = min(minimumRemaining, weeklySimulationBalance(simulation))
		cursorMS = nextEventMS
		if cursorMS >= horizonMS {
			break
		}
		next.balance = next.fullCredits
		next.resetAtMS += next.windowMS
		minimumRemaining = min(minimumRemaining, weeklySimulationBalance(simulation))
	}
	if cursorMS < horizonMS {
		return nil // A bounded simulation must not report a truncated horizon as safe.
	}
	return &weeklyPaceProjection{shortfallCredits: 0, depletionHours: nil, minimumRemaining: minimumRemaining}
}

func weeklyConsumeSimulation(accounts []weeklySimulationAccount, amount float64) {
	sort.SliceStable(accounts, func(i, j int) bool { return accounts[i].resetAtMS < accounts[j].resetAtMS })
	remaining := amount
	for i := range accounts {
		if remaining <= 0 {
			return
		}
		consumed := min(accounts[i].balance, remaining)
		accounts[i].balance -= consumed
		remaining -= consumed
	}
}

func weeklySimulationBalance(accounts []weeklySimulationAccount) float64 {
	total := 0.0
	for _, account := range accounts {
		total += account.balance
	}
	return total
}
