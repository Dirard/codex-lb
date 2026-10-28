package domain

import (
	"math"
	"sort"
	"time"
)

// AccountHealth is the overview's state of one ChatGPT account (dashboard
// redesign use case 2.1, step "aggregate account health").
type AccountHealth string

const (
	HealthOK      AccountHealth = "ok"
	HealthLow     AccountHealth = "low"
	HealthLimited AccountHealth = "limited"
	HealthReauth  AccountHealth = "reauth"
	HealthPaused  AccountHealth = "paused"
	HealthOff     AccountHealth = "off"
)

// LowRemainingPercent is the remaining share of a window at or below which an
// active account is low.
const LowRemainingPercent = 10.0

// AccountHealthState classifies an account from its effective status, the
// remaining percent of each known current window and whether purchased
// credits are usable: re-login, paused and deactivated states win; a rate
// limit, an exceeded quota or a window at 0 % without usable credits is
// limited; a window at or below 10 % is low.
func AccountHealthState(status AccountStatus, remaining []float64, creditsUsable bool) AccountHealth {
	switch status {
	case AccountReauthRequired:
		return HealthReauth
	case AccountPaused:
		return HealthPaused
	case AccountDeactivated:
		return HealthOff
	case AccountRateLimited, AccountQuotaExceeded:
		return HealthLimited
	}
	low := false
	for _, value := range remaining {
		if value <= 0 && !creditsUsable {
			return HealthLimited
		}
		if value <= LowRemainingPercent {
			low = true
		}
	}
	if low {
		return HealthLow
	}
	return HealthOK
}

// healthRank orders problem accounts: the states needing an action first.
func healthRank(health AccountHealth) int {
	switch health {
	case HealthReauth:
		return 0
	case HealthLimited:
		return 1
	case HealthPaused:
		return 2
	case HealthOff:
		return 3
	case HealthLow:
		return 4
	}
	return 5
}

// HealthCounts counts accounts by health state.
type HealthCounts struct {
	OK      int `json:"ok"`
	Low     int `json:"low"`
	Limited int `json:"limited"`
	Reauth  int `json:"reauth"`
	Paused  int `json:"paused"`
	Off     int `json:"off"`
}

func (c *HealthCounts) Add(health AccountHealth) {
	switch health {
	case HealthOK:
		c.OK++
	case HealthLow:
		c.Low++
	case HealthLimited:
		c.Limited++
	case HealthReauth:
		c.Reauth++
	case HealthPaused:
		c.Paused++
	case HealthOff:
		c.Off++
	}
}

// Problems is the number of accounts that are not ok.
func (c HealthCounts) Problems() int { return c.Low + c.Limited + c.Reauth + c.Paused + c.Off }

// WindowHealth is the capacity-weighted state of one quota window over a set
// of accounts. Unknown values are nil, never zero.
type WindowHealth struct {
	RemainingPercent      *float64   `json:"remainingPercent"`
	RemainingCredits      *float64   `json:"remainingCredits"`
	CapacityCredits       *float64   `json:"capacityCredits"`
	CoveredAccounts       int        `json:"coveredAccounts"`
	TotalAccounts         int        `json:"totalAccounts"`
	NextResetAt           *time.Time `json:"nextResetAt"`
	FullResetAt           *time.Time `json:"fullResetAt"`
	BurnCreditsPerHour    *float64   `json:"burnCreditsPerHour"`
	ProjectedExhaustionAt *time.Time `json:"projectedExhaustionAt"`
	RunsOutBeforeReset    bool       `json:"runsOutBeforeReset"`
}

// WindowAccount is one account with a current observation and a known
// capacity of a window, in credits; Burn is credits per hour, nil when
// unknown.
type WindowAccount struct {
	Capacity, Remaining float64
	ResetAt             *time.Time
	Burn                *float64
	Length              time.Duration
}

// AggregateWindow is the workflow "covered accounts --> capacity-weighted
// remaining, resets and burn --> runway projection" of use case 2.1. total
// counts every account of the scope, covered or not; defaultLength is the
// window length used when no account reports one.
func AggregateWindow(accounts []WindowAccount, total int, defaultLength time.Duration, now time.Time) WindowHealth {
	health := WindowHealth{CoveredAccounts: len(accounts), TotalAccounts: total}
	if len(accounts) == 0 {
		return health
	}
	var capacity, remaining, burn float64
	burnKnown := false
	length := time.Duration(0)
	runway := make([]RunwayAccount, 0, len(accounts))
	for _, account := range accounts {
		capacity += account.Capacity
		remaining += account.Remaining
		length = max(length, account.Length)
		if account.ResetAt != nil && account.ResetAt.After(now) {
			if health.NextResetAt == nil || account.ResetAt.Before(*health.NextResetAt) {
				health.NextResetAt = timePointer(*account.ResetAt)
			}
			if health.FullResetAt == nil || account.ResetAt.After(*health.FullResetAt) {
				health.FullResetAt = timePointer(*account.ResetAt)
			}
		}
		if account.Burn != nil {
			burnKnown = true
			burn += *account.Burn
			runway = append(runway, RunwayAccount{Capacity: account.Capacity, Remaining: account.Remaining,
				Burn: *account.Burn, ResetAt: account.ResetAt})
		}
	}
	health.CapacityCredits = floatPointer(capacity)
	health.RemainingCredits = floatPointer(remaining)
	if capacity > 0 {
		health.RemainingPercent = floatPointer(max(0, min(100, remaining/capacity*100)))
	}
	horizon := health.FullResetAt
	if horizon == nil {
		if length <= 0 {
			length = defaultLength
		}
		horizon = timePointer(now.Add(length))
	}
	if burnKnown {
		health.BurnCreditsPerHour = floatPointer(burn)
		health.ProjectedExhaustionAt = ProjectRunway(runway, now, *horizon)
		health.RunsOutBeforeReset = health.ProjectedExhaustionAt != nil
	}
	return health
}

