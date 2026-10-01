package application

import (
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestDrainStrategiesPreserveCapacityResetAndStableOrder(t *testing.T) {
	now := time.Now()
	soon, later, tomorrow, expired := now.Add(time.Hour), now.Add(8*time.Hour), now.Add(36*time.Hour), now.Add(-time.Hour)
	candidate := func(id, plan string, used float64, primary, secondary *time.Time) accountCandidate {
		return accountCandidate{account: domain.Account{ID: id, PlanType: plan}, primary: used, secondary: used,
			secondaryKnown: true, primaryReset: primary, secondaryReset: secondary}
	}
	for _, test := range []struct {
		name, strategy, want string
		choices              []accountCandidate
	}{
		{"capacity_not_id_or_remaining", "sequential_drain", "z", []accountCandidate{
			candidate("a", "pro", 99, &soon, &soon), candidate("z", "plus", 0, &tomorrow, &tomorrow)}},
		{"same_capacity_stable_hash", "sequential_drain", "b", []accountCandidate{
			candidate("a", "plus", 0, nil, nil), candidate("b", "plus", 40, nil, nil)}},
		{"same_reset_day_more_remaining", "reset_drain", "b", []accountCandidate{
			candidate("a", "plus", 90, &soon, &soon), candidate("b", "plus", 10, &later, &later)}},
		{"secondary_before_primary_preference", "reset_drain", "b", []accountCandidate{
			candidate("a", "plus", 10, &soon, &tomorrow), candidate("b", "plus", 90, &tomorrow, &soon)}},
		{"expired_secondary_uses_primary", "reset_drain", "a", []accountCandidate{
			candidate("a", "plus", 10, &soon, &expired), candidate("b", "plus", 10, &tomorrow, &later)}},
		{"unknown_reset_last", "reset_drain", "b", []accountCandidate{
			candidate("a", "plus", 0, nil, nil), candidate("b", "plus", 90, &tomorrow, nil)}},
		{"same_reset_stable_hash", "reset_drain", "b", []accountCandidate{
			candidate("a", "plus", 10, &soon, &soon), candidate("b", "plus", 10, &soon, &soon)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := &Proxy{}
			settings := domain.RuntimeSettings{RoutingStrategy: test.strategy, PreferEarlierResetAccounts: true, PreferEarlierResetWindow: "primary"}
			for range 3 {
				picked, err := proxy.chooseAccount(slices.Clone(test.choices), settings)
				if err != nil || picked.account.ID != test.want {
					t.Fatalf("picked %s, want %s: %v", picked.account.ID, test.want, err)
				}
			}
		})
	}
	preserved := candidate("free", "free", 0, nil, nil)
	preserved.account.RoutingPolicy = "preserve"
	chosen, err := (&Proxy{}).chooseAccount([]accountCandidate{preserved, candidate("normal", "pro", 80, nil, nil)}, domain.RuntimeSettings{RoutingStrategy: "sequential_drain"})
	if err != nil || chosen.account.ID != "normal" {
		t.Fatal("drain bypassed explicit preserve policy")
	}
}

func TestAffinityPreferenceRespectsPolicyPressureAndAvailablePool(t *testing.T) {
	candidate := func(id, policy string, primary, secondary float64) accountCandidate {
		return accountCandidate{account: domain.Account{ID: id, PlanType: "plus", RoutingPolicy: policy},
			primary: primary, secondary: secondary, secondaryKnown: true}
	}
	settings := domain.RuntimeSettings{RoutingStrategy: "usage_weighted",
		StickyReallocationPrimaryBudgetThresholdPct: 95, StickyReallocationSecondaryBudgetThresholdPct: 90}
	for _, test := range []struct {
		name      string
		choices   []accountCandidate
		preferred string
		want      string
	}{
		{"healthy_pin", []accountCandidate{candidate("pin", "", 95, 90), candidate("other", "", 5, 5)}, "pin", "pin"},
		{"primary_pressure", []accountCandidate{candidate("pin", "", 96, 10), candidate("other", "", 20, 20)}, "pin", "other"},
		{"secondary_pressure", []accountCandidate{candidate("pin", "", 20, 91), candidate("other", "", 30, 20)}, "pin", "other"},
		{"all_pressured", []accountCandidate{candidate("pin", "", 96, 91), candidate("other", "", 97, 20)}, "pin", "pin"},
		{"unknown_secondary_is_not_primary", []accountCandidate{{account: domain.Account{ID: "pin", PlanType: "plus"}, primary: 95}, candidate("other", "", 20, 20)}, "pin", "pin"},
		{"unknown_secondary_alternative", []accountCandidate{candidate("pin", "", 20, 91), {account: domain.Account{ID: "other", PlanType: "plus"}, primary: 30}}, "pin", "other"},
		{"missing_pin_has_no_pressure_ban", []accountCandidate{candidate("other", "", 97, 91)}, "pin", "other"},
		{"capacity_excluded_pin", []accountCandidate{candidate("other", "", 10, 10)}, "pin", "other"},
		{"manual_burn_first", []accountCandidate{candidate("pin", "", 10, 10), candidate("burn", "burn_first", 20, 20)}, "pin", "burn"},
		{"manual_preserve", []accountCandidate{candidate("pin", "preserve", 10, 10), candidate("normal", "", 20, 20)}, "pin", "normal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := (&Proxy{}).chooseAccount(slices.Clone(test.choices), settings, test.preferred)
			if err != nil || selected.account.ID != test.want {
				t.Fatalf("selected %s, want %s: %v", selected.account.ID, test.want, err)
			}
		})
	}
	settings.RoutingStrategy = "fill_first"
	selected, err := (&Proxy{}).chooseAccount([]accountCandidate{
		candidate("pin", "burn_first", 10, 10), candidate("burn", "burn_first", 90, 90),
	}, settings, "pin")
	if err != nil || selected.account.ID != "burn" {
		t.Fatalf("soft pin overrode explicit burn-first order: %s: %v", selected.account.ID, err)
	}
	choices := []accountCandidate{candidate("pin", "", 10, 10), candidate("other", "", 20, 20)}
	for _, strategy := range []string{"sequential_drain", "reset_drain", "single_account"} {
		settings.RoutingStrategy = strategy
		without, err := (&Proxy{}).chooseAccount(slices.Clone(choices), settings)
		if err != nil {
			t.Fatal(err)
		}
		with, err := (&Proxy{}).chooseAccount(slices.Clone(choices), settings, "other")
		if err != nil || with.account.ID != without.account.ID {
			t.Fatalf("%s changed by affinity: %s vs %s: %v", strategy, with.account.ID, without.account.ID, err)
		}
	}
}

func TestRelativeAvailabilityUsesConfiguredPowerAndTopK(t *testing.T) {
	now := time.Unix(1000, 0)
	reset := now.Add(time.Hour)
	choices := []accountCandidate{}
	for index, used := range []float64{80, 50, 0} {
		choices = append(choices, accountCandidate{account: domain.Account{ID: string(rune('a' + index)), PlanType: "plus"},
			secondaryKnown: true, secondary: used, secondaryReset: &reset})
	}
	for _, test := range []struct {
		power float64
		topK  int
		want  []float64
	}{{1, 3, []float64{1, .5, .2}}, {2, 3, []float64{1, .25}}, {1, 1, []float64{1}}, {8, 3, []float64{1}}} {
		candidates := slices.Clone(choices)
		weights := relativeAvailabilityWeights(candidates, test.power, test.topK, now)
		if len(weights) != len(test.want) {
			t.Fatalf("power=%v topK=%d: %v", test.power, test.topK, weights)
		}
		for index, weight := range weights {
			if math.Abs(weight-test.want[index]) > 1e-12 {
				t.Fatalf("incorrect weight: %v", weights)
			}
		}
		if !reflect.DeepEqual([]string{candidates[0].account.ID, candidates[1].account.ID, candidates[2].account.ID}, []string{"c", "b", "a"}) {
			t.Fatal("top-K did not use ranked candidate order")
		}
	}
}
