package domain

import (
	"errors"
	"testing"
)

func TestLiveUsagePricesAudioDuration(t *testing.T) {
	for _, tc := range []struct {
		ms, want int64
	}{
		{0, 0},
		{60_000, 50_000},       // one minute is $0.05
		{14_600, 12_167},       // the probe call of 2026-11-03: 12166.67 rounds up
		{1, 1},                 // 0.83 rounds to one microdollar
		{3, 3},                 // 2.5 rounds half up
		{7_200_000, 6_000_000}, // a two-hour call is $6
	} {
		amount, err := LiveUsage(tc.ms)
		if err != nil || amount.CostMicrodollars != tc.want || amount.InputTokens != 0 || amount.OutputTokens != 0 {
			t.Fatalf("LiveUsage(%d) = %+v, %v; want cost %d", tc.ms, amount, err, tc.want)
		}
	}
	for _, ms := range []int64{-1, 14*24*3_600_000 + 1} {
		if _, err := LiveUsage(ms); !errors.Is(err, ErrInvalid) {
			t.Fatalf("LiveUsage(%d) error %v", ms, err)
		}
	}
	if budget := LiveCallBudget(); budget.CostMicrodollars != 500_000 || budget.InputTokens != 1 || budget.OutputTokens != 1 {
		t.Fatalf("budget %+v", budget)
	}
}
