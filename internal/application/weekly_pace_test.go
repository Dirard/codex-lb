package application

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

var weeklyPaceNow = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func weeklyPaceTestAccount(id string, status domain.AccountStatus) domain.Account {
	return domain.Account{ID: id, Kind: domain.AccountChatGPT, PlanType: "pro", Status: status, RoutingPolicy: "normal"}
}

func weeklyPaceQuota(id, window string, usedPercent float64, resetIn time.Duration, observedAgo time.Duration, windowMinutes int) domain.AccountQuota {
	resetAt := weeklyPaceNow.Add(resetIn)
	return domain.AccountQuota{
		AccountID: id, Window: window, UsedPercent: usedPercent, ResetAt: &resetAt,
		WindowMinutes: &windowMinutes, ObservedAt: weeklyPaceNow.Add(-observedAgo),
	}
}

func weeklyPaceBuild(items ...WeeklyCreditPaceAccountInput) *domain.WeeklyCreditPace {
	return BuildWeeklyCreditPace(WeeklyCreditPaceInput{
		Now: weeklyPaceNow, RefreshInterval: time.Minute, Accounts: items,
	})
}

func TestWeeklyPaceVerdictBranchesMatchLegacyStatuses(t *testing.T) {
	tests := []struct {
		name, runway, legacyStatus string
		used                       float64
		reset                      time.Duration
		delta                      float64
	}{
		{"runs_dry", "runs_dry", "danger", 90, 5 * time.Hour, 10},
		{"tight", "tight", "ahead", 80, time.Hour, 10},
		{"safe", "safe", "on_track", 20, time.Hour, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := "account-" + test.name
			history := weeklyPaceThreeHourHistory(id, test.used, test.delta)
			current := weeklyPaceQuota(id, "secondary", test.used, test.reset, time.Minute, 10080)
			pace := weeklyPaceBuild(WeeklyCreditPaceAccountInput{
				Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: current, History: history,
			})
			if pace == nil {
				t.Fatal("expected pace")
			}
			if string(pace.RunwayStatus) != test.runway || string(pace.Status) != test.legacyStatus {
				t.Fatalf("status = %s/%s, want %s/%s; eta=%v relief=%v headroom=%v rate=%v",
					pace.Status, pace.RunwayStatus, test.legacyStatus, test.runway,
					pace.DepletionETAHours, pace.NextReliefInHours, pace.HeadroomPercent, pace.BurnRateRecentCreditsPerHour)
			}
			if pace.BurnRateRecentCreditsPerHour == nil || pace.DepletionETAHours == nil {
				t.Fatal("expected runway rate inputs")
			}
		})
	}
}

func weeklyPaceThreeHourHistory(id string, finalUsed, hourlyDelta float64) []domain.AccountQuota {
	values := []float64{
		finalUsed - 3*hourlyDelta, finalUsed - 2*hourlyDelta, finalUsed - 2*hourlyDelta,
		finalUsed - hourlyDelta, finalUsed - hourlyDelta, finalUsed,
	}
	offsets := []time.Duration{170 * time.Minute, 130 * time.Minute, 110 * time.Minute, 70 * time.Minute, 50 * time.Minute, time.Minute}
	rows := make([]domain.AccountQuota, len(values))
	for i := range rows {
		rows[i] = weeklyPaceQuota(id, "secondary", values[i], 24*time.Hour, offsets[i], 10080)
	}
	return rows
}

func TestWeeklyPaceReliefCohortAndFallback(t *testing.T) {
	cluster := weeklyPaceBuild(
		weeklyPaceOneSample("first", 96, 2*time.Hour),
		weeklyPaceOneSample("clustered", 97, 150*time.Minute),
		weeklyPaceOneSample("later", 98, 5*time.Hour),
	)
	if cluster.NextReliefInHours != 2 || cluster.NextReliefCredits != 50400*1.93 || len(cluster.ResetEvents) != 3 {
		t.Fatalf("cluster relief = %v/%v/%d", cluster.NextReliefInHours, cluster.NextReliefCredits, len(cluster.ResetEvents))
	}

	fallback := weeklyPaceBuild(
		weeklyPaceOneSample("low-a", 40, 3*time.Hour),
		weeklyPaceOneSample("low-b", 20, 6*time.Hour),
	)
	if fallback.NextReliefInHours != 3 || fallback.NextReliefCredits != 50400*.4 {
		t.Fatalf("fallback relief = %v/%v", fallback.NextReliefInHours, fallback.NextReliefCredits)
	}
}

