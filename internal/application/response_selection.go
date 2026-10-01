package application

import (
	"bytes"
	"crypto/sha256"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type accountCandidate struct {
	account                      domain.Account
	price                        pricing.Price
	primary, secondary           float64
	secondaryKnown               bool
	primaryReset, secondaryReset *time.Time
	last                         uint64
}

func (c accountCandidate) weeklyUsed() float64 {
	if c.secondaryKnown {
		return c.secondary
	}
	return c.primary
}
func (c accountCandidate) remaining() float64 {
	return planCapacity(c.account.PlanType) * (1 - c.weeklyUsed()/100)
}
func (c accountCandidate) reset(window string) *time.Time {
	if window == "primary" {
		if c.primaryReset != nil {
			return c.primaryReset
		}
		return c.secondaryReset
	}
	if c.secondaryReset != nil {
		return c.secondaryReset
	}
	return c.primaryReset
}

// These are legacy routing weights, not a promise of purchased credits or a
// replacement for authoritative provider usage/reset timestamps.
func planCapacity(plan string) float64 {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "plus", "business", "team", "edu", "education", "k12":
		return 7560
	case "pro", "enterprise":
		return 50400
	case "prolite":
		return 37800
	default:
		return 1134
	}
}

func lessUsage(a, b accountCandidate) bool {
	if a.weeklyUsed() != b.weeklyUsed() {
		return a.weeklyUsed() < b.weeklyUsed()
	}
	if a.primary != b.primary {
		return a.primary < b.primary
	}
	if a.last != b.last {
		return a.last < b.last
	}
	return a.account.ID < b.account.ID
}

func (p *Proxy) chooseAccount(choices []accountCandidate, settings domain.RuntimeSettings, preferredAccountID ...string) (accountCandidate, error) {
	p.selectionMu.Lock()
	defer p.selectionMu.Unlock()
	if p.lastSelected == nil {
		p.lastSelected = make(map[string]uint64)
	}
	for i := range choices {
		choices[i].last = p.lastSelected[choices[i].account.ID]
	}
	// Manual burn/preserve policy applies only to new ownership; existing owned
	// continuations never enter this candidate chooser.
	priority := func(c accountCandidate) int {
		switch c.account.RoutingPolicy {
		case "burn_first":
			return 0
		case "preserve":
			return 2
		default:
			return 1
		}
	}
	best := 3
	for _, choice := range choices {
		best = min(best, priority(choice))
	}
	filtered := choices[:0]
	for _, choice := range choices {
		if priority(choice) == best {
			filtered = append(filtered, choice)
		}
	}
	choices = filtered
	now := time.Now()
	if settings.PreferEarlierResetAccounts && (settings.RoutingStrategy == "usage_weighted" || settings.RoutingStrategy == "capacity_weighted" || settings.RoutingStrategy == "fill_first") {
		bucket := func(c accountCandidate) int64 {
			at := c.reset(settings.PreferEarlierResetWindow)
			if at == nil {
				return 10_000
			}
			return max(0, int64(at.Sub(now).Hours()/24))
		}
		bestBucket := int64(10_000)
		for _, choice := range choices {
			bestBucket = min(bestBucket, bucket(choice))
		}
		filtered = choices[:0]
		for _, choice := range choices {
			if bucket(choice) == bestBucket {
				filtered = append(filtered, choice)
			}
		}
		choices = filtered
	}
	// Locality is subordinate to manual policy and the drain/single-account
	// strategies. The caller supplies only currently eligible, capacity-free
	// candidates, so an unavailable pin cannot hold this selection hostage.
	supportsAffinity := false
	switch settings.RoutingStrategy {
	case "round_robin", "usage_weighted", "fill_first", "capacity_weighted", "relative_availability":
		supportsAffinity = true
	}
	if len(preferredAccountID) != 0 && preferredAccountID[0] != "" && supportsAffinity && best != 0 {
		var pinned *accountCandidate
		for i := range choices {
			if choices[i].account.ID == preferredAccountID[0] {
				pinned = &choices[i]
				break
			}
		}
		if pinned != nil {
			healthy := func(c accountCandidate) bool {
				return c.primary <= settings.StickyReallocationPrimaryBudgetThresholdPct &&
					(!c.secondaryKnown || c.secondary <= settings.StickyReallocationSecondaryBudgetThresholdPct)
			}
			if healthy(*pinned) {
				p.lastSelected[pinned.account.ID] = p.nextAccount.Add(1)
				return *pinned, nil
			}
			filtered = choices[:0]
			for _, choice := range choices {
				if healthy(choice) {
					filtered = append(filtered, choice)
				}
			}
			if len(filtered) == 0 {
				p.lastSelected[pinned.account.ID] = p.nextAccount.Add(1)
				return *pinned, nil
			}
			choices = filtered
		}
	}
	index := 0
	switch settings.RoutingStrategy {
	case "round_robin":
		sort.SliceStable(choices, func(i, j int) bool { return choices[i].last < choices[j].last })
	case "single_account":
		sort.SliceStable(choices, func(i, j int) bool { return choices[i].account.ID < choices[j].account.ID })
	case "sequential_drain":
		sort.SliceStable(choices, func(i, j int) bool {
			a, b := planCapacity(choices[i].account.PlanType), planCapacity(choices[j].account.PlanType)
			if a != b {
				return a < b
			}
			return stableAccountLess(choices[i], choices[j])
		})
	case "usage_weighted":
		sort.SliceStable(choices, func(i, j int) bool { return lessUsage(choices[i], choices[j]) })
	case "fill_first":
		sort.SliceStable(choices, func(i, j int) bool {
			if choices[i].primary != choices[j].primary {
				return choices[i].primary > choices[j].primary
			}
			if choices[i].secondary != choices[j].secondary {
				return choices[i].secondary > choices[j].secondary
			}
			return choices[i].account.ID < choices[j].account.ID
		})
	case "reset_drain":
		sort.SliceStable(choices, func(i, j int) bool { return resetDrainLess(choices[i], choices[j], now) })
	case "capacity_weighted":
		weights := make([]float64, len(choices))
		for i, choice := range choices {
			weights[i] = choice.remaining()
		}
		index = weightedIndex(weights, rand.Float64())
	case "relative_availability":
		weights := relativeAvailabilityWeights(choices, settings.RelativeAvailabilityPower, settings.RelativeAvailabilityTopK, now)
		index = weightedIndex(weights, rand.Float64())
	default:
		return accountCandidate{}, &ProxyError{Code: "invalid_routing_strategy", Status: 503, Message: "Invalid routing strategy"}
	}
	selected := choices[index]
	p.lastSelected[selected.account.ID] = p.nextAccount.Add(1)
	return selected, nil
}

