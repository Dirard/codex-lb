package domain

import (
	"sort"
	"time"
)

type AttentionReasonKind string

const (
	ReasonLimitBlocked AttentionReasonKind = "limit_blocked"
	ReasonLimitNear    AttentionReasonKind = "limit_near"
	ReasonErrorSpike   AttentionReasonKind = "error_spike"
	ReasonExpiring     AttentionReasonKind = "expiring"
	ReasonSpendSpike   AttentionReasonKind = "spend_spike"
)

// attentionOrder is the rank order of the attention-reasons table.
var attentionOrder = map[AttentionReasonKind]int{ReasonLimitBlocked: 0, ReasonLimitNear: 1, ReasonErrorSpike: 2,
	ReasonExpiring: 3, ReasonSpendSpike: 4}

type AttentionReason struct {
	Reason   AttentionReasonKind `json:"reason"`
	Severity Severity            `json:"severity"`
	// Value is the limit percent, the error rate percent, the hours until
	// expiry or the spend today in USD.
	Value *float64 `json:"value"`
}

// AttentionSignals are the inputs of one active key.
type AttentionSignals struct {
	MaxLimit      *LimitState
	ExpiresAt     *time.Time
	LastHour      ErrorWindow
	LastHourKnown bool
	SpendToday    float64
	// PreviousDays are the spends of the 7 local days before today; nil when
	// unknown (rollups unusable).
	PreviousDays []float64
}

// KeyAttentionReasons is the step "collect reasons per key" of use case 2.3,
// ordered by severity and then by the table order.
func KeyAttentionReasons(in AttentionSignals, now time.Time) []AttentionReason {
	var reasons []AttentionReason
	if in.MaxLimit != nil {
		switch {
		case in.MaxLimit.Percent >= KeyLimitCriticalPercent:
			reasons = append(reasons, AttentionReason{ReasonLimitBlocked, SeverityCritical, floatPointer(in.MaxLimit.Percent)})
		case in.MaxLimit.Percent >= KeyLimitWarningPercent:
			reasons = append(reasons, AttentionReason{ReasonLimitNear, SeverityWarning, floatPointer(in.MaxLimit.Percent)})
		}
	}
	if in.LastHourKnown && keyErrorSpike(in.LastHour) {
		reasons = append(reasons, AttentionReason{ReasonErrorSpike, SeverityWarning, percentPointer(in.LastHour.Rate())})
	}
	if severity, ok := expirySeverity(in.ExpiresAt, now); ok {
		reasons = append(reasons, AttentionReason{ReasonExpiring, severity, floatPointer(in.ExpiresAt.Sub(now).Hours())})
	}
	if len(in.PreviousDays) > 0 && in.SpendToday >= SpendSpikeMinUSD {
		var sum float64
		for _, day := range in.PreviousDays {
			sum += day
		}
		if in.SpendToday >= SpendSpikeFactor*sum/float64(len(in.PreviousDays)) {
			reasons = append(reasons, AttentionReason{ReasonSpendSpike, SeverityWarning, floatPointer(in.SpendToday)})
		}
	}
	sort.SliceStable(reasons, func(i, j int) bool { return reasonLess(reasons[i], reasons[j]) })
	return reasons
}

// reasonLess orders reasons by severity, then table order, then value
// (fewest hours first for expiry, otherwise the highest value).
func reasonLess(a, b AttentionReason) bool {
	if severityRank(a.Severity) != severityRank(b.Severity) {
		return severityRank(a.Severity) < severityRank(b.Severity)
	}
	if attentionOrder[a.Reason] != attentionOrder[b.Reason] {
		return attentionOrder[a.Reason] < attentionOrder[b.Reason]
	}
	c := compareFloats(a.Value, b.Value)
	if a.Reason == ReasonExpiring {
		return c < 0
	}
	return c > 0
}

// AttentionKey is one row of the keys-needing-attention table.
type AttentionKey struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Prefix        string            `json:"prefix"`
	GroupID       *string           `json:"groupId"`
	GroupName     *string           `json:"groupName"`
	Reasons       []AttentionReason `json:"reasons"`
	SpendTodayUSD float64           `json:"spendTodayUsd"`
	Spend7dUSD    *float64          `json:"spend7dUsd"`
	MaxLimit      *LimitState       `json:"maxLimit"`
	ExpiresAt     *time.Time        `json:"expiresAt"`
	LastUsedAt    *time.Time        `json:"lastUsedAt"`
	// PrimaryAction is edit_limits, open_group_limits, open_key_errors,
	// extend_expiry or open_key.
	PrimaryAction string `json:"primaryAction"`
}

// PrimaryAttentionAction is the action of a key's top reason.
func PrimaryAttentionAction(top AttentionReasonKind, grouped bool) string {
	switch top {
	case ReasonLimitBlocked, ReasonLimitNear:
		if grouped {
			return "open_group_limits"
		}
		return "edit_limits"
	case ReasonErrorSpike:
		return "open_key_errors"
	case ReasonExpiring:
		return "extend_expiry"
	}
	return "open_key"
}

// RankAttentionKeys is the step "rank by severity then reason order then
// value" of use case 2.3: keys with at least one reason ordered by their top
// reason, then spend today, then name and id. It returns at most limit keys
// and the number of keys with a reason.
func RankAttentionKeys(keys []AttentionKey, limit int) ([]AttentionKey, int) {
	var candidates []AttentionKey
	for _, key := range keys {
		if len(key.Reasons) > 0 {
			candidates = append(candidates, key)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		ra, rb := a.Reasons[0], b.Reasons[0]
		if reasonLess(ra, rb) != reasonLess(rb, ra) {
			return reasonLess(ra, rb)
		}
		if a.SpendTodayUSD != b.SpendTodayUSD {
			return a.SpendTodayUSD > b.SpendTodayUSD
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	total := len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if candidates == nil {
		candidates = []AttentionKey{}
	}
	return candidates, total
}
