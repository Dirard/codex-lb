package domain

import (
	"math"
	"testing"
	"time"
)

var overviewNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := overviewNow.Add(d); return &t }

func burn(v float64) *float64 { return &v }

func TestAccountHealthStateForEveryStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    AccountStatus
		remaining []float64
		credits   bool
		want      AccountHealth
	}{
		{"reauth", AccountReauthRequired, []float64{80}, false, HealthReauth},
		{"paused", AccountPaused, []float64{80}, false, HealthPaused},
		{"deactivated", AccountDeactivated, nil, false, HealthOff},
		{"rate limited", AccountRateLimited, []float64{80}, true, HealthLimited},
		{"quota exceeded", AccountQuotaExceeded, nil, false, HealthLimited},
		{"window at zero", AccountActive, []float64{50, 0}, false, HealthLimited},
		{"purchased credits prevent limited", AccountActive, []float64{50, 0}, true, HealthLow},
		{"low at exactly 10", AccountActive, []float64{10, 90}, false, HealthLow},
		{"above 10 is ok", AccountActive, []float64{10.01, 90}, false, HealthOK},
		{"no windows", AccountActive, nil, false, HealthOK},
	} {
		if got := AccountHealthState(test.status, test.remaining, test.credits); got != test.want {
			t.Errorf("%s: %s, want %s", test.name, got, test.want)
		}
	}
}

func TestProjectRunway(t *testing.T) {
	horizon := overviewNow.Add(5 * time.Hour)
	for _, test := range []struct {
		name     string
		accounts []RunwayAccount
		want     *time.Time
	}{
		{"zero burn makes no projection", []RunwayAccount{{Capacity: 100, Remaining: 10, Burn: 0}}, nil},
		{"lasts until the horizon", []RunwayAccount{{Capacity: 100, Remaining: 50, Burn: 5}}, nil},
		{"linear exhaustion", []RunwayAccount{{Capacity: 100, Remaining: 20, Burn: 10}}, at(2 * time.Hour)},
		{"already empty", []RunwayAccount{{Capacity: 100, Remaining: 0, Burn: 10}}, at(0)},
		// a: 20 left, burns 10/h, resets at +1h having used 80+10=90 → +90.
		// b: 10 left, burns 10/h, no reset. Pool burn 20/h from 30: at +1h 10
		// left plus 90 = 100, then 100/20 = 5 h later, after the horizon.
		{"reset before exhaustion refills", []RunwayAccount{
			{Capacity: 100, Remaining: 20, Burn: 10, ResetAt: at(time.Hour)},
			{Capacity: 100, Remaining: 10, Burn: 10}}, nil},
		// The reset arrives after the pool runs out: 30/20 = 1.5 h.
		{"reset after exhaustion does not help", []RunwayAccount{
			{Capacity: 100, Remaining: 20, Burn: 10, ResetAt: at(2 * time.Hour)},
			{Capacity: 100, Remaining: 10, Burn: 10}}, at(90 * time.Minute)},
		// The refill is capped at capacity: used 90 + 10·4 > 100 → +100.
		{"refill capped at capacity", []RunwayAccount{
			{Capacity: 100, Remaining: 10, Burn: 10, ResetAt: at(4 * time.Hour)},
			{Capacity: 100, Remaining: 40, Burn: 0}}, nil},
	} {
		got := ProjectRunway(test.accounts, overviewNow, horizon)
		if (got == nil) != (test.want == nil) || got != nil && !got.Equal(*test.want) {
			t.Errorf("%s: %v, want %v", test.name, got, test.want)
		}
	}
}