// RunwayAccount is one account of a runway projection.
type RunwayAccount struct {
	Capacity, Remaining, Burn float64
	ResetAt                   *time.Time
}

// ProjectRunway returns the first instant t in [now, horizon] at which the
// pooled remaining credits
//
//	R(t) = ΣR_a − B(t − now) + Σ_{now < ρ_a ≤ t} min(C_a, C_a − R_a + b_a(ρ_a − now))
//
// reach zero, or nil when they last until horizon or the burn B is zero. The
// runway is linear between reset instants, where an account regains what it
// used.
func ProjectRunway(accounts []RunwayAccount, now, horizon time.Time) *time.Time {
	var burn, value float64
	for _, account := range accounts {
		burn += account.Burn
		value += account.Remaining
	}
	if burn <= 0 || math.IsNaN(burn) || math.IsInf(burn, 0) {
		return nil
	}
	if value <= 0 {
		return timePointer(now)
	}
	type refill struct {
		at     time.Time
		amount float64
	}
	var refills []refill
	for _, account := range accounts {
		if account.ResetAt == nil || !account.ResetAt.After(now) || account.ResetAt.After(horizon) {
			continue
		}
		hours := account.ResetAt.Sub(now).Hours()
		used := account.Capacity - account.Remaining + account.Burn*hours
		refills = append(refills, refill{*account.ResetAt, max(0, min(account.Capacity, used))})
	}
	sort.Slice(refills, func(i, j int) bool { return refills[i].at.Before(refills[j].at) })
	at := now
	exhausted := func(until time.Time) (*time.Time, bool) {
		hours := until.Sub(at).Hours()
		if value-burn*hours <= 0 {
			result := at.Add(time.Duration(value / burn * float64(time.Hour)))
			return &result, true
		}
		value -= burn * hours
		at = until
		return nil, false
	}
	for _, next := range refills {
		if result, ok := exhausted(next.at); ok {
			return result
		}
		value += next.amount
	}
	if result, ok := exhausted(horizon); ok {
		return result
	}
	return nil
}

// QuotaBurnWindow is the trailing history from which account burn is taken.
const QuotaBurnWindow = 5 * time.Hour

// quotaSameWindowTolerance separates reset-time jitter between observations
// of one window from a new window, which starts at least one window later.
const quotaSameWindowTolerance = 30 * time.Minute

// QuotaBurnPerHour is the burn of one account window in credits per hour:
// the positive used-percent deltas between consecutive observations within
// one reset window over the trailing 5 hours, times capacity over 100,
// divided by the observed span. History is oldest first; fewer than two
// observations in the trailing window make the burn unknown.
func QuotaBurnPerHour(history []AccountQuota, capacity float64, now time.Time) *float64 {
	from := now.Add(-QuotaBurnWindow)
	var recent []AccountQuota
	for _, row := range history {
		if !row.ObservedAt.Before(from) && !row.ObservedAt.After(now) {
			recent = append(recent, row)
		}
	}
	if len(recent) < 2 {
		return nil
	}
	span := recent[len(recent)-1].ObservedAt.Sub(recent[0].ObservedAt).Hours()
	if span <= 0 {
		return nil
	}
	var used float64
	for i := 1; i < len(recent); i++ {
		previous, current := recent[i-1], recent[i]
		if !sameQuotaWindow(previous.ResetAt, current.ResetAt) || current.UsedPercent <= previous.UsedPercent {
			continue
		}
		used += current.UsedPercent - previous.UsedPercent
	}
	return floatPointer(used / 100 * capacity / span)
}

func sameQuotaWindow(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	delta := a.Sub(*b)
	return delta < quotaSameWindowTolerance && delta > -quotaSameWindowTolerance
}

// LimitState is the fill of one active key limit, with expired windows
// projected to zero and their advanced reset.
type LimitState struct {
	Type        LimitType   `json:"type"`
	Window      LimitWindow `json:"window"`
	ModelFilter *string     `json:"modelFilter"`
	Percent     float64     `json:"percent"`
	ResetAt     time.Time   `json:"resetAt"`
}

// FullestLimit returns the active limit with the highest current/max share,
// or nil without limits. Limits must already be projected for expired windows.
func FullestLimit(limits []LimitRule) *LimitState {
	var best *LimitState
	for _, limit := range limits {
		if limit.MaxValue <= 0 {
			continue
		}
		percent := float64(limit.CurrentValue) / float64(limit.MaxValue) * 100
		if best == nil || percent > best.Percent {
			best = &LimitState{Type: limit.Type, Window: limit.Window, ModelFilter: limit.ModelFilter,
				Percent: percent, ResetAt: limit.ResetAt}
		}
	}
	return best
}

func floatPointer(value float64) *float64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }
