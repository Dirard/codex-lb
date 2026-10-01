package domain

import (
	"strings"
	"time"
)

// AccountCreditStatus is purchased capacity reported by upstream, distinct
// from subscription credits inferred from an account's plan.
type AccountCreditStatus struct {
	AccountID  string
	Has        *bool
	Unlimited  *bool
	Balance    *float64
	ObservedAt time.Time
}

func (c AccountCreditStatus) Usable() bool {
	if c.Unlimited != nil && *c.Unlimited {
		return true
	}
	if c.Balance != nil {
		return *c.Balance > 0
	}
	return c.Has != nil && *c.Has
}

func (c AccountCreditStatus) UsableAfter(refusalAt *time.Time) bool {
	return c.Usable() && (refusalAt == nil || c.ObservedAt.After(*refusalAt))
}

// SubscriptionQuotaWindows resolves weekly-only legacy labels and Free monthly
// windows consistently for admission and group subscription reporting.
func SubscriptionQuotaWindows(plan string, quotas []AccountQuota) (primary, long *AccountQuota) {
	var monthly *AccountQuota
	for i := range quotas {
		switch quotas[i].Window {
		case "primary":
			primary = &quotas[i]
		case "secondary":
			long = &quotas[i]
		case "monthly":
			monthly = &quotas[i]
		}
	}
	if strings.EqualFold(plan, "free") && monthly != nil {
		return nil, monthly
	}
	if primary != nil && primary.WindowMinutes != nil && *primary.WindowMinutes == 10080 {
		if long == nil || primary.ObservedAt.After(long.ObservedAt.Add(5*time.Second)) ||
			!long.ObservedAt.After(primary.ObservedAt.Add(5*time.Second)) &&
				primary.ResetAt != nil && (long.ResetAt == nil || primary.ResetAt.After(*long.ResetAt)) {
			return nil, primary
		}
	}
	return primary, long
}

// SubscriptionCreditCapacity preserves the legacy plan estimates. Unknown
// plans/windows have no estimate; purchased credits are not included.
func SubscriptionCreditCapacity(plan, window string) *float64 {
	plan = strings.ToLower(strings.TrimSpace(plan))
	var capacity float64
	if window == "monthly" {
		if plan != "free" {
			return nil
		}
		capacity = 1134
	} else if window == "primary" || window == "secondary" {
		var primary, secondary float64
		switch plan {
		case "free":
			primary, secondary = 0, 1134
		case "plus", "business", "team", "edu":
			primary, secondary = 225, 7560
		case "pro", "enterprise":
			primary, secondary = 1500, 50400
		case "prolite":
			primary, secondary = 1125, 37800
		default:
			return nil
		}
		capacity = secondary
		if window == "primary" {
			capacity = primary
		}
	} else {
		return nil
	}
	return &capacity
}

type AccountAdditionalQuota struct {
	AccountID      string
	QuotaKey       string
	LimitName      string
	MeteredFeature string
	Window         string
	UsedPercent    float64
	ResetAt        *time.Time
	WindowMinutes  *int
	ObservedAt     time.Time
}

type AccountUsageSnapshot struct {
	AccountID              string
	ObservedAt             time.Time
	FetchStartedAt         time.Time
	ExpectedAccount        *Account
	ExpectedCredential     *AccountCredential
	ReportedPlanType       string
	ReportedWorkspaceID    string
	ReportedWorkspaceLabel string
	ReportedSeatType       string
	Quotas                 []AccountQuota
	ReplaceQuotaWindows    bool // Complete nonempty standard observation; partial telemetry remains merge-only.
	Credits                *AccountCreditStatus
	AdditionalQuotas       []AccountAdditionalQuota
	AdditionalReported     bool // false means upstream omitted the field; true with no rows clears old quotas.
}

// The shipped legacy registry has one separately metered model. Unknown
// telemetry is retained for display, but never grants model-specific routing.
type AdditionalQuotaDefinition struct {
	QuotaKey      string
	DisplayLabel  string
	RoutingPolicy string
	Plans         []string
}

var codexSparkQuota = AdditionalQuotaDefinition{
	QuotaKey: "codex_spark", DisplayLabel: "GPT-5.3-Codex-Spark", RoutingPolicy: "burn_first",
	Plans: []string{"pro", "prolite", "team", "business", "enterprise"},
}

const AdditionalQuotaFreshness = 3 * time.Minute
const AdditionalQuotaBlockCooldown = 120 * time.Second

func AdditionalQuotaAppliesToPlan(plan string, definition AdditionalQuotaDefinition) bool {
	plan = strings.ToLower(strings.TrimSpace(plan))
	for _, allowed := range definition.Plans {
		if allowed == plan {
			return true
		}
	}
	return plan != "free" && plan != "plus" && plan != "edu"
}

func AdditionalQuotaForModel(model string) (AdditionalQuotaDefinition, bool) {
	if normalizeAdditionalID(model) == "gpt_5_3_codex_spark" {
		return codexSparkQuota, true
	}
	return AdditionalQuotaDefinition{}, false
}

func KnownAdditionalQuota(key string) (AdditionalQuotaDefinition, bool) {
	if CanonicalAdditionalQuotaKey(key, "", "") == codexSparkQuota.QuotaKey {
		return codexSparkQuota, true
	}
	return AdditionalQuotaDefinition{}, false
}

func CanonicalAdditionalQuotaKey(quotaKey, limitName, meteredFeature string) string {
	for _, value := range []string{quotaKey, limitName, meteredFeature} {
		switch normalizeAdditionalID(value) {
		case "codex_spark", "codex_other", "gpt_5_3_codex_spark", "codex_bengalfox":
			return codexSparkQuota.QuotaKey
		}
	}
	for _, value := range []string{limitName, meteredFeature, quotaKey} {
		if normalized := normalizeAdditionalID(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeAdditionalID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	underscore := false
	for _, ch := range value {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			if underscore && out.Len() > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(ch)
			underscore = false
		} else {
			underscore = true
		}
	}
	return out.String()
}