func stableAccountLess(a, b accountCandidate) bool {
	x, y := sha256.Sum256([]byte(a.account.ID)), sha256.Sum256([]byte(b.account.ID))
	if compared := bytes.Compare(x[:], y[:]); compared != 0 {
		return compared < 0
	}
	return a.account.ID < b.account.ID
}

func resetDrainLess(a, b accountCandidate, now time.Time) bool {
	rank := func(c accountCandidate) (int64, time.Time) {
		for _, reset := range []*time.Time{c.secondaryReset, c.primaryReset} {
			if reset != nil && reset.After(now) {
				return int64(reset.Sub(now).Hours() / 24), *reset
			}
		}
		return 10_000, time.Time{}
	}
	aBucket, aReset := rank(a)
	bBucket, bReset := rank(b)
	if aBucket != bBucket {
		return aBucket < bBucket
	}
	if a.primary != b.primary {
		return a.primary < b.primary
	}
	if a.weeklyUsed() != b.weeklyUsed() {
		return a.weeklyUsed() < b.weeklyUsed()
	}
	if !aReset.Equal(bReset) {
		return aReset.Before(bReset)
	}
	return stableAccountLess(a, b)
}

// relativeAvailabilityWeights applies persisted tuning to the eligible pool
// using one clock snapshot; the corresponding candidates are sorted in-place.
func relativeAvailabilityWeights(choices []accountCandidate, power float64, topK int, now time.Time) []float64 {
	score := func(c accountCandidate) float64 {
		seconds := 7 * 24 * 3600.0
		if c.secondaryReset != nil {
			seconds = max(300, c.secondaryReset.Sub(now).Seconds())
		}
		return max(0, c.remaining()) / seconds
	}
	sort.SliceStable(choices, func(i, j int) bool {
		a, b := score(choices[i]), score(choices[j])
		if a != b {
			return a > b
		}
		return lessUsage(choices[i], choices[j])
	})
	bestScore := score(choices[0])
	weights := make([]float64, 0, min(topK, len(choices)))
	if bestScore > 0 {
		for _, choice := range choices[:min(topK, len(choices))] {
			weight := math.Pow(score(choice)/bestScore, power)
			if weight < 0.1 {
				break
			}
			weights = append(weights, weight)
		}
	}
	return weights
}

func weightedIndex(weights []float64, sample float64) int {
	total := 0.0
	for _, weight := range weights {
		total += max(0, weight)
	}
	point := sample * total
	for i, weight := range weights {
		point -= max(0, weight)
		if point < 0 {
			return i
		}
	}
	return 0
}