func weeklyPaceOneSample(id string, used float64, resetIn time.Duration) WeeklyCreditPaceAccountInput {
	quota := weeklyPaceQuota(id, "secondary", used, resetIn, time.Minute, 10080)
	return WeeklyCreditPaceAccountInput{Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: quota, History: []domain.AccountQuota{quota}}
}

func TestWeeklyPaceFleetRecentBurnLegacyNumbers(t *testing.T) {
	tests := []struct {
		name  string
		rows  []domain.AccountQuota
		wants *float64
	}{
		{
			"crosses_hour",
			[]domain.AccountQuota{
				weeklyPaceQuota("a", "secondary", 80, 2*time.Hour, time.Minute, 10080),
				weeklyPaceQuota("a", "secondary", 70, 2*time.Hour, 59*time.Minute, 10080),
				weeklyPaceQuota("a", "secondary", 70, 2*time.Hour, 61*time.Minute, 10080),
			},
			floatPointer(5040),
		},
		{
			"short_span_floor",
			[]domain.AccountQuota{
				weeklyPaceQuota("a", "secondary", 80, 2*time.Hour, time.Minute, 10080),
				weeklyPaceQuota("a", "secondary", 70, 2*time.Hour, 6*time.Minute, 10080),
			},
			floatPointer(10080),
		},
		{
			"reset_drop_is_not_burn",
			[]domain.AccountQuota{
				weeklyPaceQuota("a", "secondary", 10, 2*time.Hour, time.Minute, 10080),
				weeklyPaceQuota("a", "secondary", 80, 2*time.Hour, 6*time.Minute, 10080),
			},
			floatPointer(0),
		},
		{
			"single_sample",
			[]domain.AccountQuota{weeklyPaceQuota("a", "secondary", 80, 2*time.Hour, time.Minute, 10080)},
			nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := WeeklyCreditPaceAccountInput{Account: weeklyPaceTestAccount("a", domain.AccountActive), Quota: test.rows[0], History: test.rows}
			pace := weeklyPaceBuild(item)
			if test.wants == nil {
				if pace.BurnRateRecentCreditsPerHour != nil {
					t.Fatalf("recent burn = %v, want nil", *pace.BurnRateRecentCreditsPerHour)
				}
				return
			}
			if pace.BurnRateRecentCreditsPerHour == nil || !floatEqual(*pace.BurnRateRecentCreditsPerHour, *test.wants) {
				t.Fatalf("recent burn = %v, want %v", pace.BurnRateRecentCreditsPerHour, *test.wants)
			}
		})
	}
}

func TestWeeklyPaceForecastUsesSixHourEWMANumericLegacyRate(t *testing.T) {
	id := "ewma"
	history := weeklyPaceThreeHourHistory(id, 20, 1)
	current := weeklyPaceQuota(id, "secondary", 20, time.Hour, time.Minute, 10080)
	pace := weeklyPaceBuild(WeeklyCreditPaceAccountInput{
		Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: current, History: history,
	})
	if pace.ForecastBurnRateCreditsPerHour == nil ||
		!floatEqual(*pace.ForecastBurnRateCreditsPerHour, 453.6987428571429) {
		t.Fatalf("EWMA forecast = %v", *pace.ForecastBurnRateCreditsPerHour)
	}

	changed := weeklyPaceQuota(id, "secondary", 20, time.Hour, time.Minute, 10080)
	older := weeklyPaceQuota(id, "secondary", 10, 25*time.Hour, 10*time.Minute, 10080)
	pace = weeklyPaceBuild(WeeklyCreditPaceAccountInput{
		Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: changed,
		History: []domain.AccountQuota{older, changed},
	})
	if pace.ForecastBurnRateCreditsPerHour != nil {
		t.Fatalf("forecast crossed reset: %v", *pace.ForecastBurnRateCreditsPerHour)
	}
}

func floatPointer(value float64) *float64 { return &value }

func floatEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b))
}

func TestWeeklyPaceNearResetIsRelief(t *testing.T) {
	id := "near-reset"
	history := weeklyPaceThreeHourHistory(id, 99, 0)
	current := weeklyPaceQuota(id, "secondary", 99, 100*time.Minute+48*time.Second, time.Minute, 10080)
	pace := weeklyPaceBuild(WeeklyCreditPaceAccountInput{
		Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: current, History: history,
	})
	if pace.NextReliefInHours != 1.68 || pace.NextReliefCredits != 50400*.99 ||
		pace.DepletionETAHours != nil || pace.RunwayStatus != domain.WeeklyCreditRunwayTight || pace.Status != domain.WeeklyCreditPaceAhead {
		t.Fatalf("near-reset eta=%v relief=%v credits=%v runway=%s status=%s",
			pace.DepletionETAHours, pace.NextReliefInHours, pace.NextReliefCredits, pace.RunwayStatus, pace.Status)
	}
}

func TestWeeklyPaceCountsSaturatedAccounts(t *testing.T) {
	pace := weeklyPaceBuild(weeklyPaceOneSample("saturated", 99.5, 2*time.Hour), weeklyPaceOneSample("not", 99.49, 2*time.Hour))
	if pace.SaturatedAccountCount != 1 {
		t.Fatalf("saturated = %d", pace.SaturatedAccountCount)
	}
}

func TestWeeklyPaceAdditionalProAccountsUsesCapacityBasis(t *testing.T) {
	saturated := weeklyPaceOneSample("pro", 99.5, 2*time.Hour)
	plus := weeklyPaceOneSample("plus", 99.5, 2*time.Hour)
	plus.Account.PlanType = "plus"
	plus.Quota.UsedPercent = 99.5
	plus.History[0].UsedPercent = 99.5
	pace := BuildWeeklyCreditPace(WeeklyCreditPaceInput{
		Now: weeklyPaceNow, RefreshInterval: time.Minute, Accounts: []WeeklyCreditPaceAccountInput{saturated, plus},
		TrailingDemandUsedPercent: map[string]float64{"pro": 110, "plus": 300},
	})
	if pace.SaturatedAccountCount != 2 || pace.AddProAccounts == nil || *pace.AddProAccounts != 1 {
		t.Fatalf("mixed fleet = saturated %d add %v", pace.SaturatedAccountCount, pace.AddProAccounts)
	}

	alone := weeklyPaceOneSample("pro-demand", 99.5, 2*time.Hour)
	pace = BuildWeeklyCreditPace(WeeklyCreditPaceInput{
		Now: weeklyPaceNow, RefreshInterval: time.Minute, Accounts: []WeeklyCreditPaceAccountInput{alone},
		TrailingDemandUsedPercent: map[string]float64{"pro-demand": 277.5},
	})
	if pace.AddProAccounts == nil || *pace.AddProAccounts != 2 {
		t.Fatalf("add Pro accounts = %v, want 2", pace.AddProAccounts)
	}
}

func TestWeeklyPaceNoRateSamplesRemainsNullableNotZero(t *testing.T) {
	pace := weeklyPaceBuild(weeklyPaceOneSample("no-rate", 20, time.Hour))
	if pace.ForecastBurnRateCreditsPerHour != nil || pace.BurnRateRecentCreditsPerHour != nil ||
		pace.ProjectedDepletionHours != nil || pace.DepletionETAHours != nil || pace.ProjectedShortfallCredits != 0 ||
		pace.Confidence != domain.WeeklyCreditConfidenceLow {
		t.Fatalf("nullable pace = %+v", pace)
	}
}

func TestWeeklyPaceAcceptsCanonicalWeeklyWindow(t *testing.T) {
	item := weeklyPaceOneSample("canonical", 20, time.Hour)
	item.Quota.Window = "primary"
	item.History[0].Window = "primary"
	if pace := weeklyPaceBuild(item); pace == nil || pace.AccountCount != 1 {
		t.Fatalf("canonical weekly-primary pace = %+v", pace)
	}
}

func TestWeeklyPaceUsesEnterpriseSecondaryCapacity(t *testing.T) {
	item := weeklyPaceOneSample("enterprise", 20, time.Hour)
	item.Account.PlanType = "enterprise"
	pace := weeklyPaceBuild(item)
	if pace == nil || pace.TotalFullCredits != 50400 {
		t.Fatalf("enterprise capacity = %+v", pace)
	}
	if summary := quotaAccount(item.Account, &item.Quota, "secondary"); summary.CapacityCredits != pace.TotalFullCredits {
		t.Fatalf("overview and weekly pace disagree on enterprise capacity: %+v vs %+v", summary, pace)
	}
}

func TestWeeklyPaceFreshnessUsesThreeRefreshCycles(t *testing.T) {
	fresh := weeklyPaceOneSample("fresh-slow", 20, time.Hour)
	fresh.Quota.ObservedAt = weeklyPaceNow.Add(-5 * time.Minute)
	stale := weeklyPaceOneSample("stale-slow", 20, time.Hour)
	stale.Quota.ObservedAt = weeklyPaceNow.Add(-7 * time.Minute)
	input := func(items ...WeeklyCreditPaceAccountInput) WeeklyCreditPaceInput {
		return WeeklyCreditPaceInput{Now: weeklyPaceNow, RefreshInterval: 2 * time.Minute, Accounts: items}
	}
	if BuildWeeklyCreditPace(input(fresh)) == nil || BuildWeeklyCreditPace(input(stale)) != nil {
		t.Fatal("refresh freshness boundary changed")
	}
}