func TestAggregateWindowExcludesUnknownBurnAndSharedAccounts(t *testing.T) {
	shared := WindowAccount{Capacity: 100, Remaining: 20, ResetAt: at(4 * time.Hour), Burn: burn(10), Length: 5 * time.Hour}
	unknown := WindowAccount{Capacity: 100, Remaining: 100, ResetAt: at(3 * time.Hour), Length: 5 * time.Hour}
	group := AggregateWindow([]WindowAccount{shared, unknown}, 3, 5*time.Hour, overviewNow)
	if group.CoveredAccounts != 2 || group.TotalAccounts != 3 {
		t.Fatalf("coverage %d/%d", group.CoveredAccounts, group.TotalAccounts)
	}
	if *group.RemainingPercent != 60 || *group.CapacityCredits != 200 || *group.RemainingCredits != 120 {
		t.Fatalf("remaining %v of %v", *group.RemainingPercent, *group.CapacityCredits)
	}
	if !group.NextResetAt.Equal(*at(3 * time.Hour)) || !group.FullResetAt.Equal(*at(4 * time.Hour)) {
		t.Fatalf("resets %v %v", group.NextResetAt, group.FullResetAt)
	}
	// The unknown-burn account is excluded from the runway: 20/10 = 2 h.
	if *group.BurnCreditsPerHour != 10 || !group.RunsOutBeforeReset || !group.ProjectedExhaustionAt.Equal(*at(2 * time.Hour)) {
		t.Fatalf("projection %v burn %v", group.ProjectedExhaustionAt, *group.BurnCreditsPerHour)
	}
	// The same account in another group counts fully there as well.
	other := AggregateWindow([]WindowAccount{shared}, 1, 5*time.Hour, overviewNow)
	if *other.RemainingPercent != 20 || other.CoveredAccounts != 1 {
		t.Fatalf("shared account %v", *other.RemainingPercent)
	}
	empty := AggregateWindow(nil, 4, 5*time.Hour, overviewNow)
	if empty.RemainingPercent != nil || empty.CapacityCredits != nil || empty.TotalAccounts != 4 || empty.RunsOutBeforeReset {
		t.Fatalf("unknown window must be null: %+v", empty)
	}
	noBurn := AggregateWindow([]WindowAccount{unknown}, 1, 5*time.Hour, overviewNow)
	if noBurn.BurnCreditsPerHour != nil || noBurn.ProjectedExhaustionAt != nil || noBurn.RunsOutBeforeReset {
		t.Fatalf("unknown burn makes no projection: %+v", noBurn)
	}
	// Without a known reset the horizon is the window length.
	horizonless := AggregateWindow([]WindowAccount{{Capacity: 100, Remaining: 30, Burn: burn(10), Length: 2 * time.Hour}}, 1, 5*time.Hour, overviewNow)
	if horizonless.FullResetAt != nil || horizonless.RunsOutBeforeReset {
		t.Fatalf("2 h horizon with 3 h runway: %+v", horizonless)
	}
}

func TestQuotaBurnPerHour(t *testing.T) {
	reset := at(3 * time.Hour)
	obs := func(minutes int, used float64, reset *time.Time) AccountQuota {
		return AccountQuota{UsedPercent: used, ResetAt: reset, ObservedAt: overviewNow.Add(time.Duration(minutes) * time.Minute)}
	}
	if QuotaBurnPerHour([]AccountQuota{obs(-10, 10, reset)}, 100, overviewNow) != nil {
		t.Fatal("one observation must be unknown")
	}
	if QuotaBurnPerHour([]AccountQuota{obs(-400, 10, reset), obs(-10, 20, reset)}, 100, overviewNow) != nil {
		t.Fatal("observations outside 5 h must not count")
	}
	// 10 % then 30 % over one hour of a 200-credit window: 40 credits/h; the
	// reset (new window) and the decrease contribute nothing.
	later := at(8 * time.Hour)
	got := QuotaBurnPerHour([]AccountQuota{obs(-120, 10, reset), obs(-90, 20, reset), obs(-60, 30, reset),
		obs(-30, 0, later), obs(0, 0, later)}, 200, overviewNow)
	if got == nil || math.Abs(*got-20) > 1e-9 {
		t.Fatalf("burn %v, want 20 credits/h over the 2 h span", got)
	}
	jitter := at(3*time.Hour + time.Minute)
	got = QuotaBurnPerHour([]AccountQuota{obs(-60, 10, reset), obs(0, 20, jitter)}, 100, overviewNow)
	if got == nil || math.Abs(*got-10) > 1e-9 {
		t.Fatalf("reset jitter is one window: %v", got)
	}
}

func TestFullestLimit(t *testing.T) {
	reset := overviewNow.Add(time.Hour)
	limit := FullestLimit([]LimitRule{{Type: LimitTotalTokens, Window: WindowDaily, MaxValue: 100, CurrentValue: 50, ResetAt: reset},
		{Type: LimitCostUSD, Window: WindowWeekly, MaxValue: 10, CurrentValue: 9, ResetAt: reset}})
	if limit == nil || limit.Type != LimitCostUSD || limit.Percent != 90 {
		t.Fatalf("fullest %+v", limit)
	}
	if FullestLimit(nil) != nil {
		t.Fatal("no limits")
	}
}

func TestGroupRowOrdering(t *testing.T) {
	id := func(v string) *string { return &v }
	rows := []GroupRow{
		{ID: id("b"), Name: "b", SpendTodayUSD: 50},
		{ID: id("a"), Name: "a", SpendTodayUSD: 50},
		{ID: id("spend"), Name: "spend", SpendTodayUSD: 80},
		{ID: id("near"), Name: "near", LimitFill: LimitFill{KeysAtOrAbove90: 1}},
		{ID: id("problem"), Name: "problem", Accounts: HealthCounts{Low: 1}},
		{ID: id("warning"), Name: "warning", Score: 1},
		{ID: nil, Name: "ungrouped", Score: 2},
	}
	want := []string{"ungrouped", "warning", "problem", "near", "spend", "a", "b"}
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			if GroupRowLess(rows[j], rows[i]) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	for i, row := range rows {
		if row.Name != want[i] {
			t.Fatalf("order %d: %s, want %s", i, row.Name, want[i])
		}
	}
}