func TestWeeklyPaceScheduleUsesUTCWorkingDaysAndDefault(t *testing.T) {
	working := weeklyPaceOneSample("schedule", 80, 6*time.Hour)
	pace := BuildWeeklyCreditPace(WeeklyCreditPaceInput{
		Now: weeklyPaceNow, RefreshInterval: time.Minute, WorkingDays: []int{0},
		Accounts: []WeeklyCreditPaceAccountInput{working},
	})
	if !floatEqual(pace.TotalExpectedRemainingCredits, 50400*.25) || !floatEqual(pace.ScheduleGapCredits, 50400*.05) ||
		!floatEqual(pace.ScheduledBurnRateCreditsPerHour, 2100) || !floatEqual(pace.ScheduledUsedPercent, 75) {
		t.Fatalf("working-day expected=%v gap=%v rate=%v used=%v",
			pace.TotalExpectedRemainingCredits, pace.ScheduleGapCredits, pace.ScheduledBurnRateCreditsPerHour, pace.ScheduledUsedPercent)
	}

	pace = weeklyPaceBuild(working)
	if !floatEqual(pace.TotalExpectedRemainingCredits, 50400/28) || !floatEqual(pace.ScheduledBurnRateCreditsPerHour, 300) ||
		!floatEqual(pace.ActualUsedPercent, 80) || !floatEqual(pace.DeltaPercent, 80-2700/28.0) {
		t.Fatalf("default expected=%v rate=%v actual=%v delta=%v",
			pace.TotalExpectedRemainingCredits, pace.ScheduledBurnRateCreditsPerHour, pace.ActualUsedPercent, pace.DeltaPercent)
	}
}

func TestWeeklyPaceSmoothingDoesNotCrossResetWindow(t *testing.T) {
	id := "smooth"
	currentReset := weeklyPaceNow.Add(time.Hour)
	oldReset := weeklyPaceNow.Add(72 * time.Hour)
	current := domain.AccountQuota{
		AccountID: id, Window: "secondary", UsedPercent: 30, ResetAt: &currentReset,
		WindowMinutes: pointerTo(60), ObservedAt: weeklyPaceNow.Add(-time.Minute),
	}
	old := current
	old.UsedPercent, old.ResetAt, old.ObservedAt = 10, &oldReset, weeklyPaceNow.Add(-2*time.Minute)
	recent := current
	recent.UsedPercent, recent.ObservedAt = 0, weeklyPaceNow.Add(-20*time.Minute)
	pace := weeklyPaceBuild(WeeklyCreditPaceAccountInput{
		Account: weeklyPaceTestAccount(id, domain.AccountActive), Quota: current,
		History: []domain.AccountQuota{old, recent, current},
	})
	if pace.SmoothedDeltaPercent != 15 || pace.SmoothedScheduleGapCredits != 50400*.15 ||
		pace.PaceGapSmoothingMinutes != 30 {
		t.Fatalf("smoothed delta=%v gap=%v minutes=%d",
			pace.SmoothedDeltaPercent, pace.SmoothedScheduleGapCredits, pace.PaceGapSmoothingMinutes)
	}
}

func pointerTo[T any](value T) *T { return &value }

func TestWeeklyPaceAdvancesExpiredResetOnlyForProjection(t *testing.T) {
	item := weeklyPaceOneSample("expired", 40, -30*time.Minute)
	item.Quota.WindowMinutes = pointerTo(60)
	item.Quota.UsedPercent = 40
	pace := weeklyPaceBuild(item)
	if len(pace.ResetEvents) != 1 || !pace.ResetEvents[0].At.Equal(weeklyPaceNow.Add(30*time.Minute)) ||
		pace.NextReliefInHours != .5 || pace.TotalActualRemainingCredits != 50400*.6 {
		t.Fatalf("expired reset = %+v", pace)
	}
}

func TestWeeklyProjectPoolConsumesInResetOrder(t *testing.T) {
	projection := weeklyProjectPool([]weeklyPaceAccount{
		{fullCredits: 100, remaining: 40, resetAtMS: float64(weeklyPaceNow.Add(time.Hour).UnixMilli()), windowMS: 3 * 3_600_000},
		{fullCredits: 100, remaining: 50, resetAtMS: float64(weeklyPaceNow.Add(90 * time.Minute).UnixMilli()), windowMS: 2 * 3_600_000},
	}, float64(weeklyPaceNow.UnixMilli()), floatPointer(90))
	if !floatEqual(projection.shortfallCredits, 25) || projection.depletionHours == nil ||
		!floatEqual(*projection.depletionHours, 29.0/9.0) || projection.minimumRemaining != 0 {
		t.Fatalf("projection shortfall=%v depletion=%v minimum=%v",
			projection.shortfallCredits, *projection.depletionHours, projection.minimumRemaining)
	}
}

func TestWeeklyProjectPoolDoesNotReportTruncatedHorizonAsSafe(t *testing.T) {
	now := float64(weeklyPaceNow.UnixMilli())
	minute := float64(time.Minute.Milliseconds())
	month := float64((32 * 24 * time.Hour).Milliseconds())
	projection := weeklyProjectPool([]weeklyPaceAccount{
		{fullCredits: 100, remaining: 100, resetAtMS: now + minute, windowMS: minute},
		{fullCredits: 100, remaining: 100, resetAtMS: now + minute, windowMS: minute},
		{fullCredits: 100, remaining: 100, resetAtMS: now + month, windowMS: month},
	}, now, floatPointer(1e-9))
	if projection != nil {
		t.Fatalf("bounded simulation invented a completed safe forecast: %+v", projection)
	}
}

func TestWeeklyPaceRejectsInvalidDataAndCountsIneligible(t *testing.T) {
	stale := weeklyPaceOneSample("stale", 20, time.Hour)
	stale.Quota.ObservedAt = weeklyPaceNow.Add(-6 * time.Minute)
	if weeklyPaceBuild(stale) != nil {
		t.Fatal("stale-only pace must be nil")
	}
	if BuildWeeklyCreditPace(WeeklyCreditPaceInput{Now: weeklyPaceNow, RefreshInterval: time.Minute,
		WorkingDays: []int{7}, Accounts: []WeeklyCreditPaceAccountInput{weeklyPaceOneSample("valid", 20, time.Hour)}}) != nil {
		t.Fatal("invalid weekday must be rejected")
	}
	if BuildWeeklyCreditPace(WeeklyCreditPaceInput{Now: weeklyPaceNow, RefreshInterval: time.Minute, SmoothingMinutes: 31,
		Accounts: []WeeklyCreditPaceAccountInput{weeklyPaceOneSample("valid", 20, time.Hour)}}) != nil {
		t.Fatal("invalid smoothing must be rejected")
	}
	nonfinite := weeklyPaceOneSample("bad", 20, time.Hour)
	nonfinite.Quota.UsedPercent = math.Inf(1)
	if weeklyPaceBuild(nonfinite) != nil {
		t.Fatal("non-finite quota must be rejected")
	}
	invalidWindow := weeklyPaceOneSample("invalid-window", 20, time.Hour)
	invalidWindow.Quota.WindowMinutes = pointerTo(32*24*60 + 1)
	if weeklyPaceBuild(invalidWindow) != nil {
		t.Fatal("unbounded quota window must be rejected before calendar simulation")
	}
	future := weeklyPaceOneSample("future", 20, time.Hour)
	future.Quota.ObservedAt = weeklyPaceNow.Add(time.Hour)
	if weeklyPaceBuild(future) != nil {
		t.Fatal("future observation must not count as fresh capacity")
	}

	paused := weeklyPaceOneSample("paused", 20, time.Hour)
	paused.Account.Status = domain.AccountPaused
	pace := weeklyPaceBuild(paused, weeklyPaceOneSample("fresh", 20, time.Hour))
	if pace == nil || pace.InactiveAccountCount != 1 || pace.AccountCount != 1 {
		t.Fatalf("inactive count pace = %+v", pace)
	}
}

func TestWeeklyPaceDTOUsesWebContract(t *testing.T) {
	keyID := "key"
	pace := weeklyPaceBuild(weeklyPaceOneSample("dto", 20, time.Hour))
	pace.TopAPIKeys = []domain.WeeklyCreditAPIKeyAttribution{{APIKeyID: &keyID, Name: "key", DominantModel: "model"}}
	payload, err := json.Marshal(pace)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"totalFullCredits", "actualUsedPercent", "smoothedDeltaPercent", "paceGapSmoothingMinutes",
		"overPlanCredits", "projectedShortfallCredits", "pauseForBreakEvenHours", "paceMultiplier",
		"proAccountsToCoverOverPlan", "projectedMinimumRemainingCredits", "forecastBurnRateCreditsPerHour",
		"burnRateRecentCreditsPerHour", "depletionEtaHours", "nextReliefInHours", "resetEvents",
		"runwayStatus", "topApiKeys", "addProAccounts", "staleAccountCount", "confidence",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON key %s", key)
		}
	}
}
